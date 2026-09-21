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

func TestWorkflowAPIIsolationConflictsAndPause(t *testing.T) {
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
	cmd := memory.WorkflowCommand{TaskID: state.Task.ID, ProfileID: cookie.Value, Version: state.Task.Workflow.Version, Action: "pause"}
	call := func(identity *http.Cookie, origin string, command memory.WorkflowCommand) (int, apiResponse) {
		t.Helper()
		body, _ := json.Marshal(command)
		req := httptest.NewRequest("POST", "/api/tasks/state", strings.NewReader(string(body)))
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
		var payload apiResponse
		if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return res.Code, payload
	}
	if code, _ := call(cookie, "https://evil.example", cmd); code != 403 {
		t.Fatal("cross-origin command accepted")
	}
	if code, _ := call(nil, "", cmd); code != 409 {
		t.Fatal("foreign session command accepted")
	}
	invalid := cmd
	invalid.Action = "save"
	invalid.Progress = state.Task.Workflow.Progress
	invalid.Progress.Stage = "done"
	if code, _ := call(cookie, "", invalid); code != 400 {
		t.Fatal("invalid transition accepted")
	}
	code, payload := call(cookie, "", cmd)
	if code != 200 || !payload.Memory.Task.Workflow.Paused {
		t.Fatal("pause failed")
	}
	if code, _ = call(cookie, "", cmd); code != 409 {
		t.Fatal("stale command accepted")
	}
	count := llm.calls
	if chat = performChatBody(handler, cookie, `{"message":"Continue"}`); chat.Code != 409 || llm.calls != count {
		t.Fatal("paused chat called LLM")
	}
	handler = NewHandler(llm, "test-model", nil, agent.WithMemory(store), agent.WithProfiles(profiles))
	if chat = performChatBody(handler, cookie, `{"message":"Continue after restart"}`); chat.Code != 409 {
		t.Fatal("restart lost pause")
	}
	cmd.Action = "resume"
	cmd.Version = payload.Memory.Task.Workflow.Version
	code, payload = call(cookie, "", cmd)
	if code != 200 || payload.Memory.Task.Workflow.Paused {
		t.Fatal("resume failed")
	}
	if chat = performChatBody(handler, cookie, `{"message":"Continue","taskVersion":1}`); chat.Code != 409 {
		t.Fatal("stale chat accepted")
	}
	if chat = performChatBody(handler, cookie, `{"message":"Continue"}`); chat.Code != 200 {
		t.Fatal(chat.Body.String())
	}
	// Active profile changes must not allow an old tab to mutate another task.
	wrong := cmd
	wrong.ProfileID = "ffffffffffffffffffffffffffffffff"
	if code, _ = call(cookie, "", wrong); code != 409 {
		t.Fatal("wrong profile accepted")
	}
}

func TestControlledLifecycleAPI(t *testing.T) {
	store, _ := memory.NewStore(t.TempDir())
	handler := NewHandler(&fakeLLM{}, "test", nil, agent.WithMemory(store))
	chat := performChatBody(handler, nil, `{"message":"Начать"}`)
	cookie := sessionCookieFrom(t, chat)
	state, _ := store.Get(cookie.Value)
	call := func(cmd memory.WorkflowCommand) (int, apiResponse) {
		t.Helper()
		data, _ := json.Marshal(cmd)
		req := httptest.NewRequest("POST", "/api/tasks/state", strings.NewReader(string(data)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Codex-Chat", "1")
		req.AddCookie(cookie)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		var payload apiResponse
		if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return res.Code, payload
	}
	command := func(action string, p memory.Progress) memory.WorkflowCommand {
		return memory.WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: action, Progress: p}
	}
	p := state.Task.Workflow.Progress
	p.Stage = "done"
	p.Result = "Result"
	p.Validation = "Все отлично"
	code, payload := call(command("complete", p))
	if code != 400 || payload.Memory == nil || !strings.Contains(payload.Error, "validation") {
		t.Fatalf("skip response: %d %+v", code, payload)
	}
	if !payload.Memory.Task.Workflow.Events[0].Rejected || payload.Memory.Task.Workflow.Stage != "planning" {
		t.Fatal("rejection missing")
	}
	p = state.Task.Workflow.Progress
	p.Stage = "execution"
	if code, _ = call(command("approve_plan", p)); code != 400 {
		t.Fatal("empty plan approved")
	}
	p.Plan = "Реализовать, затем проверить"
	code, payload = call(command("approve_plan", p))
	if code != 200 {
		t.Fatal(payload.Error)
	}
	state = payload.Memory.State
	p = state.Task.Workflow.Progress
	p.Stage = "validation"
	p.Result = "Result"
	code, payload = call(command("submit_result", p))
	if code != 200 {
		t.Fatal(payload.Error)
	}
	state = payload.Memory.State
	p = state.Task.Workflow.Progress
	p.Stage = "done"
	p.Validation = "Строка вместо проверки"
	if code, _ = call(command("complete", p)); code != 400 {
		t.Fatal("text bypassed validation")
	}
	p = state.Task.Workflow.Progress
	p.Validation = "Проверены критерии готовности"
	cmd := command("record_validation", p)
	cmd.ValidationStatus = "passed"
	cmd.ValidationBasis = "user_report"
	code, payload = call(cmd)
	if code != 200 {
		t.Fatal(payload.Error)
	}
	state = payload.Memory.State
	p = state.Task.Workflow.Progress
	p.Stage = "done"
	code, payload = call(command("complete", p))
	if code != 200 || payload.Memory.Task.Workflow.Stage != "done" {
		t.Fatal("completion failed")
	}
}
