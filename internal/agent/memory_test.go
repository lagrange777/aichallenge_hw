package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"codex-chat-cli/internal/memory"
)

const profileID = "00112233445566778899aabbccddeeff"

type layeredLLM struct {
	requests        []CompletionRequest
	extractionFails bool
	invalid         bool
}

func (f *layeredLLM) Complete(_ context.Context, r CompletionRequest) (CompletionResponse, error) {
	f.requests = append(f.requests, r)
	if r.Instructions == proposalInstructions {
		if f.extractionFails {
			return CompletionResponse{}, errors.New("extractor unavailable")
		}
		output := `{"proposals":[{"layer":"working","key":"goal","value":"Go API","reason":"Current task"},{"layer":"long_term","key":"language","value":"Russian","reason":"Enduring preference"},{"layer":"long_term","key":"guess","value":"unverified","reason":"Review me"}]}`
		if f.invalid {
			output = "not JSON"
		}
		return CompletionResponse{ResponseID: "proposal", Output: output, Usage: Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}}, nil
	}
	answer := "No saved preference or task"
	for _, m := range r.History {
		if m.Role == "developer" && strings.Contains(m.Content, `"language":"Russian"`) {
			answer = "Отвечаю по-русски"
		}
		if m.Role == "developer" && strings.Contains(m.Content, `"goal":"Go API"`) {
			answer += "; задача: Go API"
		}
		if m.Role == "developer" && strings.Contains(m.Content, "unverified") {
			return CompletionResponse{}, errors.New("unreviewed memory leaked")
		}
	}
	return CompletionResponse{ResponseID: "answer", Output: answer, Usage: Usage{InputTokens: 20, OutputTokens: 5, TotalTokens: 25}}, nil
}
func TestMemoryLayersAffectAnswersOnlyAfterReview(t *testing.T) {
	root := t.TempDir()
	store, _ := memory.NewStore(root)
	history := &memoryHistory{}
	llm := &layeredLLM{}
	a, err := NewPersistent(llm, "gpt-5.3-codex", profileID, history, WithMemory(store), WithContextStrategy(StrategyConfig{Type: StrategySlidingWindow, KeepLast: 1}))
	if err != nil {
		t.Fatal(err)
	}
	ask := func(a *Agent, text string) Response {
		t.Helper()
		r, err := a.Ask(context.Background(), Request{Message: text})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := ask(a, "Remember my preferences")
	if first.Text != "No saved preference or task" || first.Usage.TotalTokens != 35 || first.TokenMetrics.ProposalTokens != 10 {
		t.Fatalf("first response = %#v", first)
	}
	view, _ := a.Memories()
	if len(view.Working) != 0 || len(view.LongTerm) != 0 || len(view.Proposals) != 3 {
		t.Fatalf("unreviewed state = %#v", view)
	}
	task := view.Task.ID
	ask(a, "Check again before confirmation")
	for _, p := range view.Proposals {
		action := "accept"
		if p.Key == "guess" {
			action = "reject"
		}
		if _, err = a.ReviewMemory(p.ID, task, action, p.Layer, p.Key, p.Value); err != nil {
			t.Fatal(err)
		}
	}
	result := ask(a, "What do you know?")
	if result.Text != "Отвечаю по-русски; задача: Go API" {
		t.Fatalf("after review = %q", result.Text)
	}
	last := llm.requests[len(llm.requests)-2]
	if len(last.History) != 2 || last.History[0].Role != "developer" || last.PreviousResponseID != "" {
		t.Fatalf("context = %#v", last)
	}
	if err = a.Reset(); err != nil {
		t.Fatal(err)
	}
	view, _ = a.Memories()
	if view.ShortTermMessages != 0 || len(view.Working) != 1 || len(view.LongTerm) != 1 {
		t.Fatal("reset erased task/profile")
	}
	restartedStore, _ := memory.NewStore(root)
	restarted, err := NewPersistent(llm, "gpt-5.3-codex", profileID, history, WithMemory(restartedStore))
	if err != nil {
		t.Fatal(err)
	}
	if result = ask(restarted, "After restart"); result.Text != "Отвечаю по-русски; задача: Go API" {
		t.Fatalf("restart = %q", result.Text)
	}
	next, err := restarted.NewTask(task, "Other task")
	if err != nil {
		t.Fatal(err)
	}
	if next.ShortTermMessages != 0 || len(next.Working) != 0 || len(next.LongTerm) != 1 {
		t.Fatal("new task lifecycle broken")
	}
	if result = ask(restarted, "New task"); result.Text != "Отвечаю по-русски" {
		t.Fatalf("old task leaked: %q", result.Text)
	}
	if _, err = restarted.EditMemory(next.Task.ID, next.LongTerm[0].ID, memory.LongTerm, "", "", true); err != nil {
		t.Fatal(err)
	}
	if result = ask(restarted, "After deletion"); result.Text != "No saved preference or task" {
		t.Fatalf("deleted layer entry leaked: %q", result.Text)
	}
}
func TestMemoryExtractionFailureKeepsAnswerAndCountsInvalidOutput(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "transport", true: "invalid JSON"}[invalid], func(t *testing.T) {
			store, _ := memory.NewStore(t.TempDir())
			llm := &layeredLLM{extractionFails: !invalid, invalid: invalid}
			a, _ := NewPersistent(llm, "gpt-5.3-codex", profileID, &memoryHistory{}, WithMemory(store))
			result, err := a.Ask(context.Background(), Request{Message: "hello"})
			if err != nil {
				t.Fatal(err)
			}
			if len(a.Messages()) != 2 || result.Text == "" || result.TokenMetrics.ContextWarning == "" {
				t.Fatal("answer lost on extraction failure")
			}
			view, _ := a.Memories()
			if len(view.Proposals) != 0 {
				t.Fatal("invalid proposal persisted")
			}
			if invalid && result.Usage.TotalTokens != 35 {
				t.Fatal("invalid JSON usage not accounted")
			}
		})
	}
}
func TestMemoryBranchingReplaysCurrentLayersAfterDeletionAndRestart(t *testing.T) {
	store, _ := memory.NewStore(t.TempDir())
	history := &memoryHistory{}
	llm := &layeredLLM{}
	a, _ := NewPersistent(llm, "gpt-5.3-codex", profileID, history, WithMemory(store), WithContextStrategy(StrategyConfig{Type: StrategyBranching}))
	if _, err := a.Ask(context.Background(), Request{Message: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateBranches(); err != nil {
		t.Fatal(err)
	}
	b, err := NewPersistent(llm, "gpt-5.3-codex", profileID, history, WithMemory(store))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Messages()) != 2 || b.Snapshot().ActiveBranchID != "branch-a" {
		t.Fatal("branch lost on memory restore")
	}
	if _, err = b.Ask(context.Background(), Request{Message: "branch"}); err != nil {
		t.Fatal(err)
	}
	if llm.requests[len(llm.requests)-2].PreviousResponseID != "" {
		t.Fatal("hidden API chain retained stale memory")
	}
}

type failingSaveHistory struct {
	memoryHistory
	fail bool
}

func (h *failingSaveHistory) Save(id string, state ConversationState) error {
	if h.fail {
		return errors.New("disk error")
	}
	return h.memoryHistory.Save(id, state)
}
func TestTaskChangeFailurePreservesCurrentTask(t *testing.T) {
	store, _ := memory.NewStore(t.TempDir())
	hist := &failingSaveHistory{}
	a, err := NewPersistent(&layeredLLM{}, "gpt-5.3-codex", profileID, hist, WithMemory(store))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Ask(context.Background(), Request{Message: "original task"}); err != nil {
		t.Fatal(err)
	}
	original, _ := a.Memories()
	second, err := a.NewTask(original.Task.ID, "Second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Ask(context.Background(), Request{Message: "second task"}); err != nil {
		t.Fatal(err)
	}
	hist.fail = true
	if _, err = a.NewTask(second.Task.ID, "Third"); err == nil {
		t.Fatal("expected save error")
	}
	if _, err = a.SwitchTask(second.Task.ID, original.Task.ID); err == nil {
		t.Fatal("expected save error")
	}
	if err = a.Reset(); err == nil {
		t.Fatal("expected save error")
	}
	view, _ := a.Memories()
	if view.Task.ID != second.Task.ID || len(view.Tasks) != 2 || len(a.Messages()) != 2 {
		t.Fatal("failed operation changed active task")
	}
	restored, err := NewPersistent(&layeredLLM{}, "gpt-5.3-codex", profileID, hist, WithMemory(store))
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Messages()) != 2 || restored.Messages()[0].Text != "second task" {
		t.Fatal("active task did not survive restart")
	}
}

func TestSaveMessagesDirectlyToMemory(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hist := &memoryHistory{}
	llm := &layeredLLM{}
	a, err := NewPersistent(llm, "gpt-5.3-codex", profileID, hist, WithMemory(store))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Ask(context.Background(), Request{Message: "original user message"}); err != nil {
		t.Fatal(err)
	}
	messages := a.Messages()
	view, err := a.Memories()
	if err != nil {
		t.Fatal(err)
	}
	calls := len(llm.requests)
	if messages[0].ID == "" || messages[1].ID == "" || messages[0].ID == messages[1].ID {
		t.Fatal("missing stable message references")
	}
	state, err := a.SaveMessageMemory(view.Task.ID, messages[0].ID, memory.Working, "goal", "Go API")
	if err != nil {
		t.Fatal(err)
	}
	state, err = a.SaveMessageMemory(view.Task.ID, messages[1].ID, memory.LongTerm, "language", "Russian")
	if err != nil {
		t.Fatal(err)
	}
	if len(llm.requests) != calls {
		t.Fatal("manual memory save made an LLM call")
	}
	if len(state.Working) != 1 || state.Working[0].MessageID != messages[0].ID || !strings.Contains(state.Working[0].Source, "original user message") {
		t.Fatalf("working memory = %#v", state.Working)
	}
	if len(state.LongTerm) != 1 || !strings.HasPrefix(state.LongTerm[0].Source, "Ассистент:") {
		t.Fatalf("long-term memory = %#v", state.LongTerm)
	}
	if _, err = a.SaveMessageMemory(view.Task.ID, messages[0].ID, memory.Working, "goal", "Go API"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.SaveMessageMemory(view.Task.ID, "not-a-message", memory.LongTerm, "forged", "value"); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("unknown message accepted: %v", err)
	}
	restored, err := NewPersistent(llm, "gpt-5.3-codex", profileID, hist, WithMemory(store))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Messages()[0].ID != messages[0].ID {
		t.Fatal("message reference changed after restart")
	}
	result, err := restored.Ask(context.Background(), Request{Message: "check saved memory"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Отвечаю по-русски; задача: Go API" {
		t.Fatalf("manually saved facts not used: %s", result.Text)
	}
	if err = restored.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err = restored.SaveMessageMemory(view.Task.ID, messages[0].ID, memory.Working, "stale", "value"); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("deleted conversation source accepted: %v", err)
	}
}
