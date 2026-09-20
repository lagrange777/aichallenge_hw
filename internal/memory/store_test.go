package memory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const owner = "00112233445566778899aabbccddeeff"

func TestSeparateLayersReviewLifecycleAndIsolation(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Get(owner)
	if err != nil {
		t.Fatal(err)
	}
	task := state.Task.ID
	state, err = store.Propose(owner, task, "user message", []Proposal{
		{Layer: Working, Key: "goal", Value: "Build an API", Reason: "Current task"},
		{Layer: Working, Key: "language", Value: "Russian", Reason: "User preference"},
		{Layer: LongTerm, Key: "guess", Value: "Unconfirmed guess", Reason: "Uncertain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Working) != 0 || len(state.LongTerm) != 0 {
		t.Fatal("proposals entered confirmed layers")
	}
	ids := []string{state.Proposals[0].ID, state.Proposals[1].ID, state.Proposals[2].ID}
	if _, err = store.Review(owner, ids[0], task, "accept", Working, "goal", "Build a Go API"); err != nil {
		t.Fatal(err)
	}
	// The reviewer changes both the destination and text proposed by the model.
	state, err = store.Review(owner, ids[1], task, "accept", LongTerm, "language", "English")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Working) != 1 || state.Working[0].Value != "Build a Go API" || len(state.LongTerm) != 1 || state.LongTerm[0].Value != "English" {
		t.Fatalf("state = %#v", state)
	}
	if _, err = store.Review(owner, ids[2], task, "reject", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Review(owner, ids[2], task, "accept", LongTerm, "guess", "guess"); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-review: %v", err)
	}
	restarted, _ := NewStore(root)
	state, err = restarted.Get(owner)
	if err != nil {
		t.Fatal(err)
	}
	if state.Proposals[2].Status != "rejected" || state.Proposals[0].ReviewedAt == nil {
		t.Fatal("review decisions not durable")
	}
	var revision string
	if err = readJSON(filepath.Join(root, owner, "CURRENT.json"), &revision); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"working.json", "long_term.json", "proposals.json", "task.json"} {
		info, err := os.Stat(filepath.Join(root, owner, revision, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions = %v", name, info.Mode())
		}
	}
	other, err := restarted.Get("11112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Working)+len(other.LongTerm)+len(other.Proposals) != 0 {
		t.Fatal("profile leakage")
	}
	state, err = restarted.NewTask(owner, task, "Next task")
	if err != nil {
		t.Fatal(err)
	}
	if state.Task.ID == task || len(state.Working) != 0 || len(state.Proposals) != 0 || len(state.LongTerm) != 1 {
		t.Fatal("new task did not isolate task memory")
	}
	if _, err = restarted.Edit(owner, task, ids[1], LongTerm, "language", "German", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale task edit = %v", err)
	}
	state, err = restarted.Edit(owner, state.Task.ID, ids[1], LongTerm, "language", "German", false)
	if err != nil {
		t.Fatal(err)
	}
	if state.LongTerm[0].Value != "German" {
		t.Fatal("edit failed")
	}
	state, err = restarted.Edit(owner, state.Task.ID, ids[1], LongTerm, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LongTerm) != 0 {
		t.Fatal("delete failed")
	}
}
func TestInvalidBatchDoesNotPartiallySave(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	state, _ := store.Get(owner)
	_, err := store.Propose(owner, state.Task.ID, "source", []Proposal{
		{Layer: Working, Key: "goal", Value: "valid", Reason: "task"},
		{Layer: "short_term", Key: "invalid", Value: "invalid", Reason: "bad layer"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v", err)
	}
	state, _ = store.Get(owner)
	if len(state.Proposals) != 0 {
		t.Fatal("partial transaction was saved")
	}
	if _, err = store.Get("../../escape"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unsafe owner accepted")
	}
}

func TestConfirmationFailureLeavesProposalPending(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Get(owner)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.Propose(owner, state.Task.ID, "source", []Proposal{{Layer: Working, Key: "goal", Value: "Build", Reason: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Review(owner, state.Proposals[0].ID, state.Task.ID, "accept", LongTerm, "", "value"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid acceptance: %v", err)
	}
	state, err = store.Get(owner)
	if err != nil {
		t.Fatal(err)
	}
	if state.Proposals[0].Status != "pending" || len(state.Working)+len(state.LongTerm) != 0 {
		t.Fatal("invalid review partially committed")
	}
}
