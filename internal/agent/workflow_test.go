package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"codex-chat-cli/internal/memory"
	"codex-chat-cli/internal/profile"
)

type workflowLLM struct {
	requests []CompletionRequest
	invalid  bool
}

func (f *workflowLLM) Complete(_ context.Context, r CompletionRequest) (CompletionResponse, error) {
	if r.Instructions == invariantCheckInstructions {
		return allowLifecycleCheck(r), nil
	}
	f.requests = append(f.requests, r)
	if r.Internal {
		if f.invalid {
			return CompletionResponse{Output: "invalid JSON"}, nil
		}
		var input struct {
			Task memory.Task `json:"task"`
		}
		_ = json.Unmarshal([]byte(r.Input), &input)
		p := input.Task.Workflow.Progress
		p.CurrentStep = "Проверить хеш"
		p.ExpectedActor = "agent"
		p.ExpectedAction = "Объяснить проверку хеша"
		p.Completed = "Схема согласована"
		data, _ := json.Marshal(map[string]any{"proposals": []memory.Proposal{}, "taskProposal": memory.TaskProposal{Progress: p, Reason: "Есть следующий шаг"}})
		return CompletionResponse{Output: string(data)}, nil
	}
	answer := "Нужен план"
	if strings.Contains(r.History[0].Content, "Схема согласована") && strings.Contains(r.History[0].Content, "Проверить хеш") {
		answer = "Продолжаю с проверки хеша"
	}
	return CompletionResponse{Output: answer}, nil
}
func TestTaskCheckpointSurvivesResetRestartAndControlsRequests(t *testing.T) {
	root := t.TempDir()
	store, _ := memory.NewStore(root)
	hist := &memoryHistory{}
	llm := &workflowLLM{}
	a, err := NewPersistent(llm, "test", profileID, hist, WithMemory(store))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Ask(context.Background(), Request{Message: "Авторизация"}); err != nil {
		t.Fatal(err)
	}
	state, _ := a.Memories()
	if state.Task.Workflow.CurrentStep == "Проверить хеш" || state.Task.Workflow.Proposal == nil {
		t.Fatal("unconfirmed progress applied")
	}
	state, err = a.UpdateWorkflow(memory.WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: "accept"})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Reset(); err != nil {
		t.Fatal(err)
	}
	state, err = a.UpdateWorkflow(memory.WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: "pause"})
	if err != nil {
		t.Fatal(err)
	}
	store, _ = memory.NewStore(root)
	a, err = NewPersistent(llm, "test", profileID, hist, WithMemory(store))
	if err != nil {
		t.Fatal(err)
	}
	count := len(llm.requests)
	if _, err = a.Ask(context.Background(), Request{Message: "continue", ContextStrategy: "sticky_facts"}); !errors.Is(err, ErrTaskPaused) {
		t.Fatal("paused request allowed")
	}
	if len(llm.requests) != count {
		t.Fatal("paused task called LLM")
	}
	state, err = a.UpdateWorkflow(memory.WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: "resume"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Ask(context.Background(), Request{Message: "Продолжи", ContextStrategy: "sliding_window"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Продолжаю с проверки хеша" {
		t.Fatalf("resume answer=%s", result.Text)
	}
	if strings.Contains(llm.requests[len(llm.requests)-2].History[0].Content, `"proposal"`) {
		t.Fatal("unconfirmed proposal leaked into context")
	}
	stale := state.Task.Workflow.Version
	if _, err = a.Ask(context.Background(), Request{Message: "stale", TaskVersion: &stale}); !errors.Is(err, memory.ErrConflict) {
		t.Fatal("stale version allowed")
	}
	llm.invalid = true
	if _, err = a.Ask(context.Background(), Request{Message: "Сохрани последний обмен даже при ошибке извлечения"}); err != nil {
		t.Fatal(err)
	}
	state, _ = a.Memories()
	if !strings.Contains(state.Task.Workflow.LastTurn.User, "ошибке") || state.Task.Workflow.Proposal != nil {
		t.Fatal("extraction failure lost recovery context")
	}
	for _, action := range []string{"approve_plan", "submit_result", "record_validation", "complete"} {
		p := state.Task.Workflow.Progress
		p.Stage = map[string]string{"approve_plan": "execution", "submit_result": "validation", "record_validation": "validation", "complete": "done"}[action]
		p.Plan = "Проверить хеш и подготовить результат"
		p.Result = "Result"
		p.Validation = "User verified"
		state, err = a.UpdateWorkflow(memory.WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: action, Progress: p, ValidationStatus: "passed", ValidationBasis: "user_report"})
		if err != nil {
			t.Fatal(err)
		}
	}
	count = len(llm.requests)
	if _, err = a.Ask(context.Background(), Request{Message: "continue"}); !errors.Is(err, ErrTaskDone) || len(llm.requests) != count {
		t.Fatal("completed task called LLM")
	}
}

func TestWorkflowIsIsolatedByProfile(t *testing.T) {
	store, _ := memory.NewStore(t.TempDir())
	profiles, _ := profile.NewStore(t.TempDir())
	a, err := NewPersistent(&workflowLLM{}, "test", profileID, &memoryHistory{}, WithMemory(store), WithProfiles(profiles))
	if err != nil {
		t.Fatal(err)
	}
	original, _ := a.Memories()
	original, err = a.UpdateWorkflow(memory.WorkflowCommand{ProfileID: profileID, TaskID: original.Task.ID, Version: original.Task.Workflow.Version, Action: "pause"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := a.SaveProfile(profileID, profile.Presets()[0], true)
	if err != nil {
		t.Fatal(err)
	}
	secondID := view.Profiles[1].ID
	view, err = a.SwitchProfile(profileID, secondID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Memory.Task.Workflow.Paused || view.Memory.Task.ID == original.Task.ID {
		t.Fatal("checkpoint leaked to another profile")
	}
	if _, err = a.UpdateWorkflow(memory.WorkflowCommand{ProfileID: profileID, TaskID: original.Task.ID, Version: original.Task.Workflow.Version, Action: "resume"}); !errors.Is(err, profile.ErrConflict) {
		t.Fatal("stale profile mutated workflow")
	}
	view, err = a.SwitchProfile(secondID, profileID)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Memory.Task.Workflow.Paused || view.Memory.Task.Workflow.Version != original.Task.Workflow.Version {
		t.Fatal("profile switch lost pause")
	}
}

// Legacy memory/profile tests model successful lifecycle checks independently
// from their own extraction fixtures. Guard-specific tests use invariantLLM.
func allowLifecycleCheck(r CompletionRequest) CompletionResponse {
	var input struct {
		Rules []memory.Invariant `json:"rules"`
	}
	_ = json.Unmarshal([]byte(r.Input), &input)
	ids := []string{}
	for _, rule := range input.Rules {
		ids = append(ids, rule.ID)
	}
	data, _ := json.Marshal(invariantVerdict{Verdict: "allow", CheckedIDs: ids, ConflictingIDs: []string{}, Explanation: "Ответ соответствует текущему этапу."})
	return CompletionResponse{Output: string(data)}
}
