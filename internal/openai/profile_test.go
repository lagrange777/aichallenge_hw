package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/profile"
)

func TestProfileInMainAuxiliaryAndTokenRequests(t *testing.T) {
	var requests []responseRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request responseRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/input_tokens") {
			w.Write([]byte(`{"input_tokens":100}`))
			return
		}
		w.Write([]byte(`{"id":"answer","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`))
	}))
	defer server.Close()
	client, _ := NewClient("test-key", server.URL, "Base instructions.", server.Client())
	p := profile.Presets()[1]
	r := agent.CompletionRequest{Model: "test", Input: "первое сообщение", Profile: &p, Format: "one paragraph"}
	if _, err := client.CountTokens(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.Internal = true
	r.Instructions = "Return ONLY JSON."
	r.Format = "JSON"
	if _, err := client.Complete(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatalf("requests: %d", len(requests))
	}
	for _, request := range requests {
		for _, expected := range []string{`"language":"en"`, `"level":"expert"`, "FIRST response", "explicit current user request and current response options > configured profile > older preferences"} {
			if !strings.Contains(request.Instructions, expected) {
				t.Fatalf("missing %q", expected)
			}
		}
	}
	if !strings.Contains(requests[1].Instructions, "RESPONSE LANGUAGE: ENGLISH") {
		t.Fatal("resolved language rule missing")
	}
	if !strings.Contains(requests[1].Instructions, "Response format: one paragraph") {
		t.Fatal("request override dropped")
	}
	if !strings.Contains(requests[2].Instructions, "Return ONLY JSON.") || !strings.Contains(requests[2].Instructions, "This is INTERNAL processing") {
		t.Fatal("auxiliary schema protection missing")
	}
}
