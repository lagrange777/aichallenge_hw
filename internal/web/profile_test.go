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
	"codex-chat-cli/internal/profile"
)

func TestProfileAPIIsolationAndRestart(t *testing.T) {
	root := t.TempDir()
	profiles, _ := profile.NewStore(filepath.Join(root, "profiles"))
	layers, _ := memory.NewStore(filepath.Join(root, "memory"))
	hist, _ := history.NewJSONStore(filepath.Join(root, "history.json"))
	llm := &fakeLLM{respond: func(r agent.CompletionRequest) string {
		if r.Internal {
			return `{"proposals":[]}`
		}
		return "answer"
	}}
	handler := NewHandler(llm, "test-model", hist, agent.WithMemory(layers), agent.WithProfiles(profiles))
	chat := performChatBody(handler, nil, `{"message":"primary conversation"}`)
	if chat.Code != 200 {
		t.Fatal(chat.Body.String())
	}
	cookie := sessionCookieFrom(t, chat)
	call := func(path string, body any, identity *http.Cookie, origin string) (int, apiResponse) {
		t.Helper()
		encoded, _ := json.Marshal(body)
		method := "POST"
		if body == nil {
			method = "GET"
		}
		req := httptest.NewRequest(method, path, strings.NewReader(string(encoded)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Codex-Chat", "1")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if identity != nil {
			req.AddCookie(identity)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		var result apiResponse
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return res.Code, result
	}
	code, result := call("/api/profiles", nil, cookie, "")
	if code != 200 || result.Profiles.ActiveID != cookie.Value || len(result.Profiles.Messages) != 2 {
		t.Fatal("initial profile not linked to chat")
	}
	body := map[string]any{"activeId": cookie.Value, "profile": profile.Presets()[0]}
	if code, _ = call("/api/profiles/new", body, cookie, "https://evil.example"); code != 403 {
		t.Fatal("cross origin mutation accepted")
	}
	code, result = call("/api/profiles/new", body, cookie, "")
	if code != 200 || len(result.Profiles.Profiles) != 2 {
		t.Fatalf("create: %d %#v", code, result)
	}
	id := result.Profiles.Profiles[1].ID
	switchBody := map[string]string{"activeId": cookie.Value, "id": id}
	if code, _ = call("/api/profiles/switch", switchBody, nil, ""); code != 409 {
		t.Fatal("profile accessible to another browser")
	}
	code, result = call("/api/profiles/switch", switchBody, cookie, "")
	if code != 200 || result.Profiles.ActiveID != id || len(result.Profiles.Messages) != 0 {
		t.Fatal("profile switch mixed histories")
	}
	stale, _ := json.Marshal(map[string]string{"message": "stale", "profileId": cookie.Value})
	if chat = performChatBody(handler, cookie, string(stale)); chat.Code != 409 {
		t.Fatal("stale profile chat accepted")
	}
	if chat = performChatBody(handler, cookie, `{"message":"new profile conversation"}`); chat.Code != 200 {
		t.Fatal(chat.Body.String())
	}
	profiles, _ = profile.NewStore(filepath.Join(root, "profiles"))
	handler = NewHandler(llm, "test-model", hist, agent.WithMemory(layers), agent.WithProfiles(profiles))
	code, result = call("/api/profiles", nil, cookie, "")
	if code != 200 || result.Profiles.ActiveID != id || result.Profiles.Messages[1].Profile.ID != id {
		t.Fatal("restart lost identity or snapshot")
	}
	code, result = call("/api/profiles/switch", map[string]string{"activeId": id, "id": cookie.Value}, cookie, "")
	if code != 200 || result.Profiles.Messages[0].Text != "primary conversation" {
		t.Fatal("primary conversation lost")
	}
}
