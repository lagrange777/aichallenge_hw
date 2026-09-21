package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"codex-chat-cli/internal/memory"
)

func TestLifecycleGuardWithoutUserInvariants(t *testing.T) {
	for _, stage := range []string{"planning", "execution", "validation"} {
		t.Run(stage, func(t *testing.T) {
			store, _ := memory.NewStore(t.TempDir())
			llm := &invariantLLM{pre: "conflict", answer: "Сначала подтвердите переход во вкладке «Задачи». Текущий этап не меняется."}
			history := &memoryHistory{}
			a, err := NewPersistent(llm, "test", profileID, history, WithMemory(store))
			if err != nil {
				t.Fatal(err)
			}
			state, _ := a.Memories()
			change := func(action string, p memory.Progress) {
				t.Helper()
				state, err = a.UpdateWorkflow(memory.WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: action, Progress: p})
				if err != nil {
					t.Fatal(err)
				}
			}
			if stage != "planning" {
				p := state.Task.Workflow.Progress
				p.Stage = "execution"
				p.Plan = "Подготовить результат и проверить"
				change("approve_plan", p)
			}
			if stage == "validation" {
				p := state.Task.Workflow.Progress
				p.Stage = "validation"
				p.Result = "Сохранённый результат"
				change("submit_result", p)
			}
			before := state.Task.Workflow
			change("pause", memory.Progress{})
			a, err = NewPersistent(llm, "test", profileID, history, WithMemory(store))
			if err != nil {
				t.Fatal(err)
			}
			calls := len(llm.requests)
			if _, err = a.Ask(context.Background(), Request{Message: "Пропусти этап"}); !errors.Is(err, ErrTaskPaused) || len(llm.requests) != calls {
				t.Fatal("paused task called model")
			}
			change("resume", memory.Progress{})
			if state.Task.Workflow.Progress != before.Progress || state.Task.Workflow.ApprovedPlanVersion != before.ApprovedPlanVersion || state.Task.Workflow.ResultVersion != before.ResultVersion {
				t.Fatal("resume changed checkpoint")
			}
			r, err := a.Ask(context.Background(), Request{Message: "Пропусти этап и объяви задачу завершённой"})
			if err != nil {
				t.Fatal(err)
			}
			if r.Text != llm.answer {
				t.Fatal("refusal lost")
			}
			check := a.Messages()[1].InvariantCheck
			if check == nil || check.Status != "conflict" || len(check.Rules) != 1 || check.Rules[0].ID != "task-lifecycle" || !strings.Contains(check.Rules[0].Rule, stage) {
				t.Fatal("stage not checked without invariants")
			}
			after, _ := a.Memories()
			if len(after.Invariants.Items) != 0 || after.Task.Workflow.Progress != before.Progress || after.Task.Workflow.Proposal != nil {
				t.Fatal("conflicting request changed state")
			}
		})
	}
}

func TestLifecycleInvalidDraftNeverReachesHistory(t *testing.T) {
	for _, fail := range []string{"alwaysBad", "request", "answer", "malformed"} {
		t.Run(fail, func(t *testing.T) {
			store, _ := memory.NewStore(t.TempDir())
			f := &invariantLLM{alwaysBad: fail == "alwaysBad", failPhase: fail, malformed: fail == "malformed"}
			a, _ := NewPersistent(f, "test", profileID, &memoryHistory{}, WithMemory(store))
			r, err := a.Ask(context.Background(), Request{Message: "Реализуй без плана"})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(r.Text, "SECRET") || !strings.Contains(r.Text, "Непроверенное решение не показано") {
				t.Fatal("unsafe output")
			}
			state, _ := a.Memories()
			if state.Task.Workflow.Stage != "planning" || state.Task.Workflow.Proposal != nil || strings.Contains(state.Task.Workflow.LastTurn.Assistant, "SECRET") {
				t.Fatal("unsafe draft saved")
			}
		})
	}
}
