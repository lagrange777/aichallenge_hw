package memory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkflowTransitionsPauseAndRestore(t *testing.T) {
	root := t.TempDir()
	store, _ := NewStore(root)
	owner := "00112233445566778899aabbccddeeff"
	state, _ := store.Get(owner)
	change := func(action string, p Progress) {
		t.Helper()
		var err error
		state, err = store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: action, Progress: p, ValidationStatus: "passed", ValidationBasis: "user_report"})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, stage := range []string{"planning", "execution", "validation"} {
		p := state.Task.Workflow.Progress
		p.Stage = stage
		p.Plan = "Составить схему, реализовать, проверить"
		p.Completed = "Схема согласована"
		p.Result = "Результат"
		action := map[string]string{"planning": "save", "execution": "approve_plan", "validation": "submit_result"}[stage]
		change(action, p)
		change("pause", Progress{})
		before := state.Task.Workflow
		store, _ = NewStore(root)
		state, _ = store.Get(owner)
		if !state.Task.Workflow.Paused || state.Task.Workflow.Progress != before.Progress || state.Task.Workflow.ApprovedPlanVersion != before.ApprovedPlanVersion {
			t.Fatal("pause checkpoint lost")
		}
		if _, err := store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: "save", Progress: p}); !errors.Is(err, ErrConflict) {
			t.Fatal("paused task edited")
		}
		other, err := store.NewTask(owner, state.Task.ID, "Another")
		if err != nil {
			t.Fatal(err)
		}
		state, err = store.SwitchTask(owner, other.Task.ID, state.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Task.Workflow.Paused || state.Task.Workflow.Stage != stage {
			t.Fatal("task switch lost checkpoint")
		}
		change("resume", Progress{})
	}
	p := state.Task.Workflow.Progress
	p.Validation = "Пользователь проверил результат"
	change("record_validation", p)
	p.Stage = "execution"
	p.Validation = "Исправить пограничный случай"
	change("request_changes", p)
	if state.Task.Workflow.ValidationStatus != "pending" || state.Task.Workflow.ValidatedResultVersion != 0 {
		t.Fatal("old validation survived fixes")
	}
	p.Result = "Исправленный результат"
	change("save", p)
	p.Stage = "validation"
	change("submit_result", p)
	p = state.Task.Workflow.Progress
	p.Validation = "Повторные проверки пройдены"
	change("record_validation", p)
	p.Stage = "done"
	change("complete", p)
	if state.Task.Workflow.Stage != "done" || state.Task.Workflow.ValidatedResultVersion != state.Task.Workflow.ResultVersion {
		t.Fatal("incorrect completion")
	}
	if _, err := store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: "pause"}); !errors.Is(err, ErrConflict) {
		t.Fatal("completed task paused")
	}
}

func TestWorkflowGuardsAndUnconfirmedProposal(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	owner := "00112233445566778899aabbccddeeff"
	state, _ := store.Get(owner)
	for _, stage := range []string{"execution", "validation", "done", "bogus"} {
		p := state.Task.Workflow.Progress
		p.Stage = stage
		after, err := store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: 1, Action: "save", Progress: p})
		if !errors.Is(err, ErrInvalid) || after.Task.Workflow.Stage != "planning" || after.Task.Workflow.Version != 1 {
			t.Fatalf("accepted stage %s: %v", stage, err)
		}
		if !after.Task.Workflow.Events[len(after.Task.Workflow.Events)-1].Rejected {
			t.Fatal("rejection not audited")
		}
	}
	p := state.Task.Workflow.Progress
	p.Stage = "execution"
	p.Plan = "Реализовать и проверить"
	state, err := store.RecordTaskTurn(owner, state.Task.ID, 1, "Goal", "Plan", &TaskProposal{Progress: p, Reason: "Plan ready"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Task.Workflow.Stage != "planning" || state.Task.Workflow.ApprovedPlanVersion != 0 {
		t.Fatal("proposal applied")
	}
	if _, err := store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: 2, Action: "accept"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("generic acceptance bypassed approval")
	}
	if _, err := store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: 1, Action: "approve_plan", Progress: p}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale approval accepted")
	}
	state, err = store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: 2, Action: "approve_plan", Progress: p})
	if err != nil {
		t.Fatal(err)
	}
	if state.Task.Workflow.ApprovedPlanVersion != state.Task.Workflow.PlanVersion || state.Task.Workflow.PlanVersion == 0 {
		t.Fatal("missing plan approval")
	}
	p.Stage = "validation"
	if _, err := PreviewWorkflow(state.Task.Workflow, WorkflowCommand{Action: "submit_result", Progress: p}); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing result allowed")
	}
	p = state.Task.Workflow.Progress
	p.Plan = "Другой план"
	if _, err := PreviewWorkflow(state.Task.Workflow, WorkflowCommand{Action: "save", Progress: p}); !errors.Is(err, ErrInvalid) {
		t.Fatal("approved plan changed")
	}
	p.Stage = "planning"
	next, err := PreviewWorkflow(state.Task.Workflow, WorkflowCommand{Action: "replan", Progress: p})
	if err != nil || next.ApprovedPlanVersion != 0 {
		t.Fatal("replan did not reset approval")
	}
	if _, err := store.RecordTaskTurn(owner, state.Task.ID, 2, "stale", "answer", nil); !errors.Is(err, ErrConflict) {
		t.Fatal("stale reply overwrote checkpoint")
	}
}

func TestWorkflowTransitionMatrixAndValidationGuards(t *testing.T) {
	targets := map[string]string{"approve_plan": "execution", "submit_result": "validation", "request_changes": "execution", "replan": "planning", "complete": "done", "record_validation": "validation"}
	allowed := map[string]map[string]bool{
		"planning": {"approve_plan": true}, "execution": {"submit_result": true, "replan": true},
		"validation": {"request_changes": true, "replan": true, "complete": true, "record_validation": true}, "done": {},
	}
	for from, actions := range allowed {
		for action, target := range targets {
			t.Run(from+"/"+action, func(t *testing.T) {
				w := initialWorkflow("Goal")
				w.Stage = from
				w.Plan = "Plan"
				w.PlanVersion = 1
				w.ApprovedPlanVersion = 1
				w.Result = "Result"
				w.ResultVersion = 2
				w.Validation = "Evidence"
				w.ValidationStatus = "passed"
				w.ValidatedResultVersion = 2
				p := w.Progress
				p.Stage = target
				_, err := PreviewWorkflow(w, WorkflowCommand{Action: action, Progress: p, ValidationStatus: "passed", ValidationBasis: "user_report"})
				if (err == nil) != actions[action] {
					t.Fatalf("unexpected transition: %v", err)
				}
			})
		}
	}
	w := initialWorkflow("Goal")
	w.Stage = "validation"
	w.Plan = "Plan"
	w.PlanVersion = 1
	w.ApprovedPlanVersion = 1
	w.Result = "Result"
	w.ResultVersion = 2
	w.Validation = "Text alone is not verification"
	p := w.Progress
	p.Stage = "done"
	for _, status := range []string{"", "pending", "failed", "passed"} {
		w.ValidationStatus = status
		w.ValidatedResultVersion = 1
		if _, err := PreviewWorkflow(w, WorkflowCommand{Action: "complete", Progress: p}); err == nil {
			t.Fatal("stale or missing validation allowed completion")
		}
	}
	w.ValidatedResultVersion = 2
	p.Result = "Changed result"
	if _, err := PreviewWorkflow(w, WorkflowCommand{Action: "complete", Progress: p}); err == nil {
		t.Fatal("changed result reused validation")
	}
	p = w.Progress
	p.Validation = "Changed verification"
	next, err := PreviewWorkflow(w, WorkflowCommand{Action: "save", Progress: p})
	if err != nil || next.ValidationStatus != "pending" {
		t.Fatal("edited validation kept approval")
	}
	w.Stage = "execution"
	w.ApprovedPlanVersion = 0
	p = w.Progress
	p.Stage = "validation"
	if _, err := PreviewWorkflow(w, WorkflowCommand{Action: "submit_result", Progress: p}); err == nil {
		t.Fatal("legacy task bypassed plan approval")
	}
}

func TestLegacyTaskWorkflowMigration(t *testing.T) {
	root := t.TempDir()
	store, _ := NewStore(root)
	owner := "00112233445566778899aabbccddeeff"
	state, _ := store.Get(owner)
	other, _ := store.NewTask(owner, state.Task.ID, "Other")
	var revision string
	if err := readJSON(filepath.Join(root, owner, "CURRENT.json"), &revision); err != nil {
		t.Fatal(err)
	}
	// Remove workflow from both legacy task files without changing IDs or memory.
	if err := os.WriteFile(filepath.Join(root, owner, revision, "task.json"), []byte(`{"id":"`+other.Task.ID+`","name":"Other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, owner, revision, "tasks.json"), []byte(`[{"task":{"id":"`+owner+`","name":"Legacy"},"working":[],"proposals":[]}]`), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := store.Get(owner)
	if err != nil {
		t.Fatal(err)
	}
	if state.Task.Workflow.Stage != "planning" || state.Archived[0].Task.Workflow.Goal != "Legacy" {
		t.Fatal("legacy task migration failed")
	}
}

func TestTaskProposalCannotRewriteApprovedArtifacts(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	owner := "00112233445566778899aabbccddeeff"
	state, _ := store.Get(owner)
	p := state.Task.Workflow.Progress
	p.Plan = "Утверждённый план"
	p.Stage = "execution"
	state, err := store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: "approve_plan", Progress: p})
	if err != nil {
		t.Fatal(err)
	}
	p.Stage = "validation"
	p.Result = "Результат для проверки"
	state, err = store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: "submit_result", Progress: p})
	if err != nil {
		t.Fatal(err)
	}
	proposed := state.Task.Workflow.Progress
	proposed.Stage = "execution"
	proposed.Plan = "Перефразированный план"
	proposed.Goal = "Перефразированная цель"
	proposed.Result = "Неподтверждённое исправление"
	state, err = store.RecordTaskTurn(owner, state.Task.ID, state.Task.Workflow.Version, "Проверь", "Нужны исправления", &TaskProposal{Progress: proposed, Reason: "Найден дефект"})
	if err != nil {
		t.Fatal(err)
	}
	got := state.Task.Workflow.Proposal.Progress
	if got.Plan != p.Plan || got.Goal != p.Goal || got.Result != p.Result {
		t.Fatal("model overwrote confirmed artifacts")
	}
}
