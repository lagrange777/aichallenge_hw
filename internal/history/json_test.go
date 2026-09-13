package history

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"codex-chat-cli/internal/agent"
)

func TestJSONStoreSurvivesNewInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "history.json")
	store, err := NewJSONStore(path)
	if err != nil {
		t.Fatalf("NewJSONStore() error = %v", err)
	}
	cost := 0.0012
	want := agent.ConversationState{
		PreviousResponseID: "resp_1",
		ActiveModel:        "test-model",
		Messages: []agent.Message{
			{Role: "user", Text: "Меня зовут Мила", Time: 1},
			{Role: "assistant", Text: "Запомнил", Time: 2, Model: "test-model", Metrics: &agent.MessageMetrics{DurationMS: 15, TotalTokens: 12, CostUSD: &cost}},
		},
		UpdatedAt: time.Date(2026, time.September, 13, 10, 0, 0, 0, time.UTC),
	}
	if err := store.Save("conversation-a", want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	restartedStore, err := NewJSONStore(path)
	if err != nil {
		t.Fatalf("NewJSONStore() after restart error = %v", err)
	}
	got, err := restartedStore.Load("conversation-a")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestJSONStoreKeepsConversationsIsolatedAndDeletesOne(t *testing.T) {
	store, err := NewJSONStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatalf("NewJSONStore() error = %v", err)
	}
	if err := store.Save("one", agent.ConversationState{Messages: []agent.Message{{Role: "user", Text: "one"}}}); err != nil {
		t.Fatalf("Save(one) error = %v", err)
	}
	if err := store.Save("two", agent.ConversationState{Messages: []agent.Message{{Role: "user", Text: "two"}}}); err != nil {
		t.Fatalf("Save(two) error = %v", err)
	}
	if err := store.Delete("one"); err != nil {
		t.Fatalf("Delete(one) error = %v", err)
	}
	if _, err := store.Load("one"); !errors.Is(err, agent.ErrHistoryNotFound) {
		t.Fatalf("Load(one) error = %v, want ErrHistoryNotFound", err)
	}
	state, err := store.Load("two")
	if err != nil {
		t.Fatalf("Load(two) error = %v", err)
	}
	if len(state.Messages) != 1 || state.Messages[0].Text != "two" {
		t.Fatalf("Load(two) = %#v", state)
	}
}
