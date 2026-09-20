package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/history"
	"codex-chat-cli/internal/memory"
)

func TestMemoryAPIReviewIsolationAndTaskReset(t *testing.T) {
	root := t.TempDir()
	store, _ := memory.NewStore(filepath.Join(root, "memory"))
	hist, _ := history.NewJSONStore(filepath.Join(root, "history.json"))
	llm := &fakeLLM{respond: func(r agent.CompletionRequest) string {
		if strings.Contains(r.Instructions, "You suggest memories") {
			return `{"proposals":[{"layer":"working","key":"language","value":"Russian","reason":"User preference"}]}`
		}
		return "answer"
	}}
	handler := NewHandler(llm, "test-model", hist, agent.WithMemory(store))
	chat := performChatBody(handler, nil, `{"message":"remember","contextStrategy":"sliding_window","contextKeepLast":1}`)
	if chat.Code != 200 {
		t.Fatalf("chat = %d %s", chat.Code, chat.Body.String())
	}
	cookie := sessionCookieFrom(t, chat)
	call := func(path, body string, identity *http.Cookie, origin string) (int, apiResponse) {
		t.Helper()
		method := "POST"
		if body == "" {
			method = "GET"
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Codex-Chat", "1")
		if identity != nil {
			req.AddCookie(identity)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		var payload apiResponse
		if res.Code != 204 {
			if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
		}
		return res.Code, payload
	}
	_, payload := call("/api/memory", "", cookie, "")
	if payload.Memory == nil || len(payload.Memory.Proposals) != 1 || len(payload.Memory.Working) != 0 {
		t.Fatalf("memory = %#v", payload.Memory)
	}
	proposal := payload.Memory.Proposals[0]
	task := payload.Memory.Task.ID
	body, _ := json.Marshal(map[string]string{"id": proposal.ID, "taskId": task, "action": "accept", "layer": "long_term", "key": "language", "value": "Russian"})
	if code, _ := call("/api/memory/review", string(body), cookie, "https://evil.example"); code != 403 {
		t.Fatalf("cross-origin status = %d", code)
	}
	if code, _ := call("/api/memory/review", string(body), nil, ""); code != 409 {
		t.Fatalf("other profile review = %d", code)
	}
	if code, p := call("/api/memory/review", string(body), cookie, ""); code != 200 || len(p.Memory.LongTerm) != 1 {
		t.Fatalf("review = %d %#v", code, p)
	}
	if code, _ := call("/api/reset", "{}", cookie, ""); code != 204 {
		t.Fatalf("reset = %d", code)
	}
	_, payload = call("/api/memory", "", cookie, "")
	if payload.Memory.ShortTermMessages != 0 || len(payload.Memory.LongTerm) != 1 {
		t.Fatal("new chat lost confirmed profile")
	}
	body, _ = json.Marshal(map[string]string{"taskId": task, "name": "Next task"})
	if code, p := call("/api/tasks/new", string(body), cookie, ""); code != 200 || p.Memory.Task.ID == task || len(p.Memory.LongTerm) != 1 {
		t.Fatalf("new task = %d %#v", code, p)
	}
	// A new handler models a server restart; the cookie still owns the same profile.
	handler = NewHandler(llm, "test-model", hist, agent.WithMemory(store))
	_, payload = call("/api/memory", "", cookie, "")
	if payload.Memory.Task.Name != "Next task" || len(payload.Memory.LongTerm) != 1 {
		t.Fatal("restart lost memory")
	}
}

func TestSaveMessageMemoryAPI(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{respond: func(r agent.CompletionRequest) string {
		if strings.Contains(r.Instructions, "You suggest memories") {
			return `{"proposals":[]}`
		}
		return "assistant answer"
	}}
	handler := NewHandler(llm, "test-model", nil, agent.WithMemory(store))
	chat := performChatBody(handler, nil, `{"message":"user text"}`)
	if chat.Code != 200 {
		t.Fatalf("chat: %s", chat.Body.String())
	}
	cookie := sessionCookieFrom(t, chat)
	var result apiResponse
	if err = json.Unmarshal(chat.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 2 || result.Messages[0].ID == "" {
		t.Fatal("chat response lacks persistent message references")
	}
	state, err := store.Get(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	save := func(layer string, identity *http.Cookie, origin string) (int, apiResponse) {
		t.Helper()
		data, _ := json.Marshal(map[string]string{"taskId": state.Task.ID, "messageId": result.Messages[0].ID, "layer": layer, "key": "User choice", "value": "Edited excerpt"})
		request := httptest.NewRequest("POST", "/api/memory/from-message", strings.NewReader(string(data)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Codex-Chat", "1")
		if identity != nil {
			request.AddCookie(identity)
		}
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var payload apiResponse
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return response.Code, payload
	}
	if code, _ := save("working", cookie, "https://evil.example"); code != 403 {
		t.Fatalf("foreign origin = %d", code)
	}
	if code, _ := save("working", nil, ""); code != 409 {
		t.Fatalf("foreign session = %d", code)
	}
	if code, _ := save("short_term", cookie, ""); code != 400 {
		t.Fatalf("invalid layer = %d", code)
	}
	if code, payload := save("working", cookie, ""); code != 200 || len(payload.Memory.Working) != 1 || payload.Memory.Working[0].Value != "Edited excerpt" {
		t.Fatalf("manual save = %d %#v", code, payload)
	}
	if code, payload := save("long_term", cookie, ""); code != 200 || len(payload.Memory.LongTerm) != 1 {
		t.Fatalf("second layer save = %d %#v", code, payload)
	}
	if llm.calls != 2 {
		t.Fatalf("saving triggered model calls: %d", llm.calls)
	}
}
