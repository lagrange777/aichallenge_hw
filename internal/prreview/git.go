// Package prreview reviews Git objects as data, never executing candidate code.
package prreview

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxFiles = 24
const MaxPatchBytes = 80000
const MaxBlobBytes = 128 << 10

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var hunkPattern = regexp.MustCompile(`(?m)^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

type File struct {
	Path         string       `json:"path"`
	PreviousPath string       `json:"previousPath,omitempty"`
	Status       string       `json:"status"`
	Reason       string       `json:"skipped,omitempty"`
	Patch        string       `json:"patch,omitempty"`
	Added        map[int]bool `json:"-"`
	Deleted      map[int]bool `json:"-"`
}

type Input struct {
	Base          string   `json:"base"`
	Head          string   `json:"head"`
	MergeBase     string   `json:"mergeBase"`
	Files         []File   `json:"files"`
	Sources       []Source `json:"sources"`
	CorpusFiles   int      `json:"corpusFiles"`
	CorpusChunks  int      `json:"corpusChunks"`
	CorpusSkipped int      `json:"corpusSkipped"`
}

type Repository struct{ Root string }

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("git output exceeds limit")
	}
	return b.Buffer.Write(p)
}
func (r Repository) git(ctx context.Context, limit int, args ...string) ([]byte, error) {
	root, err := filepath.Abs(r.Root)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager", "-c", "safe.directory=" + root, "-c", "core.hooksPath=/dev/null", "-c", "core.quotePath=false"}, args...)...)
	cmd.Dir = r.Root
	// Git must not inherit credentials, custom config, external diff or a PR's hooks.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
	buf := &cappedBuffer{limit: limit}
	cmd.Stdout = buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s failed or exceeded limit", args[0])
	}
	return buf.Bytes(), nil
}

func allowed(p string) bool {
	if p == "" || path.Clean(p) != p || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\r\n\t\\") {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if strings.HasPrefix(s, ".") && s != ".github" {
			return false
		}
	}
	if strings.HasPrefix(p, "artifacts/") || strings.HasPrefix(p, "data/") || strings.HasPrefix(p, "vendor/") || strings.HasPrefix(p, "tools/") || strings.HasPrefix(p, "documents/") {
		return false
	}
	if p == "Dockerfile" || p == "go.mod" || p == "go.sum" {
		return true
	}
	switch path.Ext(p) {
	case ".md", ".go", ".js", ".css", ".html", ".yaml", ".yml":
		return true
	}
	return false
}

func (r Repository) blob(ctx context.Context, rev, p string) (string, error) {
	// ls-tree avoids following a symlink or a submodule; rev is always a full SHA.
	info, err := r.git(ctx, 1024, "ls-tree", rev, "--", p)
	if err != nil || (!bytes.HasPrefix(info, []byte("100644 blob ")) && !bytes.HasPrefix(info, []byte("100755 blob "))) {
		return "", fmt.Errorf("non-regular file")
	}
	b, err := r.git(ctx, MaxBlobBytes, "show", rev+":"+p)
	if err != nil || !utf8.Valid(b) || bytes.ContainsRune(b, 0) {
		return "", fmt.Errorf("binary, oversized or unavailable file")
	}
	if strings.Contains(string(b[:min(len(b), 512)]), "Code generated") {
		return "", fmt.Errorf("generated file")
	}
	return string(b), nil
}

func (r Repository) Collect(ctx context.Context, base, head string) (*Input, error) {
	if !shaPattern.MatchString(base) || !shaPattern.MatchString(head) {
		return nil, fmt.Errorf("base/head must be full lowercase commit SHAs")
	}
	merge, err := r.git(ctx, 100, "merge-base", base, head)
	if err != nil {
		return nil, err
	}
	in := &Input{Base: base, Head: head, MergeBase: strings.TrimSpace(string(merge)), Files: []File{}, Sources: []Source{}}
	if !shaPattern.MatchString(in.MergeBase) {
		return nil, fmt.Errorf("invalid merge base")
	}
	names, err := r.git(ctx, 2<<20, "diff", "--no-ext-diff", "--no-textconv", "--name-status", "-z", "--find-renames", in.MergeBase, head, "--")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimSuffix(string(names), "\x00"), "\x00")
	used, count := 0, 0
	for n := 0; n < len(parts) && parts[n] != ""; {
		status := parts[n]
		n++
		if n >= len(parts) {
			return nil, fmt.Errorf("malformed file list")
		}
		f := File{Status: status[:1], Path: parts[n]}
		n++
		if f.Status == "R" || f.Status == "C" {
			f.PreviousPath = f.Path
			if n >= len(parts) {
				return nil, fmt.Errorf("malformed rename")
			}
			f.Path = parts[n]
			n++
		}
		if !allowed(f.Path) || (f.PreviousPath != "" && !allowed(f.PreviousPath)) {
			f.Reason = "excluded path or file type"
		} else if count >= MaxFiles {
			f.Reason = "file count limit"
		} else {
			oldPath := f.Path
			if f.PreviousPath != "" {
				oldPath = f.PreviousPath
			}
			if f.Status != "D" {
				_, err = r.blob(ctx, head, f.Path)
			} else {
				_, err = r.blob(ctx, in.MergeBase, oldPath)
			}
			if err != nil {
				f.Reason = "binary, generated, non-regular or oversized file"
			} else {
				paths := []string{f.Path}
				if oldPath != f.Path {
					paths = append(paths, oldPath)
				}
				args := append([]string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=3", "--find-renames", in.MergeBase, head, "--"}, paths...)
				b, e := r.git(ctx, MaxPatchBytes, args...)
				if e != nil || used+len(b) > MaxPatchBytes {
					f.Reason = "patch byte limit"
				} else {
					f.Patch = string(b)
					f.Added, f.Deleted = changedLines(f.Patch)
					used += len(b)
					count++
				}
			}
		}
		in.Files = append(in.Files, f)
	}
	if err = r.retrieve(ctx, in); err != nil {
		return nil, err
	}
	return in, nil
}

func changedLines(patch string) (map[int]bool, map[int]bool) {
	added, deleted := map[int]bool{}, map[int]bool{}
	old, newLine := 0, 0
	active := false
	for _, line := range strings.Split(patch, "\n") {
		if m := hunkPattern.FindStringSubmatch(line); m != nil {
			old, _ = strconv.Atoi(m[1])
			newLine, _ = strconv.Atoi(m[3])
			active = true
			continue
		}
		if !active || line == "" {
			continue
		}
		switch line[0] {
		case '+':
			added[newLine] = true
			newLine++
		case '-':
			deleted[old] = true
			old++
		case ' ':
			old++
			newLine++
		case '\\':
		default:
			active = false
		}
	}
	return added, deleted
}
