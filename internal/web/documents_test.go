package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-chat-cli/internal/docindex"
)

type documentTestEmbedder struct{ calls int }

func (e *documentTestEmbedder) Model() string   { return "test" }
func (e *documentTestEmbedder) Dimensions() int { return 2 }
func (e *documentTestEmbedder) Embed(_ context.Context, text []string) (docindex.EmbeddingBatch, error) {
	e.calls++
	b := docindex.EmbeddingBatch{}
	for range text {
		b.Vectors = append(b.Vectors, []float32{1, 0})
	}
	return b, nil
}
func TestDocumentsAPIReadSearchAndBoundaries(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "guide.md"), []byte("# Guide\nTest document."), 0600)
	docs, err := docindex.LoadCorpus(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := docindex.Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := &documentTestEmbedder{}
	if _, err = store.Build(context.Background(), docs, e, docindex.DefaultChunkConfig(), nil); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithDocuments(nil, "test", nil, nil, nil, &docindex.Service{Store: store, Embedder: e})
	for _, tc := range []struct {
		method, path, body string
		auth               bool
		origin             string
		status             int
	}{
		{"GET", "/api/documents", "", false, "", 403},
		{"GET", "/api/documents", "", true, "https://evil.example", 403},
		{"GET", "/api/documents", "", true, "", 200},
		{"GET", "/api/documents/chunks?strategy=fixed", "", true, "", 200},
		{"GET", "/api/documents/chunks?strategy=wrong", "", true, "", 400},
		{"GET", "/api/documents/chunks?offset=-1", "", true, "", 400},
		{"POST", "/api/documents/search", `{"query":"evidence"}`, true, "", 200},
		{"POST", "/api/documents/search", `{"query":"evidence"} {}`, true, "", 400},
		{"POST", "/api/documents/search", `{"query":""}`, true, "", 400},
		{"POST", "/api/documents/search", `{"query":"evidence","path":".env"}`, true, "", 400},
		{"GET", "/api/documents/search", "", true, "", 405},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.auth {
			req.Header.Set("X-Codex-Chat", "1")
		}
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		req.Header.Set("Content-Type", "application/json")
		before := e.calls
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("missing security headers")
		}
		if tc.method != "POST" || tc.status != 200 {
			if e.calls != before {
				t.Fatal("unexpected paid call")
			}
		}
		if strings.Contains(w.Body.String(), `"vector"`) {
			t.Fatal("vectors exposed in UI payload")
		}
	}
	missing := NewHandler(nil, "test", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/documents", nil)
	req.Header.Set("X-Codex-Chat", "1")
	w := httptest.NewRecorder()
	missing.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatal("missing index should be explicit")
	}
}
