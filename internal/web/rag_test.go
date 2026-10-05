package web

import (
	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/docindex"
	"codex-chat-cli/internal/rag"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRAGAPIsGuardMethodsOriginAndUnavailableIndex(t *testing.T) {
	h := NewHandler(&fakeLLM{}, "test-model", nil)
	for _, tc := range []struct {
		method, path, origin, header string
		want                         int
	}{
		{"GET", "/api/rag/compare", "", "1", 405},
		{"POST", "/api/rag/compare", "https://evil.example", "1", 403},
		{"POST", "/api/rag/compare", "", "", 403},
		{"POST", "/api/rag/compare", "", "1", 503},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"query":"q"}`))
		r.Header.Set("X-Codex-Chat", tc.header)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	w := performChatBody(h, nil, `{"message":"question","rag":true}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("missing RAG accepted: %d", w.Code)
	}
}

func TestRAGRunValidationAndConfiguredPipeline(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "guide.md"), []byte("# Guide\nExact project evidence."), 0600); err != nil {
		t.Fatal(err)
	}
	docs, err := docindex.LoadCorpus(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := docindex.Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	emb := &documentTestEmbedder{}
	if _, err = store.Build(context.Background(), docs, emb, docindex.DefaultChunkConfig(), nil); err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{respond: func(r agent.CompletionRequest) string {
		if r.Instructions == rag.RerankPrompt {
			if !r.Internal || len(r.Tools) > 0 || r.PreviousResponseID != "" {
				t.Error("grader is not isolated")
			}
			var input struct {
				Candidates []struct {
					ID string `json:"id"`
				} `json:"candidates"`
			}
			if err := json.Unmarshal([]byte(r.Input), &input); err != nil {
				t.Fatal(err)
			}
			scores := []map[string]any{}
			for _, c := range input.Candidates {
				scores = append(scores, map[string]any{"id": c.ID, "score": 1, "reason": "Only related topic", "evidenceLine": 0})
			}
			b, _ := json.Marshal(map[string]any{"scores": scores})
			return string(b)
		}
		t.Error("empty selection must not invoke answering model")
		return "unexpected"
	}}
	h := NewHandlerWithDocuments(llm, "test", nil, nil, nil, &docindex.Service{Store: store, Embedder: emb})
	for _, tc := range []struct {
		method, body, origin, header string
		want                         int
	}{
		{"GET", `{}`, "", "1", 405},
		{"POST", `{"query":"q"}`, "https://evil.example", "1", 403},
		{"POST", `{"query":"q"}`, "", "", 403},
		{"POST", `{"query":"q","model":"unknown"}`, "", "1", 400},
		{"POST", `{"query":"q","options":{"mode":"full","topKBefore":1,"topKAfter":2,"relevanceThreshold":2}}`, "", "1", 400},
		{"POST", `{"query":"q","options":{"mode":"filter","topKBefore":20,"topKAfter":5,"relevanceThreshold":4}}`, "", "1", 400},
		{"POST", `{"query":"q","path":".env"}`, "", "1", 400},
		{"POST", `{"query":"q"} {}`, "", "1", 400},
		{"POST", `{"query":"q","options":{"mode":"filter","topKBefore":1,"topKAfter":1,"relevanceThreshold":2}}`, "", "1", 200},
	} {
		before := emb.calls
		req := httptest.NewRequest(tc.method, "/api/rag/run", strings.NewReader(tc.body))
		req.Header.Set("X-Codex-Chat", tc.header)
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body.String())
		}
		if tc.want != 200 && emb.calls != before {
			t.Fatal("invalid request caused embedding call")
		}
		if tc.want == 200 {
			var v rag.ModeResult
			if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			if v.Mode != "filter" || v.Answer.Text != agent.NoRAGEvidence || !v.Answer.Retrieval.Empty || v.Answer.Retrieval.Options.TopKBefore != 1 || len(v.Answer.Retrieval.Candidates) != 1 || len(v.Answer.Retrieval.Sources) != 0 {
				t.Fatalf("options/empty behavior lost: %+v", v)
			}
		}
	}
	if llm.calls != 1 {
		t.Fatalf("unexpected LLM calls: %d", llm.calls)
	}
}

func TestGroundedRAGRunAndReport(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "guide.md"), []byte("# Guide\nTTL is 12 hours."), 0600)
	docs, err := docindex.LoadCorpus(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := docindex.Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	emb := &documentTestEmbedder{}
	if _, err = store.Build(context.Background(), docs, emb, docindex.DefaultChunkConfig(), nil); err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{respond: func(r agent.CompletionRequest) string {
		switch r.Instructions {
		case rag.RerankPrompt:
			var input struct {
				Candidates []struct {
					ID string `json:"id"`
				} `json:"candidates"`
			}
			json.Unmarshal([]byte(r.Input), &input)
			scores := []map[string]any{}
			for _, c := range input.Candidates {
				scores = append(scores, map[string]any{"id": c.ID, "score": 3, "reason": "direct evidence", "evidenceLine": 2})
			}
			b, _ := json.Marshal(map[string]any{"scores": scores})
			return string(b)
		case agent.GroundingPrompt:
			return `{"status":"answered","claims":[{"text":"TTL — 12 часов.","evidence":[{"ref":"S1","start":2,"end":2}]}]}`
		case agent.GroundingCheckPrompt:
			return `{"claims":[{"index":0,"verdict":"supported","reason":"Указано явно"}],"complete":true}`
		default:
			t.Errorf("unexpected instruction %s", r.Instructions)
			return ""
		}
	}}
	h := NewHandlerWithDocuments(llm, "test", nil, nil, nil, &docindex.Service{Store: store, Embedder: emb})
	for _, mode := range []string{"baseline", "filter"} {
		req := httptest.NewRequest("POST", "/api/rag/run", strings.NewReader(`{"query":"TTL?","options":{"mode":"`+mode+`","topKBefore":20,"topKAfter":5,"relevanceThreshold":2,"grounded":true}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Codex-Chat", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if mode == "baseline" {
			if w.Code != 400 || llm.calls != 0 {
				t.Fatal("invalid mode performed calls")
			}
			continue
		}
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result rag.ModeResult
		if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Answer.Retrieval.Grounding == nil || result.Answer.Retrieval.Grounding.Status != "answered" || !strings.Contains(result.Answer.Text, "TTL is 12 hours.") {
			t.Fatal(w.Body.String())
		}
	}
	if llm.calls != 3 {
		t.Fatalf("wanted reranker + answer + check, got %d", llm.calls)
	}
	report := filepath.Join(t.TempDir(), "report.json")
	os.WriteFile(report, []byte(`{"version":1,"complete":true,"pairs":[]}`), 0600)
	t.Setenv("RAG_GROUNDING_PATH", report)
	req := httptest.NewRequest("GET", "/api/rag/grounding", nil)
	req.Header.Set("X-Codex-Chat", "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"complete":true`) {
		t.Fatal(w.Body.String())
	}
}

func TestDialogueAPIRequiresPersistentChat(t *testing.T) {
	h := NewHandler(&fakeLLM{}, "test-model", nil)
	w := performChatBody(h, nil, `{"message":"q","rag":false,"ragOptions":{"mode":"filter","topKBefore":20,"topKAfter":5,"relevanceThreshold":2,"grounded":true,"dialogue":true}}`)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	report := filepath.Join(t.TempDir(), "dialogues.json")
	os.WriteFile(report, []byte(`{"version":1,"complete":true,"runs":[]}`), 0600)
	t.Setenv("RAG_DIALOGUE_PATH", report)
	for _, tc := range []struct {
		method, header string
		want           int
	}{{"GET", "1", 200}, {"POST", "1", 405}, {"GET", "", 403}} {
		req := httptest.NewRequest(tc.method, "/api/rag/dialogues", nil)
		req.Header.Set("X-Codex-Chat", tc.header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
