package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"codex-chat-cli/internal/agent"
)

func TestReviewRequestIsBoundedAndNotStored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req responseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Store || req.MaxOutputTokens != 8000 || len(req.Tools) != 0 {
			t.Errorf("request=%+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"resp_review","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"findings\":[]}"}]}]}`))
	}))
	defer srv.Close()
	c, err := NewClient("test", srv.URL, "", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Complete(context.Background(), agent.CompletionRequest{Internal: true, NoStore: true, MaxOutputTokens: 8000, Model: "test", Input: "diff"})
	if err != nil {
		t.Fatal(err)
	}
}
