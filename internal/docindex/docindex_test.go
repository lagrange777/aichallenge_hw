package docindex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func document(t *testing.T, source, text string) Document {
	t.Helper()
	ext := filepath.Ext(source)
	if strings.HasSuffix(source, ".go.txt") {
		ext = ".go"
	}
	s, err := sections(text, ext)
	if err != nil {
		t.Fatal(err)
	}
	return Document{Source: source, Title: source, Text: text, Hash: digest(text), Words: len(strings.Fields(text)), Characters: utf8.RuneCountInString(text), Sections: s}
}
func TestChunkUnicodeCoverageAndStableIDs(t *testing.T) {
	d := document(t, "guide.md", "# Руководство\n\n"+strings.Repeat("Текст 🦊 на русском. ", 40)+"\n\n## Второй раздел\n\n"+strings.Repeat("Пример\n\n", 35))
	for _, strategy := range []string{Fixed, Structured} {
		chunks, err := Split(d, strategy, ChunkConfig{100, 15})
		if err != nil {
			t.Fatal(err)
		}
		covered := make([]bool, d.Characters)
		seen := map[string]bool{}
		for _, c := range chunks {
			if !utf8.ValidString(c.Text) || c.End-c.Start > 100 || c.ID == "" || c.Section == "" || seen[c.ID] {
				t.Fatalf("bad chunk %+v", c)
			}
			seen[c.ID] = true
			if c.Text != string([]rune(d.Text)[c.Start:c.End]) {
				t.Fatal("offset mismatch")
			}
			for i := c.Start; i < c.End; i++ {
				covered[i] = true
			}
			if strategy == Structured {
				for _, s := range d.Sections {
					if s.Start > c.Start && s.Start < c.End {
						t.Fatal("crossed section")
					}
				}
			}
		}
		for i, ok := range covered {
			if !ok {
				t.Fatalf("lost character at %d", i)
			}
		}
		again, _ := Split(d, strategy, ChunkConfig{100, 15})
		if len(again) != len(chunks) {
			t.Fatal("unstable split")
		}
		for i := range chunks {
			if chunks[i].ID != again[i].ID {
				t.Fatal("unstable ID")
			}
		}
	}
	for _, cfg := range []ChunkConfig{{0, 0}, {100, 100}, {100, -1}, {2001, 0}} {
		if _, err := Split(d, Fixed, cfg); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
func TestStructureUnderstandsFencesHierarchyAndGo(t *testing.T) {
	d := document(t, "a.md", "# A\ntext\n```md\n# fake\n````\n## B\nbody\n### C\nmore\n")
	if len(d.Sections) != 3 || d.Sections[2].Title != "A > B > C" {
		t.Fatalf("sections: %+v", d.Sections)
	}
	d = document(t, "a.go.txt", "package demo\n\n// Thing docs\ntype Thing struct{}\n\n// Run docs\nfunc (t *Thing) Run() {}\n")
	if len(d.Sections) != 3 || d.Sections[2].Title != "func Thing.Run" || !strings.HasPrefix(string([]rune(d.Text)[d.Sections[2].Start:]), "// Run docs") {
		t.Fatalf("Go sections: %+v", d.Sections)
	}
}
func TestCorpusRejectsLinksAndInvalidEncoding(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.md"), []byte("# Тест\r\nтекст"), 0600)
	docs, err := LoadCorpus(root)
	if err != nil || len(docs) != 1 || strings.Contains(docs[0].Text, "\r") {
		t.Fatalf("%+v %v", docs, err)
	}
	os.Symlink("a.md", filepath.Join(root, "link.md"))
	if _, err = LoadCorpus(root); err == nil {
		t.Fatal("symlink accepted")
	}
	os.Remove(filepath.Join(root, "link.md"))
	os.WriteFile(filepath.Join(root, "b.txt"), []byte{0xff}, 0600)
	if _, err = LoadCorpus(root); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

type fakeEmbedder struct {
	calls int
	fail  bool
	model string
}

func (e *fakeEmbedder) Model() string {
	if e.model != "" {
		return e.model
	}
	return "test-embedding"
}
func (e *fakeEmbedder) Dimensions() int { return 2 }
func (e *fakeEmbedder) Embed(_ context.Context, texts []string) (EmbeddingBatch, error) {
	e.calls++
	if e.fail {
		return EmbeddingBatch{}, fmt.Errorf("injected embedding failure")
	}
	b := EmbeddingBatch{Tokens: len(texts)}
	for range texts {
		b.Vectors = append(b.Vectors, []float32{1, 0})
	}
	return b, nil
}
func TestBuildCacheRollbackRestartAndSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e := &fakeEmbedder{}
	docs := []Document{document(t, "a.md", "# A\n\nalpha evidence\n\n## B\n\nbeta evidence")}
	ctx := context.Background()
	first, err := s.Build(ctx, docs, e, ChunkConfig{100, 10}, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := e.calls
	second, err := s.Build(ctx, docs, e, ChunkConfig{100, 10}, nil)
	if err != nil || e.calls != calls || second.CorpusHash != first.CorpusHash {
		t.Fatalf("cache not reused: %v", err)
	}
	e.fail = true
	changed := []Document{document(t, "a.md", "# Changed\nNew evidence.")}
	if _, err = s.Build(ctx, changed, e, ChunkConfig{100, 10}, nil); err == nil {
		t.Fatal("failure ignored")
	}
	old, err := s.Info()
	if err != nil || old.CorpusHash != first.CorpusHash {
		t.Fatal("failed build destroyed prior index")
	}
	s.Close()
	s, err = OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	matches, err := s.Search(ctx, Fixed, []float32{1, 0}, 5)
	if err != nil || len(matches) != 1 || matches[0].Score < 0.999 {
		t.Fatalf("search after restart: %+v %v", matches, err)
	}
	if _, err = s.Search(ctx, Fixed, []float32{1}, 5); err == nil {
		t.Fatal("wrong dimensions accepted")
	}
	if _, err = (&Service{s, &fakeEmbedder{model: "other"}}).Search(ctx, "query", 5); err == nil {
		t.Fatal("different embedding model accepted")
	}
}
func TestEvaluationUsesSourceEvidenceAndSameQueryVector(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := &fakeEmbedder{}
	ctx := context.Background()
	docs := []Document{document(t, "a.md", "# A\nalpha evidence\n## B\nbeta evidence")}
	if _, err = s.Build(ctx, docs, e, ChunkConfig{100, 10}, nil); err != nil {
		t.Fatal(err)
	}
	q := []Question{{ID: "q1", Query: "Which evidence?", Relevant: []Evidence{{Source: "a.md", Quote: "alpha evidence"}, {Source: "a.md", Quote: "beta evidence"}}}}
	calls := e.calls
	r, err := s.Evaluate(ctx, docs, q, e, 1)
	if err != nil {
		t.Fatal(err)
	}
	if e.calls != calls+1 {
		t.Fatal("query embedded separately per strategy")
	}
	if r.Evaluations[0].Recall != 1 || r.Evaluations[1].Recall != 0.5 || r.Evaluations[1].HitRate != 1 || r.Evaluations[1].MRR != 1 {
		t.Fatalf("wrong metrics: %+v", r.Evaluations)
	}
	if _, err = s.Report(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Markdown(), "50.0%") {
		t.Fatal("missing report metrics")
	}
	q[0].Relevant[0].Quote = "not in corpus"
	if _, err = s.Evaluate(ctx, docs, q, e, 1); err == nil {
		t.Fatal("invalid gold accepted")
	}
	docs[0].Hash = "changed"
	if _, err = s.Evaluate(ctx, docs, q, e, 1); err == nil {
		t.Fatal("stale corpus accepted")
	}
}
func TestOpenAIEmbeddingWireOrderAndValidation(t *testing.T) {
	for _, mode := range []string{"ok", "duplicate", "dimension", "zero", "model", "http-error"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer test-secret" || r.Method != "POST" {
					t.Error("wrong request")
				}
				var input struct {
					Model      string   `json:"model"`
					Dimensions int      `json:"dimensions"`
					Input      []string `json:"input"`
					Format     string   `json:"encoding_format"`
				}
				if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Input) != 2 || input.Dimensions != 2 || input.Format != "float" {
					t.Error("wrong payload")
				}
				if mode == "http-error" {
					w.WriteHeader(401)
					fmt.Fprint(w, "test-secret")
					return
				}
				v := []float32{0, 2}
				index := 0
				model := "test-model"
				switch mode {
				case "duplicate":
					index = 1
				case "dimension":
					v = []float32{1}
				case "zero":
					v = []float32{0, 0}
				case "model":
					model = "other"
				}
				json.NewEncoder(w).Encode(map[string]any{"model": model, "data": []any{map[string]any{"index": 1, "embedding": []float32{1, 0}}, map[string]any{"index": index, "embedding": v}}, "usage": map[string]int{"total_tokens": 4}})
			}))
			defer server.Close()
			e, err := NewOpenAIEmbedder("test-secret", server.URL+"/v1", "test-model", 2)
			if err != nil {
				t.Fatal(err)
			}
			b, err := e.Embed(context.Background(), []string{"one", "two"})
			if mode == "ok" {
				if err != nil || b.Vectors[0][1] != 1 || b.Vectors[1][0] != 1 || b.Tokens != 4 {
					t.Fatalf("%+v %v", b, err)
				}
			} else {
				if err == nil {
					t.Fatal("bad response accepted")
				}
				if strings.Contains(err.Error(), "test-secret") {
					t.Fatal("secret leaked")
				}
			}
		})
	}
}
func TestOpenAIEmbeddingRetriesAndCancellation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, `{"model":"test","data":[{"index":0,"embedding":[1,0]}],"usage":{"total_tokens":1}}`)
	}))
	defer server.Close()
	e, _ := NewOpenAIEmbedder("key", server.URL, "test", 2)
	if _, err := e.Embed(context.Background(), []string{"one"}); err != nil || calls != 2 {
		t.Fatalf("retry: %v, %d", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Embed(ctx, []string{"one"}); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestBundledCorpusAndGoldEvidence(t *testing.T) {
	docs, err := LoadCorpus("../../documents/corpus")
	if err != nil {
		t.Fatal(err)
	}
	bySource := map[string]string{}
	words := 0
	for _, d := range docs {
		bySource[d.Source] = d.Text
		words += d.Words
	}
	if words < 12000 {
		t.Fatalf("corpus below 30 pages at 400 words: %d", words)
	}
	qs, err := ReadQuestions("../../documents/questions.json")
	if err != nil || len(qs) != 20 {
		t.Fatalf("questions: %d %v", len(qs), err)
	}
	for _, q := range qs {
		for _, a := range q.Relevant {
			if strings.Count(bySource[a.Source], a.Quote) != 1 {
				t.Fatalf("invalid evidence: %s", q.ID)
			}
		}
	}
	if _, err = Preview(docs, DefaultChunkConfig()); err != nil {
		t.Fatal(err)
	}
}
