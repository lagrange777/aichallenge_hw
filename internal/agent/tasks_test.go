package agent

import (
	"context"
	"errors"
	"testing"

	"codex-chat-cli/internal/memory"
)

func TestTasksRestoreHistoryLayersAndStrategy(t *testing.T) {
	root := t.TempDir()
	store, err := memory.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	hist := &memoryHistory{}
	llm := &layeredLLM{}
	a, err := NewPersistent(llm, "gpt-5.3-codex", profileID, hist, WithMemory(store))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Ask(context.Background(), Request{Message: "first task", ContextStrategy: "branching"}); err != nil {
		t.Fatal(err)
	}
	firstMessages := a.Messages()
	first, _ := a.Memories()
	for _, p := range first.Proposals[:2] {
		if _, err = a.ReviewMemory(p.ID, first.Task.ID, "accept", p.Layer, p.Key, p.Value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = a.CreateBranches(); err != nil {
		t.Fatal(err)
	}
	second, err := a.NewTask(first.Task.ID, "Second task")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Tasks) != 2 || len(second.Working) != 0 || len(second.Proposals) != 0 || len(a.Messages()) != 0 || len(second.LongTerm) != 1 {
		t.Fatal("new task isolation failed")
	}
	if _, err = a.Ask(context.Background(), Request{Message: "second task", ContextStrategy: "none"}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.SaveMessageMemory(second.Task.ID, a.Messages()[0].ID, memory.Working, "goal", "Other goal"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.EditMemory(second.Task.ID, second.LongTerm[0].ID, memory.LongTerm, "language", "English", false); err != nil {
		t.Fatal(err)
	}
	view, err := a.SwitchTask(second.Task.ID, first.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Messages()) != 2 || a.Messages()[0].ID != firstMessages[0].ID || len(view.Proposals) != 3 || view.Working[0].Value != "Go API" || view.LongTerm[0].Value != "English" {
		t.Fatal("task layers/history were not restored or profile was rolled back")
	}
	if a.Snapshot().Strategy.Type != StrategyBranching || a.Snapshot().ActiveBranchID != "branch-a" {
		t.Fatal("branch state was not restored")
	}
	if _, err = a.SwitchTask(second.Task.ID, first.Task.ID); !errors.Is(err, memory.ErrConflict) {
		t.Fatal("stale switch accepted")
	}
	if _, err = a.SwitchTask(first.Task.ID, "11112233445566778899aabbccddeeff"); !errors.Is(err, memory.ErrConflict) {
		t.Fatal("unknown task accepted")
	}
	store, err = memory.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err = NewPersistent(llm, "gpt-5.3-codex", profileID, hist, WithMemory(store))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Messages()) != 2 || a.Messages()[0].Text != "first task" {
		t.Fatal("active task lost on restart")
	}
	view, err = a.SwitchTask(first.Task.ID, second.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Messages()[0].Text != "second task" || view.Working[0].Value != "Other goal" || a.Snapshot().Strategy.Type != StrategyNone {
		t.Fatal("second task was overwritten")
	}
	if err = a.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.SwitchTask(second.Task.ID, first.Task.ID); err != nil {
		t.Fatal(err)
	}
	if len(a.Messages()) != 2 {
		t.Fatal("reset erased another task")
	}
	if _, err = a.SwitchTask(first.Task.ID, second.Task.ID); err != nil {
		t.Fatal(err)
	}
	if len(a.Messages()) != 0 {
		t.Fatal("reset transcript reappeared")
	}
}

func TestLegacyTaskHistoryIsPreservedAndResetDoesNotResurrectIt(t *testing.T) {
	store, _ := memory.NewStore(t.TempDir())
	state, _ := store.Get(profileID)
	// Simulate a pre-upgrade task whose ID differs from its profile ID.
	state, err := store.NewTask(profileID, state.Task.ID, "Existing task")
	if err != nil {
		t.Fatal(err)
	}
	hist := &memoryHistory{}
	if err = hist.Save(profileID, ConversationState{MemoryTaskID: state.Task.ID, Messages: []Message{{Role: "user", Text: "legacy message"}}}); err != nil {
		t.Fatal(err)
	}
	a, err := NewPersistent(&layeredLLM{}, "gpt-5.3-codex", profileID, hist, WithMemory(store))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Messages()) != 1 {
		t.Fatal("legacy conversation was lost")
	}
	other, err := a.NewTask(state.Task.ID, "New task")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SwitchTask(other.Task.ID, state.Task.ID); err != nil {
		t.Fatal(err)
	}
	if len(a.Messages()) != 1 {
		t.Fatal("legacy conversation was not archived")
	}
	if err = a.Reset(); err != nil {
		t.Fatal(err)
	}
	a, err = NewPersistent(&layeredLLM{}, "gpt-5.3-codex", profileID, hist, WithMemory(store))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Messages()) != 0 {
		t.Fatal("legacy fallback resurrected cleared history")
	}
}
