package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/memory"
	"codex-chat-cli/internal/profile"
)

func TestInvariantAPIIsolationVersionAndExplicitMutation(t *testing.T) {
	root := t.TempDir()
	store, _ := memory.NewStore(filepath.Join(root, "memory"))
	profiles, _ := profile.NewStore(filepath.Join(root, "profiles"))
	llm := &fakeLLM{respond: func(r agent.CompletionRequest) string {
		if r.Internal {
			return `{"proposals":[]}`
		}
		return "answer"
	}}
	handler := NewHandler(llm, "test-model", nil, agent.WithMemory(store), agent.WithProfiles(profiles))
	chat := performChatBody(handler, nil, `{"message":"Start"}`)
	if chat.Code != 200 {
		t.Fatal(chat.Body.String())
	}
	cookie := sessionCookieFrom(t, chat)
	state, _ := store.Get(cookie.Value)
	cmd := memory.InvariantCommand{TaskID: state.Task.ID, ProfileID: cookie.Value, Version: state.Invariants.Version, Action: "create", Rule: memory.Invariant{Category: "architecture", Title: "Монолит", Rule: "Один сервис"}}
	call := func(identity *http.Cookie, origin string, cmd memory.InvariantCommand) (int, apiResponse) {
		t.Helper()
		body, _ := json.Marshal(cmd)
		req := httptest.NewRequest("POST", "/api/tasks/invariants", strings.NewReader(string(body)))
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
		if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return res.Code, payload
	}
	if code, _ := call(cookie, "https://evil.example", cmd); code != 403 {
		t.Fatal("foreign origin accepted")
	}
	if code, _ := call(nil, "", cmd); code != 409 {
		t.Fatal("foreign session accepted")
	}
	invalid := cmd
	invalid.Rule.Category = "unknown"
	if code, _ := call(cookie, "", invalid); code != 400 {
		t.Fatal("invalid rule accepted")
	}
	cmd.Rule.Category = "other"
	code, p := call(cookie, "", cmd)
	if code != 200 || len(p.Memory.Invariants.Active()) != 1 || p.Memory.Invariants.Items[0].Category != "other" {
		t.Fatalf("create: %d %+v", code, p)
	}
	if code, _ = call(cookie, "", cmd); code != 409 {
		t.Fatal("stale rule mutation accepted")
	}
	if llm.calls != 2 {
		t.Fatal("rule mutation called model")
	}
	cmd.Version = p.Memory.Invariants.Version
	cmd.Action = "disable"
	cmd.Rule = p.Memory.Invariants.Items[0]
	wrong := cmd
	wrong.ProfileID = "ffffffffffffffffffffffffffffffff"
	if code, _ = call(cookie, "", wrong); code != 409 {
		t.Fatal("foreign profile accepted")
	}
	handler = NewHandler(llm, "test-model", nil, agent.WithMemory(store), agent.WithProfiles(profiles))
	code, p = call(cookie, "", cmd)
	if code != 200 || len(p.Memory.Invariants.Active()) != 0 {
		t.Fatal("restart lost rules")
	}
}
