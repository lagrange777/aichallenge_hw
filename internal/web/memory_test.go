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
