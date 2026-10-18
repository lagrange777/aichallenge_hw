package prreview

import (
	"context"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strings"

	"codex-chat-cli/internal/docindex"
	"codex-chat-cli/internal/projecthelp"
)

type Source struct {
	Ref      string  `json:"ref"`
	Kind     string  `json:"kind"`
	Path     string  `json:"path"`
	Section  string  `json:"section"`
	Revision string  `json:"revision"`
	ChunkID  string  `json:"chunkId"`
	Start    int     `json:"start"`
	End      int     `json:"end"`
	Text     string  `json:"text"`
	Score    float64 `json:"score,omitempty"`
}

// chunks preserves physical line numbers, including CRLF and Unicode. Go AST
// declarations / Markdown headings are boundaries; long sections split at 40 lines.
func chunks(p, rev, text string) []Source {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	labels := map[int]string{1: path.Base(p)}
	if strings.HasSuffix(p, ".go") {
		fset := token.NewFileSet()
		f, _ := parser.ParseFile(fset, p, text, 0)
		if f != nil {
			for _, d := range f.Decls {
				name := "declaration"
				switch v := d.(type) {
				case *ast.FuncDecl:
					name = v.Name.Name
				case *ast.GenDecl:
					name = v.Tok.String()
				}
				labels[fset.Position(d.Pos()).Line] = name
			}
		}
	} else if strings.HasSuffix(p, ".md") {
		for i, l := range lines {
			if strings.HasPrefix(l, "#") {
				labels[i+1] = strings.TrimSpace(strings.TrimLeft(l, "#"))
			}
		}
	}
	starts := []int{}
	for n := range labels {
		starts = append(starts, n)
	}
	sort.Ints(starts)
	starts = append(starts, len(lines)+1)
	var out []Source
	for i := 0; i < len(starts)-1; i++ {
		for a := starts[i]; a < starts[i+1]; a += 40 {
			b := min(a+39, starts[i+1]-1)
			t := strings.Join(lines[a-1:b], "\n")
			if len(t) > 10000 || strings.TrimSpace(t) == "" {
				continue
			}
			id := fmt.Sprintf("%x", sha256.Sum256([]byte(rev+":"+p+fmt.Sprint(a, b)+t)))
			out = append(out, Source{Kind: "code", Path: p, Revision: rev, ChunkID: id[:16], Section: labels[starts[i]], Start: a, End: b, Text: t})
			if strings.HasSuffix(p, ".md") {
				out[len(out)-1].Kind = "documentation"
			}
		}
	}
	return out
}

func (r Repository) retrieve(ctx context.Context, in *Input) error {
	// Every changed diff is explicit evidence. Its line numbers refer to the
	// patch, whereas retrieved documentation/code uses physical source lines.
	for i, f := range in.Files {
		if f.Reason == "" {
			in.Sources = append(in.Sources, Source{Ref: fmt.Sprintf("D%d", i+1), Kind: "diff", Path: f.Path, Section: "PR patch (patch line numbers)", Revision: in.Head, ChunkID: fmt.Sprintf("diff-%d", i+1), Start: 1, End: len(strings.Split(f.Patch, "\n")), Text: f.Patch})
		}
	}
	if len(in.Sources) == 0 {
		return nil
	}
	listing, err := r.git(ctx, 2<<20, "ls-tree", "-r", "--name-only", "-z", in.Base)
	if err != nil {
		return err
	}
	docs, code := &projecthelp.Snapshot{}, &projecthelp.Snapshot{}
	lookup := map[string]Source{}
	total := 0
	for _, p := range strings.Split(string(listing), "\x00") {
		if !allowed(p) {
			continue
		}
		if in.CorpusFiles >= 1000 || total >= 12<<20 {
			in.CorpusSkipped++
			continue
		}
		t, e := r.blob(ctx, in.Base, p)
		if e != nil {
			in.CorpusSkipped++
			continue
		}
		total += len(t)
		in.CorpusFiles++
		for _, s := range chunks(p, in.Base, t) {
			lookup[s.ChunkID] = s
			c := docindex.Chunk{ID: s.ChunkID, Source: s.Path, Title: path.Base(s.Path), Section: s.Section, Text: s.Text}
			if s.Kind == "documentation" {
				docs.Chunks = append(docs.Chunks, c)
			} else {
				code.Chunks = append(code.Chunks, c)
			}
			in.CorpusChunks++
		}
	}
	selected := map[string]bool{}
	budget := 0
	for _, f := range in.Files {
		if f.Reason != "" {
			continue
		}
		// Per-file retrieval prevents a large, unrelated patch dominating BM25.
		query := f.Path + " " + f.Patch[:min(len(f.Patch), 3500)]
		for _, index := range []*projecthelp.Snapshot{docs, code} {
			for _, hit := range projecthelp.Search(index, query) {
				s := lookup[hit.ChunkID]
				if selected[s.ChunkID] || budget+len(s.Text) > 24000 {
					continue
				}
				selected[s.ChunkID] = true
				s.Ref = fmt.Sprintf("R%d", len(selected))
				s.Score = hit.Score
				in.Sources = append(in.Sources, s)
				budget += len(s.Text)
				if len(selected) >= 16 {
					return nil
				}
			}
		}
	}
	return nil
}
