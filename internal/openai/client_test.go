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
		_, _ = w.Write([]byte(`{"id":"` + responseID + `","model":"` + request.Model + `","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":200,"cache_write_tokens":100},"output_tokens":300,"output_tokens_details":{"reasoning_tokens":50},"total_tokens":1300}}`))
	}))
	defer server.Close()

	client, err := NewClient("test-key", "gpt-5.3-codex", server.URL+"/v1", "Be helpful.", &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	result, err := client.Respond(context.Background(), "first", "", chat.ResponseOptions{})
	if err != nil {
		t.Fatalf("first Respond() error = %v", err)
	}
	if result.ResponseID != "resp_1" || result.Output != "answer" || result.Model != "gpt-5.3-codex" {
		t.Fatalf("first response = %+v", result)
	}
	if result.Usage.TotalTokens != 1300 || result.Usage.CachedInputTokens != 200 || result.Usage.ReasoningTokens != 50 {
		t.Fatalf("first usage = %+v", result.Usage)
	}
	if result.CostUSD == nil || *result.CostUSD <= 0 {
		t.Fatalf("first cost = %v", result.CostUSD)
	}

	options := chat.ResponseOptions{
		Model:               "gpt-5.6-terra",
		Format:              "JSON object",
		LengthLimit:         "at most 120 words",
		CompletionCondition: "stop after the summary",
	}
	temperature := 0.7
	options.Temperature = &temperature
	result, err = client.Respond(context.Background(), "second", result.ResponseID, options)
	if err != nil {
		t.Fatalf("second Respond() error = %v", err)
	}
	if result.ResponseID != "resp_2" || result.Model != "gpt-5.6-terra" {
		t.Fatalf("second response = %+v", result)
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
	if second.Model != "gpt-5.6-terra" {
		t.Fatalf("second model = %q", second.Model)
	}
	if first.Temperature != nil || second.Temperature == nil || *second.Temperature != temperature {
		t.Fatalf("temperatures = (%v, %v), want (nil, %v)", first.Temperature, second.Temperature, temperature)
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

	if _, err := client.Respond(context.Background(), "hello", "", chat.ResponseOptions{}); err == nil {
		t.Fatal("Respond() error = nil, want an error")
	}
}
