package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-chat-cli/internal/chat"
)

func TestRespondSendsConversationState(t *testing.T) {
	requests := make(chan responseRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}

		var request responseRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- request

		responseID := "resp_1"
		if request.PreviousResponseID != "" {
			responseID = "resp_2"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + responseID + `","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`))
	}))
	defer server.Close()

	client, err := NewClient("test-key", "gpt-5.3-codex", server.URL+"/v1", "Be helpful.", &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	id, text, err := client.Respond(context.Background(), "first", "", chat.ResponseOptions{})
	if err != nil {
		t.Fatalf("first Respond() error = %v", err)
	}
	if id != "resp_1" || text != "answer" {
		t.Fatalf("first response = (%q, %q)", id, text)
	}

	options := chat.ResponseOptions{
		Format:              "JSON object",
		LengthLimit:         "at most 120 words",
		CompletionCondition: "stop after the summary",
	}
	id, _, err = client.Respond(context.Background(), "second", id, options)
	if err != nil {
		t.Fatalf("second Respond() error = %v", err)
	}
	if id != "resp_2" {
		t.Fatalf("second response ID = %q", id)
	}

	first := <-requests
	second := <-requests
	if first.Model != "gpt-5.3-codex" || first.Input != "first" || !first.Store {
		t.Fatalf("first request = %+v", first)
	}
	if first.Instructions != "Be helpful." {
		t.Fatalf("first instructions = %q", first.Instructions)
	}
	for _, requirement := range []string{"Response format: JSON object", "Response length limit: at most 120 words", "Completion condition: stop after the summary"} {
		if !strings.Contains(second.Instructions, requirement) {
			t.Fatalf("second instructions do not contain %q: %q", requirement, second.Instructions)
		}
	}
	if second.PreviousResponseID != "resp_1" {
		t.Fatalf("previous_response_id = %q", second.PreviousResponseID)
	}
}

func TestRespondReturnsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key","code":"invalid_api_key"}}`))
	}))
	defer server.Close()

	client, err := NewClient("test-key", "model", server.URL, "", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if _, _, err := client.Respond(context.Background(), "hello", "", chat.ResponseOptions{}); err == nil {
		t.Fatal("Respond() error = nil, want an error")
	}
}
