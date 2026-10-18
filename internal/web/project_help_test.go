package web

import (
	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/history"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type helpWebRetriever struct{}

func (helpWebRetriever) Retrieve(context.Context, string) (*agent.Retrieval, error) {
	return &agent.Retrieval{Project: &agent.ProjectContext{Documents: 2, Chunks: 2}, Sources: []agent.Source{{Ref: "S1", ChunkID: "main", Source: "docs/architecture.md", Section: "Structure", Text: "Точка входа — cmd/codex-chat."}}}, nil
}
func TestProjectHelpChatPersistsWithSourcesAndIgnoresCorpusSwitches(t *testing.T) {
	store, err := history.NewJSONStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{respond: func(r agent.CompletionRequest) string {
		if strings.Contains(r.Instructions, "You independently verify") {
			return `{"claims":[{"index":0,"verdict":"supported","reason":"Прямо указано"}],"complete":true}`
		}
		return `{"status":"answered","claims":[{"text":"Точка входа — cmd/codex-chat.","evidence":[{"ref":"S1","start":1,"end":1}]}]}`
	}}
	h := NewHandler(llm, "test-model", store, agent.WithProjectRetriever(helpWebRetriever{}))
	w := performChatBody(h, nil, `{"message":"/help Где точка входа?","rag":false,"ragOptions":{"dialogue":true}}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var value apiResponse
	if json.Unmarshal(w.Body.Bytes(), &value) != nil || !strings.Contains(value.Answer, "cmd/codex-chat") {
		t.Fatal(w.Body.String())
	}
	if len(value.Messages) != 2 || value.Messages[1].Retrieval.Project == nil || len(value.Messages[1].Retrieval.Grounding.Quotes) != 1 {
		t.Fatal(value.Messages)
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("missing session")
	}
	// A new handler must restore the exact evidence from durable history.
	h = NewHandler(llm, "test-model", store, agent.WithProjectRetriever(helpWebRetriever{}))
	w = performChatBody(h, cookie, `{"message":"/help"}`)
	if w.Code != 200 || len(llm.requests) != 2 {
		t.Fatalf("menu invoked model: %d %d", w.Code, len(llm.requests))
	}
	json.Unmarshal(w.Body.Bytes(), &value)
	if len(value.Messages) != 4 || value.Messages[1].Retrieval.Project == nil {
		t.Fatal("evidence not restored")
	}
}
