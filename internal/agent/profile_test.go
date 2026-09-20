package agent

import (
	"context"
	"errors"
	"testing"

	"codex-chat-cli/internal/memory"
	"codex-chat-cli/internal/profile"
)

func TestProfileIsolationSnapshotsAndRestart(t *testing.T) {
	profiles, _ := profile.NewStore(t.TempDir())
	layers, _ := memory.NewStore(t.TempDir())
	hist := &memoryHistory{}
	llm := &layeredLLM{}
	// A legacy transcript already belongs to the browser's original identity.
	hist.Save(profileID, ConversationState{Messages: []Message{{Role: "user", Text: "legacy"}}})
	a, err := NewPersistent(llm, "gpt-5.3-codex", profileID, hist, WithMemory(layers), WithProfiles(profiles))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Messages()) != 1 {
		t.Fatal("migration lost legacy conversation")
	}
	view, _ := a.Profiles()
	p := view.Active()
	p.Language = "ru"
	if _, err = a.SaveProfile(profileID, p, false); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Ask(context.Background(), Request{Message: "hello", ProfileID: profileID}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.SaveMessageMemory(view.Memory.Task.ID, a.Messages()[0].ID, memory.LongTerm, "private", "Only primary profile"); err != nil {
		t.Fatal(err)
	}
	firstSnapshot := a.Messages()[2].Profile
	view, err = a.SaveProfile(profileID, profile.Presets()[1], true)
	if err != nil {
		t.Fatal(err)
	}
	second := view.Profiles[1]
	view, err = a.SwitchProfile(profileID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) != 0 || len(view.Memory.LongTerm) != 0 || len(view.Memory.Tasks) != 1 {
		t.Fatal("profile data leaked")
	}
	if _, err = a.Ask(context.Background(), Request{Message: "stale", ProfileID: profileID}); !errors.Is(err, profile.ErrConflict) {
		t.Fatal("stale browser request accepted")
	}
	if _, err = a.Ask(context.Background(), Request{Message: "проверка первого ответа", ProfileID: second.ID}); err != nil {
		t.Fatal(err)
	}
	for _, request := range llm.requests[len(llm.requests)-2:] {
		if request.Profile == nil || request.Profile.Language != "en" || request.Profile.ID != second.ID {
			t.Fatal("profile missing from main or extraction request")
		}
		if request.Internal != (request.Instructions == proposalInstructions) {
			t.Fatal("wrong auxiliary mode")
		}
	}
	p = second
	p.Language = "ru"
	if _, err = a.SaveProfile(second.ID, p, false); err != nil {
		t.Fatal(err)
	}
	if a.Messages()[1].Profile.Language != "en" {
		t.Fatal("editing changed historic snapshot")
	}
	if err = a.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Ask(context.Background(), Request{Message: "after reset"}); err != nil {
		t.Fatal(err)
	}
	if a.Messages()[1].Profile.Language != "ru" {
		t.Fatal("edit not applied to first answer")
	}
	a, err = NewPersistent(llm, "gpt-5.3-codex", profileID, hist, WithMemory(layers), WithProfiles(profiles))
	if err != nil {
		t.Fatal(err)
	}
	if a.Messages()[0].Text != "after reset" {
		t.Fatal("active profile not restored")
	}
	view, err = a.SwitchProfile(second.ID, profileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) != 3 || len(view.Memory.LongTerm) != 1 || view.Messages[0].Text != "legacy" {
		t.Fatal("primary data not restored")
	}
	if firstSnapshot.Language != "ru" {
		t.Fatal("snapshot was mutated")
	}
}

func TestProfileSwitchSaveFailureKeepsIdentity(t *testing.T) {
	profiles, _ := profile.NewStore(t.TempDir())
	layers, _ := memory.NewStore(t.TempDir())
	hist := &failingSaveHistory{}
	a, _ := NewPersistent(&layeredLLM{}, "gpt-5.3-codex", profileID, hist, WithMemory(layers), WithProfiles(profiles))
	view, err := a.SaveProfile(profileID, profile.Presets()[0], true)
	if err != nil {
		t.Fatal(err)
	}
	hist.fail = true
	if _, err = a.SwitchProfile(profileID, view.Profiles[1].ID); err == nil {
		t.Fatal("expected failure")
	}
	view, err = a.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if view.ActiveID != profileID || a.conversationID != profileID {
		t.Fatal("failed switch changed profile")
	}
}
