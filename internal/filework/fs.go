// Package filework implements goal-driven file tasks in an isolated workspace.
package filework

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const maxFile = 96 << 10

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Hash    string `json:"hash"`
	Exists  bool   `json:"exists"`
}
type Patch struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
	Hash   string `json:"hash"`
	Exists bool   `json:"exists"`
	Diff   string `json:"diff"`
}

func public(name string) bool {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\\x00\r\n\t") {
		return false
	}
	for _, p := range strings.Split(name, "/") {
		if strings.HasPrefix(p, ".") {
			return false
		}
		switch p {
		case "data", "artifacts", "tools", "documents", "node_modules", "vendor", "bin", "profiles", "memory":
			return false
		}
	}
	switch path.Ext(name) {
	case ".go", ".md", ".yaml", ".yml", ".html", ".js", ".css":
		return true
	}
	return name == "go.mod" || name == "Dockerfile"
}

// Every path component is checked; os.Root additionally prevents escape during
// filesystem operations, including symlink races. No shell commands are run.
func checked(root *os.Root, name string, missing bool) error {
	if !public(name) {
		return errors.New("path is outside the public source/documentation allowlist")
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if errors.Is(err, fs.ErrNotExist) && missing {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlinks are forbidden")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return errors.New("parent is not a directory")
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return errors.New("not a regular file")
		}
	}
	return nil
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func read(root *os.Root, name string) (File, error) {
	f := File{Path: name}
	if err := checked(root, name, true); err != nil {
		return f, err
	}
	h, err := root.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	defer h.Close()
	i, err := h.Stat()
	if err != nil || !i.Mode().IsRegular() || i.Size() > maxFile {
		return f, errors.New("file must be regular and at most 96 KiB")
	}
	b, err := io.ReadAll(io.LimitReader(h, maxFile+1))
	if err != nil || len(b) > maxFile || !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return f, errors.New("file must be bounded UTF-8 text")
	}
	f.Exists, f.Content, f.Hash = true, string(b), digest(string(b))
	return f, nil
}
func list(root *os.Root) ([]string, error) {
	var result []string
	err := fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() && name != "." && !public(name+"/x.md") {
			return fs.SkipDir
		}
		if !d.IsDir() && d.Type().IsRegular() && public(name) {
			result = append(result, name)
			if len(result) > 1500 {
				return errors.New("workspace exceeds 1500 public files")
			}
		}
		return nil
	})
	sort.Strings(result)
	return result, err
}

// Whole-file unified hunks are valid git-apply patches, with exact original bytes.
func unified(p Patch) string {
	if p.Before == p.After && p.Exists {
		return ""
	}
	old := "a/" + p.Path
	if !p.Exists {
		old = "/dev/null"
	}
	lines := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	}
	a, b := lines(p.Before), lines(p.After)
	start := func(n int) int {
		if n == 0 {
			return 0
		}
		return 1
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n", old, p.Path, start(len(a)), len(a), start(len(b)), len(b))
	for _, side := range []struct {
		lines       []string
		prefix, raw string
	}{{a, "-", p.Before}, {b, "+", p.After}} {
		for i, line := range side.lines {
			fmt.Fprintf(&out, "%s%s\n", side.prefix, line)
			if i == len(side.lines)-1 && !strings.HasSuffix(side.raw, "\n") {
				out.WriteString("\\ No newline at end of file\n")
			}
		}
	}
	return out.String()
}
