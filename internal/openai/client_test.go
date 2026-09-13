package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"codex-chat-cli/internal/agent"
)

func TestCompleteSendsAgentRequest(t *testing.T) {
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

	client, err := NewClient("test-key", server.URL+"/v1", "Be helpful.", &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	result, err := client.Complete(context.Background(), agent.CompletionRequest{Input: "first", Model: "gpt-5.3-codex"})
	if err != nil {
		t.Fatalf("first Complete() error = %v", err)
	}
	if result.ResponseID != "resp_1" || result.Output != "answer" || result.Model != "gpt-5.3-codex" {
		t.Fatalf("first response = %+v", result)
	}
	if result.Usage.TotalTokens != 1300 || result.Usage.CachedInputTokens != 200 || result.Usage.ReasoningTokens != 50 {
		t.Fatalf("first usage = %+v", result.Usage)
	}

	request := agent.CompletionRequest{
		Input:               "second",
		Model:               "gpt-5.6-terra",
		PreviousResponseID:  result.ResponseID,
		Format:              "JSON object",
		LengthLimit:         "at most 120 words",
		CompletionCondition: "stop after the summary",
	}
	temperature := 0.7
	request.Temperature = &temperature
	result, err = client.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("second Complete() error = %v", err)
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

func TestCompleteSendsReplayedHistoryAsMessages(t *testing.T) {
	requests := make(chan []inputMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Input              json.RawMessage `json:"input"`
			PreviousResponseID string          `json:"previous_response_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload.PreviousResponseID != "" {
			t.Errorf("previous_response_id = %q, want empty", payload.PreviousResponseID)
		}
		var messages []inputMessage
		if err := json.Unmarshal(payload.Input, &messages); err != nil {
			t.Errorf("decode input messages: %v", err)
		}
		requests <- messages
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_new","model":"model-b","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"continued"}]}],"usage":{"input_tokens":30,"output_tokens":5,"total_tokens":35}}`))
	}))
	defer server.Close()

	client, err := NewClient("test-key", server.URL, "", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.Complete(context.Background(), agent.CompletionRequest{
		Input: "What is my name?",
		History: []agent.ContextMessage{
			{Role: "user", Content: "My name is Mila."},
			{Role: "assistant", Content: "Nice to meet you, Mila."},
		},
		Model: "model-b",
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	want := []inputMessage{
		{Role: "user", Content: "My name is Mila."},
		{Role: "assistant", Content: "Nice to meet you, Mila."},
		{Role: "user", Content: "What is my name?"},
	}
	got := <-requests
	if len(got) != len(want) {
		t.Fatalf("input messages = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("input messages[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestRespondReturnsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key","code":"invalid_api_key"}}`))
	}))
	defer server.Close()

	client, err := NewClient("test-key", server.URL, "", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if _, err := client.Complete(context.Background(), agent.CompletionRequest{Input: "hello", Model: "model"}); err == nil {
		t.Fatal("Complete() error = nil, want an error")
	}
}

func TestCountTokensUsesCurrentAndPreviousResponseContexts(t *testing.T) {
	requests := make(chan inputTokenRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses/input_tokens" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var request inputTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- request
		count := 25
		if request.PreviousResponseID != "" {
			count = 125
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":` + strconv.Itoa(count) + `}`))
	}))
	defer server.Close()

	client, err := NewClient("test-key", server.URL+"/v1", "Be helpful.", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	counts, err := client.CountTokens(context.Background(), agent.CompletionRequest{
		Input:              "question",
		Model:              "gpt-5.3-codex",
		PreviousResponseID: "resp_1",
		Format:             "Markdown",
	})
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	if counts.CurrentRequestTokens != 25 || counts.ProjectedInputTokens != 125 {
		t.Fatalf("counts = %#v", counts)
	}
	first := <-requests
	second := <-requests
	if first.PreviousResponseID == second.PreviousResponseID {
		t.Fatalf("requests = %#v, %#v", first, second)
	}
	for _, request := range []inputTokenRequest{first, second} {
		if request.Input != "question" || request.Model != "gpt-5.3-codex" || !strings.Contains(request.Instructions, "Response format: Markdown") {
			t.Fatalf("token count request = %#v", request)
		}
	}
}

func TestCountTokensSeparatesCurrentRequestFromReplayedHistory(t *testing.T) {
	requests := make(chan inputTokenRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request inputTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- request
		count := 20
		if _, ok := request.Input.([]any); ok {
			count = 90
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":` + strconv.Itoa(count) + `}`))
	}))
	defer server.Close()

	client, err := NewClient("test-key", server.URL, "", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	counts, err := client.CountTokens(context.Background(), agent.CompletionRequest{
		Input:   "current",
		History: []agent.ContextMessage{{Role: "user", Content: "earlier"}, {Role: "assistant", Content: "answer"}},
		Model:   "model-b",
	})
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	if counts.CurrentRequestTokens != 20 || counts.ProjectedInputTokens != 90 {
		t.Fatalf("counts = %#v", counts)
	}

	first := <-requests
	second := <-requests
	_, firstIsHistory := first.Input.([]any)
	_, secondIsHistory := second.Input.([]any)
	if firstIsHistory == secondIsHistory {
		t.Fatalf("expected one current-only and one replayed-history request: %#v, %#v", first, second)
	}
}
