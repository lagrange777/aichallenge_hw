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
	state, err := store.Get(owner)
	if err != nil {
		t.Fatal(err)
	}
	change := func(action string, p Progress) {
		t.Helper()
		state, err = store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: action, Progress: p})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, stage := range []string{"planning", "execution", "validation"} {
		p := state.Task.Workflow.Progress
		p.Stage = stage
		p.CurrentStep = "Следующий шаг"
		p.Completed = "Схема согласована"
		p.Result = "Результат"
		change("save", p)
		change("pause", Progress{})
		store, _ = NewStore(root)
		state, err = store.Get(owner)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Task.Workflow.Paused || state.Task.Workflow.Stage != stage || state.Task.Workflow.Completed != p.Completed {
			t.Fatal("pause checkpoint lost")
		}
		if _, err = store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: "save", Progress: p}); !errors.Is(err, ErrConflict) {
			t.Fatal("paused task edited")
		}
		other, err := store.NewTask(owner, state.Task.ID, "Another task")
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
	p.Stage = "execution"
	change("save", p)
	p.Stage = "validation"
	change("save", p)
	p.Stage = "done"
	p.Validation = "Пользователь подтвердил прохождение проверок"
	change("save", p)
	if state.Task.Workflow.Stage != "done" || len(state.Task.Workflow.Events) != 12 {
		t.Fatalf("final state: %+v", state.Task.Workflow)
	}
	if _, err = store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: state.Task.Workflow.Version, Action: "pause"}); !errors.Is(err, ErrConflict) {
		t.Fatal("completed task paused")
	}
}

func TestWorkflowGuardsAndUnconfirmedProposal(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	owner := "00112233445566778899aabbccddeeff"
	state, _ := store.Get(owner)
	p := state.Task.Workflow.Progress
	for _, stage := range []string{"validation", "done", "bogus"} {
		p.Stage = stage
		if _, err := store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: 1, Action: "save", Progress: p}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid stage %s: %v", stage, err)
		}
	}
	p.Stage = "execution"
	proposal := &TaskProposal{Progress: p, Reason: "Plan agreed"}
	state, err := store.RecordTaskTurn(owner, state.Task.ID, 1, "Goal", "Plan", proposal)
	if err != nil {
		t.Fatal(err)
	}
	if state.Task.Workflow.Stage != "planning" || state.Task.Workflow.Proposal == nil {
		t.Fatal("proposal applied automatically")
	}
	if _, err = store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: 1, Action: "accept"}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale approval accepted")
	}
	state, err = store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: 2, Action: "accept"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Task.Workflow.Stage != "execution" || state.Task.Workflow.LastTurn.Assistant != "Plan" {
		t.Fatal("checkpoint incorrect")
	}
	p.Stage = "validation"
	if err = ValidateProgress("execution", p); err == nil {
		t.Fatal("missing result allowed")
	}
	p.Result = "Code"
	p.Stage = "done"
	if err = ValidateProgress("validation", p); err == nil {
		t.Fatal("missing verification allowed")
	}
	if _, err = store.RecordTaskTurn(owner, state.Task.ID, 2, "stale", "answer", nil); !errors.Is(err, ErrConflict) {
		t.Fatal("stale answer overwrote checkpoint")
	}
	state, err = store.RecordTaskTurn(owner, state.Task.ID, 3, "next", "reply", &TaskProposal{Progress: state.Task.Workflow.Progress, Reason: "Next step"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.UpdateWorkflow(owner, WorkflowCommand{TaskID: state.Task.ID, Version: 4, Action: "reject"})
	if err != nil || state.Task.Workflow.Proposal != nil || state.Task.Workflow.Stage != "execution" {
		t.Fatal("rejection changed stage")
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
