package rag

import (
	"codex-chat-cli/internal/agent"
	"encoding/json"
	"os"
	"testing"
)

func TestDialogueScenariosHaveLongMemoryAndCorrections(t *testing.T) {
	data, err := os.ReadFile("../../documents/rag25-scenarios.json")
	if err != nil {
		t.Fatal(err)
	}
	var qs []DialogueScenario
	if err = json.Unmarshal(data, &qs); err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 {
		t.Fatal("expected two scenarios")
	}
	for _, q := range qs {
		if len(q.Turns) != 12 || q.Goal == "" {
			t.Fatal(q.ID)
		}
		for _, turn := range q.Turns {
			if turn.Query == "" || len(turn.Expectations) == 0 || len(turn.ExpectedMemory) == 0 {
				t.Fatal("incomplete turn")
			}
		}
		if len(q.Turns[6].ForbiddenMemory) == 0 || len(q.Turns[9].Sources) != 1 || q.Turns[9].Sources[0] != "user" {
			t.Fatal("missing correction / long range recall")
		}
	}
}
func TestMemoryEvaluationRejectsStaleConstraints(t *testing.T) {
	s := &agent.DialogueState{Entries: []agent.DialogueEntry{{Text: "Goal. Run locally."}}}
	turn := DialogueTurn{ExpectedMemory: []string{"Goal", "locally"}, ForbiddenMemory: []string{"Docker"}}
	if !CheckDialogueMemory(s, turn) {
		t.Fatal("expected match")
	}
	s.Entries = append(s.Entries, agent.DialogueEntry{Text: "Docker"})
	if CheckDialogueMemory(s, turn) {
		t.Fatal("stale constraint accepted")
	}
}
