package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"codex-chat-cli/internal/agent"
)

type fakeLLM struct {
	mu       sync.Mutex
	requests []agent.CompletionRequest
	calls    int
	err      error
}

func (f *fakeLLM) Complete(_ context.Context, request agent.CompletionRequest) (agent.CompletionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	if f.err != nil {
		return agent.CompletionResponse{}, f.err
	}
	f.calls++
	return agent.CompletionResponse{
		ResponseID: "resp_" + string(rune('0'+f.calls)),
		Output:     "reply to " + request.Input,
		Model:      request.Model,
		Usage:      agent.Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
	}, nil
}

func TestChatPassesOptionalResponseFields(t *testing.T) {
	llm := &fakeLLM{}
	handler := NewHandler(llm, "test-model")
	body := `{"message":"question","responseFormat":" Markdown table ","lengthLimit":" 300 words ","completionCondition":" after recommendations ","temperature":0.4}`
	response := performChatBody(handler, nil, body)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	llm.mu.Lock()
	defer llm.mu.Unlock()
	want := agent.CompletionRequest{Input: "question", Model: "test-model", Format: "Markdown table", LengthLimit: "300 words", CompletionCondition: "after recommendations"}
	if len(llm.requests) != 1 || llm.requests[0].Temperature == nil || *llm.requests[0].Temperature != 0.4 {
		t.Fatalf("requests = %#v, want %#v", llm.requests, want)
	}
	got := llm.requests[0]
	got.Temperature = nil
	if got != want {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
}

func TestEmptyMessageIgnoresOptionalResponseFields(t *testing.T) {
	llm := &fakeLLM{}
	handler := NewHandler(llm, "test-model")
	body := `{"message":"  ","responseFormat":"JSON","lengthLimit":"10 words","completionCondition":"immediately","temperature":2}`
	response := performChatBody(handler, nil, body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if llm.calls != 0 || len(llm.requests) != 0 {
		t.Fatalf("LLM was called for an empty message: calls=%d requests=%#v", llm.calls, llm.requests)
	}
}

func TestServesWebApplication(t *testing.T) {
	handler := NewHandler(&fakeLLM{}, "test-model")

	for _, test := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/", contentType: "text/html", contains: `id="response-options"`},
		{path: "/app.css", contentType: "text/css", contains: ".app-shell"},
		{path: "/app.js", contentType: "text/javascript", contains: "sendMessage"},
		{path: "/markdown.js", contentType: "text/javascript", contains: "CodexMarkdown"},
		{path: "/favicon.svg", contentType: "image/svg+xml", contains: "<svg"},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			if !strings.HasPrefix(response.Header().Get("Content-Type"), test.contentType) {
				t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
			}
			if !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("body does not contain %q", test.contains)
			}
			if response.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("Content-Security-Policy is missing")
			}
		})
	}
}

func TestLandingPageDoesNotContainSuggestionButtons(t *testing.T) {
	handler := NewHandler(&fakeLLM{}, "test-model")
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	for _, label := range []string{"Изучить проект", "Проверить код", "Объяснить код"} {
		if strings.Contains(response.Body.String(), label) {
			t.Fatalf("landing page still contains %q", label)
		}
	}
}

func TestChatCarriesConversationState(t *testing.T) {
	llm := &fakeLLM{}
	handler := NewHandler(llm, "gpt-5.3-codex")

	first := performChat(handler, nil, "first")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	cookie := sessionCookieFrom(t, first)

	second := performChat(handler, cookie, "second")
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", second.Code, second.Body.String())
	}

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.requests) != 2 || llm.requests[0].PreviousResponseID != "" || llm.requests[1].PreviousResponseID != "resp_1" {
		t.Fatalf("requests = %#v", llm.requests)
	}

	var payload apiResponse
	if err := json.Unmarshal(second.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Answer != "reply to second" {
		t.Fatalf("answer = %q", payload.Answer)
	}
	if payload.Model != "gpt-5.3-codex" || payload.Metrics == nil || payload.Metrics.TotalTokens != 120 || payload.Metrics.CostUSD == nil {
		t.Fatalf("response metadata = model %q, metrics %#v", payload.Model, payload.Metrics)
	}
}

func TestResetStartsFreshConversation(t *testing.T) {
	llm := &fakeLLM{}
	handler := NewHandler(llm, "test-model")

	first := performChat(handler, nil, "first")
	cookie := sessionCookieFrom(t, first)

	resetRequest := httptest.NewRequest(http.MethodPost, "/api/reset", nil)
	resetRequest.Header.Set("X-Codex-Chat", "1")
	resetRequest.AddCookie(cookie)
	resetResponse := httptest.NewRecorder()
	handler.ServeHTTP(resetResponse, resetRequest)
	if resetResponse.Code != http.StatusNoContent {
		t.Fatalf("reset status = %d", resetResponse.Code)
	}

	second := performChat(handler, cookie, "second")
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d", second.Code)
	}

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.requests) != 2 || llm.requests[1].PreviousResponseID != "" {
		t.Fatalf("requests = %#v", llm.requests)
	}
}

func TestBrowserSessionsAreIsolated(t *testing.T) {
	llm := &fakeLLM{}
	handler := NewHandler(llm, "test-model")

	first := performChat(handler, nil, "browser one")
	second := performChat(handler, nil, "browser two")
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d, %d", first.Code, second.Code)
	}

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.requests) != 2 || llm.requests[0].PreviousResponseID != "" || llm.requests[1].PreviousResponseID != "" {
		t.Fatalf("requests = %#v", llm.requests)
	}
}

func TestAPIValidation(t *testing.T) {
	handler := NewHandler(&fakeLLM{}, "test-model")

	tests := []struct {
		name        string
		method      string
		body        string
		contentType string
		header      bool
		origin      string
		want        int
	}{
		{name: "method", method: http.MethodGet, header: true, want: http.StatusMethodNotAllowed},
		{name: "request header", method: http.MethodPost, contentType: "application/json", body: `{\"message\":\"hi\"}`, want: http.StatusForbidden},
		{name: "content type", method: http.MethodPost, header: true, contentType: "text/plain", body: "hi", want: http.StatusUnsupportedMediaType},
		{name: "invalid json", method: http.MethodPost, header: true, contentType: "application/json", body: `{`, want: http.StatusBadRequest},
		{name: "empty", method: http.MethodPost, header: true, contentType: "application/json", body: `{"message":"  "}`, want: http.StatusBadRequest},
		{name: "temperature below range", method: http.MethodPost, header: true, contentType: "application/json", body: `{"message":"hi","temperature":-0.1}`, want: http.StatusBadRequest},
		{name: "temperature above range", method: http.MethodPost, header: true, contentType: "application/json", body: `{"message":"hi","temperature":2.1}`, want: http.StatusBadRequest},
		{name: "unknown model", method: http.MethodPost, header: true, contentType: "application/json", body: `{"message":"hi","model":"unknown"}`, want: http.StatusBadRequest},
		{name: "cross origin", method: http.MethodPost, header: true, origin: "https://example.org", contentType: "application/json", body: `{\"message\":\"hi\"}`, want: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/api/chat", strings.NewReader(test.body))
			if test.header {
				request.Header.Set("X-Codex-Chat", "1")
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestStatusAndHealth(t *testing.T) {
	handler := NewHandler(&fakeLLM{}, "test-model")

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), "test-model") || !strings.Contains(statusResponse.Body.String(), "gpt-5.6-terra") {
		t.Fatalf("status response = %d %q", statusResponse.Code, statusResponse.Body.String())
	}

	healthRequest := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthResponse := httptest.NewRecorder()
	handler.ServeHTTP(healthResponse, healthRequest)
	if healthResponse.Code != http.StatusOK || healthResponse.Body.String() != "ok\n" {
		t.Fatalf("health response = %d %q", healthResponse.Code, healthResponse.Body.String())
	}
}

func TestChatPassesSelectedModel(t *testing.T) {
	llm := &fakeLLM{}
	handler := NewHandler(llm, "gpt-5.3-codex")
	response := performChatBody(handler, nil, `{"message":"question","model":"gpt-5.6-luna"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.requests) != 1 || llm.requests[0].Model != "gpt-5.6-luna" {
		t.Fatalf("requests = %#v", llm.requests)
	}
}

func performChat(handler http.Handler, cookie *http.Cookie, message string) *httptest.ResponseRecorder {
	return performChatBody(handler, cookie, `{"message":`+quoteJSON(message)+`}`)
}

func performChatBody(handler http.Handler, cookie *http.Cookie, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Codex-Chat", "1")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func sessionCookieFrom(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookie {
			return cookie
		}
	}
	t.Fatal("session cookie is missing")
	return nil
}
