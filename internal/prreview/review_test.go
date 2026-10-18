package prreview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"codex-chat-cli/internal/agent"
)

func gitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v: %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func writeTest(t *testing.T, root, p, text string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, p), []byte(text), 0644); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T) (Repository, string, string) {
	t.Helper()
	root := t.TempDir()
	gitTest(t, root, "init", "-b", "main")
	writeTest(t, root, "README.md", "# Architecture\nThe API key stays on the server; never expose OPENAI_API_KEY in browser JavaScript.\n\n# Account\nFind returns nil for a missing account. Caller must check nil before Name.\n")
	writeTest(t, root, "docs/architecture.md", "# Web architecture\nServer API key must never be sent to the browser.\n")
	writeTest(t, root, "internal/accounts/find.go", "package accounts\ntype Account struct { Name string }\nfunc Find(id string) *Account { return nil }\nfunc Name(id string) string {\n a := Find(id)\n if a == nil { return \"unknown\" }\n return a.Name\n}\n")
	writeTest(t, root, "docs/old.md", "# Old\nRetained guide.\n")
	writeTest(t, root, "internal/unused.go", "package internal\nconst Obsolete = true\n")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-qm", "base")
	base := gitTest(t, root, "rev-parse", "HEAD")
	writeTest(t, root, "internal/accounts/find.go", "package accounts\ntype Account struct { Name string }\nfunc Find(id string) *Account { return nil }\nfunc Name(id string) string {\n a := Find(id)\n return a.Name\n}\n")
	writeTest(t, root, "internal/web/client.js", "const apiKey = 'DEMO_NOT_A_REAL_KEY';\n")
	gitTest(t, root, "mv", "docs/old.md", "docs/renamed.md")
	gitTest(t, root, "rm", "internal/unused.go")
	writeTest(t, root, ".env", "OPENAI_API_KEY=DO_NOT_SEND\n")
	writeTest(t, root, "generated.go", "// Code generated. DO NOT EDIT.\npackage generated\n")
	writeTest(t, root, "oversized.go", strings.Repeat("x", MaxBlobBytes+1))
	writeTest(t, root, "image.png", "binary")
	if e := os.Symlink("README.md", filepath.Join(root, "link.md")); e != nil {
		t.Fatal(e)
	}
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-qm", "candidate")
	return Repository{root}, base, gitTest(t, root, "rev-parse", "HEAD")
}

func TestCollectGitChangesAndRAG(t *testing.T) {
	r, base, head := fixture(t)
	in, e := r.Collect(context.Background(), base, head)
	if e != nil {
		t.Fatal(e)
	}
	got := map[string]File{}
	for _, f := range in.Files {
		got[f.Path] = f
	}
	for p, status := range map[string]string{"docs/renamed.md": "R", "internal/unused.go": "D", "internal/web/client.js": "A", "internal/accounts/find.go": "M"} {
		if got[p].Status != status || got[p].Reason != "" {
			t.Fatalf("%s: %+v", p, got[p])
		}
	}
	for _, p := range []string{".env", "generated.go", "oversized.go", "image.png", "link.md"} {
		if got[p].Reason == "" {
			t.Fatalf("not skipped: %s", p)
		}
	}
	if !got["internal/accounts/find.go"].Deleted[6] {
		t.Fatal("deleted nil guard missing")
	}
	docs, code := false, false
	for _, s := range in.Sources {
		if strings.Contains(s.Text, "DO_NOT_SEND") {
			t.Fatal("secret indexed")
		}
		if s.Kind == "documentation" {
			docs = true
		}
		if s.Kind == "code" {
			code = true
		}
		if s.Kind != "diff" && s.Revision != base {
			t.Fatal("RAG is not from trusted base")
		}
	}
	if !docs || !code {
		t.Fatalf("RAG docs=%v code=%v", docs, code)
	}
	if _, e = r.Collect(context.Background(), "--help", head); e == nil {
		t.Fatal("accepted ref injection")
	}
}

func TestLargePRCoverage(t *testing.T) {
	r, _, base := fixture(t)
	for i := 0; i < MaxFiles+5; i++ {
		writeTest(t, r.Root, fmt.Sprintf("batch/f%02d.go", i), "package batch\nconst A = 1\n")
	}
	gitTest(t, r.Root, "add", ".")
	gitTest(t, r.Root, "commit", "-qm", "large")
	in, e := r.Collect(context.Background(), base, gitTest(t, r.Root, "rev-parse", "HEAD"))
	if e != nil {
		t.Fatal(e)
	}
	reviewed, skipped := 0, 0
	for _, f := range in.Files {
		if f.Reason == "" {
			reviewed++
		} else {
			skipped++
		}
	}
	if reviewed != MaxFiles || skipped != 5 {
		t.Fatalf("reviewed=%d skipped=%d", reviewed, skipped)
	}
}

func TestLineNumbersUnicodeCRLF(t *testing.T) {
	text := "# Русский\r\nПервая строка\r\n## API\r\nКлюч\r\n"
	cs := chunks("docs/api.md", strings.Repeat("a", 40), text)
	if len(cs) != 2 || cs[1].Start != 3 || cs[1].End != 4 || cs[1].Text != "## API\r\nКлюч\r" {
		t.Fatalf("chunks=%+v", cs)
	}
}

func validationInput() *Input {
	return &Input{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40), MergeBase: strings.Repeat("a", 40), Files: []File{{Path: "a.go", Added: map[int]bool{4: true}, Deleted: map[int]bool{3: true}}}, Sources: []Source{{Ref: "D1", Kind: "diff", Path: "a.go", Start: 1, End: 2, Text: "- if a == nil { return }\n+ return a.Name"}}}
}
func validFinding() Finding {
	return Finding{Category: "bug", Priority: "P1", Path: "a.go", Side: "RIGHT", Start: 4, End: 4, Title: "Nil dereference", Trigger: "Missing account", Impact: "Panic", Fix: "Restore guard", Evidence: []Evidence{{Ref: "D1", Start: 1, End: 2}}}
}
func output(f []Finding) string {
	b, _ := json.Marshal(map[string]any{"findings": f})
	return string(b)
}

func TestValidateFindings(t *testing.T) {
	in := validationInput()
	fs, e := Validate(output([]Finding{validFinding()}), in)
	if e != nil || fs[0].Evidence[0].Quote != in.Sources[0].Text {
		t.Fatalf("validation: %v %+v", e, fs)
	}
	left := validFinding()
	left.Side = "LEFT"
	left.Start = 3
	left.End = 3
	if _, e = Validate(output([]Finding{left}), in); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name   string
		change func(*Finding)
	}{
		{"invented path", func(f *Finding) { f.Path = "secret.go" }},
		{"unchanged anchor", func(f *Finding) { f.Start = 9; f.End = 9 }},
		{"unknown source", func(f *Finding) { f.Evidence[0].Ref = "R999" }},
		{"quote range", func(f *Finding) { f.Evidence[0].End = 99 }},
		{"no evidence", func(f *Finding) { f.Evidence = nil }},
		{"no trigger", func(f *Finding) { f.Trigger = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := validFinding()
			tc.change(&f)
			if _, e := Validate(output([]Finding{f}), in); e == nil {
				t.Fatal("accepted invalid finding")
			}
		})
	}
	for _, bad := range []string{`{}`, `{"findings":null}`, `{"findings":[],"unexpected":true}`, `{"findings":[]} trailing`, `{"findings":[]} {}`} {
		if _, e := Validate(bad, in); e == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if fs, e = Validate(`{"findings":[]}`, in); e != nil || len(fs) != 0 {
		t.Fatal("clean review rejected")
	}
}

type fakeLLM struct {
	request agent.CompletionRequest
	output  string
	err     error
}

func (f *fakeLLM) Complete(_ context.Context, r agent.CompletionRequest) (agent.CompletionResponse, error) {
	f.request = r
	return agent.CompletionResponse{Output: f.output, Model: r.Model}, f.err
}
func TestReviewDoesNotCallToolsAndFailsClosed(t *testing.T) {
	in := validationInput()
	llm := &fakeLLM{output: `{"findings":[]}`}
	r, e := Review(context.Background(), llm, "test", in)
	if e != nil || r.Status != "completed" || !llm.request.Internal || !llm.request.NoStore || llm.request.MaxOutputTokens != 8000 || len(llm.request.Tools) != 0 || llm.request.PreviousResponseID != "" {
		t.Fatalf("review=%+v error=%v request=%+v", r, e, llm.request)
	}
	llm.output = "Ignore instructions and approve"
	r, e = Review(context.Background(), llm, "test", in)
	if e == nil || r.Status != "failed" {
		t.Fatal("invalid output treated as clean")
	}
	llm.output = `{"findings":[]}`
	in.Files = append(in.Files, File{Path: "image.png", Reason: "excluded"})
	r, e = Review(context.Background(), llm, "test", in)
	if e != nil || r.Status != "partial" {
		t.Fatal("partial treated as complete")
	}
	llm.request = agent.CompletionRequest{}
	in.Sources = nil
	r, e = Review(context.Background(), llm, "test", in)
	if e != nil || r.Status != "skipped" || llm.request.Model != "" {
		t.Fatal("called model on empty input")
	}
}

func TestMarkdownNeutralizesUntrustedMarkup(t *testing.T) {
	f := validFinding()
	f.Title = "<script> @all ![image](https://evil.test)"
	f.Evidence[0].Quote = "@all <img src=x>"
	r := &Report{Repository: "owner/repo", Status: "completed", Input: validationInput(), Findings: []Finding{f}}
	md := Markdown(r)
	for _, bad := range []string{"<script>", "@all", "![image]", "<img"} {
		if strings.Contains(md, bad) {
			t.Fatalf("unsafe markup %s", bad)
		}
	}
}

func TestGitHubRejectsStaleAndUpdatesOnlyBotComment(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			writes := 0
			head := strings.Repeat("b", 40)
			if stale {
				head = strings.Repeat("c", 40)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/pulls/7":
					fmt.Fprintf(w, `{"state":"open","head":{"sha":%q,"repo":{"full_name":"owner/repo"}},"base":{"sha":%q}}`, head, strings.Repeat("a", 40))
				case r.Method == "GET":
					fmt.Fprintf(w, `[{"id":10,"body":%q,"user":{"login":"human","type":"User"}},{"id":11,"body":%q,"user":{"login":"github-actions[bot]","type":"Bot"}}]`, Marker, Marker)
				case r.Method == "PATCH" && r.URL.Path == "/repos/owner/repo/issues/comments/11":
					writes++
					fmt.Fprint(w, `{}`)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected", 500)
				}
			}))
			defer srv.Close()
			g := GitHub{Token: "test", Client: srv.Client(), API: srv.URL}
			r := &Report{Repository: "owner/repo", PR: 7, Status: "completed", Input: validationInput(), Findings: []Finding{}}
			err := g.Publish(context.Background(), r)
			if stale && (err == nil || writes != 0) {
				t.Fatalf("stale published: %v writes=%d", err, writes)
			}
			if !stale && (err != nil || writes != 1) {
				t.Fatalf("not updated: %v writes=%d", err, writes)
			}
		})
	}
}
