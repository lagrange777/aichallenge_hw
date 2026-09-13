package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeLLM struct {
	requests  []CompletionRequest
	callCount int
	fail      bool
}

type countingLLM struct {
	fakeLLM
	counts        []TokenCounts
	countRequests []CompletionRequest
	countError    error
}

type compressionLLM struct {
	requests []CompletionRequest
	calls    int
}

func (f *compressionLLM) Complete(_ context.Context, request CompletionRequest) (CompletionResponse, error) {
	f.requests = append(f.requests, request)
	f.calls++
	output := "answer"
	if request.Instructions == summaryInstructions {
		output = "The user asked the first question and received an answer."
	}
	return CompletionResponse{
		ResponseID: "resp_" + string(rune('0'+f.calls)),
		Output:     output,
		Usage:      Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
	}, nil
}

func (f *compressionLLM) CountTokens(_ context.Context, request CompletionRequest) (TokenCounts, error) {
	projected := 10
	if len(request.History) > 0 && request.History[0].Role == "developer" {
		projected = 100
	} else if len(request.History) >= 4 {
		projected = 300
	} else if len(request.History) > 0 || request.PreviousResponseID != "" {
		projected = 50
	}
	return TokenCounts{CurrentRequestTokens: 10, ProjectedInputTokens: projected}, nil
}

type memoryHistory struct {
	state ConversationState
	saved bool
}

func (h *memoryHistory) Load(string) (ConversationState, error) {
	if !h.saved {
		return ConversationState{}, ErrHistoryNotFound
	}
	return h.state, nil
}

func (h *memoryHistory) Save(_ string, state ConversationState) error {
	h.state = state
	h.saved = true
	return nil
}

func (h *memoryHistory) Delete(string) error {
	h.state = ConversationState{}
	h.saved = false
	return nil
}

func (f *countingLLM) CountTokens(_ context.Context, request CompletionRequest) (TokenCounts, error) {
	f.countRequests = append(f.countRequests, request)
	if f.countError != nil {
		return TokenCounts{}, f.countError
	}
	index := len(f.countRequests) - 1
	if index >= len(f.counts) {
		return TokenCounts{}, errors.New("missing fake token count")
	}
	return f.counts[index], nil
}

func (f *fakeLLM) Complete(_ context.Context, request CompletionRequest) (CompletionResponse, error) {
	f.requests = append(f.requests, request)
	if f.fail {
		return CompletionResponse{}, errors.New("request failed")
	}
	f.callCount++
	return CompletionResponse{
		ResponseID: "resp_" + string(rune('0'+f.callCount)),
		Output:     "answer",
		Usage:      Usage{InputTokens: 100, OutputTokens: 20},
	}, nil
}

func TestAgentCarriesAndResetsResponseID(t *testing.T) {
	llm := &fakeLLM{}
	agent := New(llm, "model-a")

	if _, err := agent.Ask(context.Background(), Request{Message: "first"}); err != nil {
		t.Fatalf("first Ask() error = %v", err)
	}
	if _, err := agent.Ask(context.Background(), Request{Message: "second"}); err != nil {
		t.Fatalf("second Ask() error = %v", err)
	}
	if err := agent.Reset(); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if _, err := agent.Ask(context.Background(), Request{Message: "third"}); err != nil {
		t.Fatalf("third Ask() error = %v", err)
	}

	want := []string{"", "resp_1", ""}
	for index := range want {
		if llm.requests[index].PreviousResponseID != want[index] {
			t.Fatalf("previous response ID %d = %q, want %q", index, llm.requests[index].PreviousResponseID, want[index])
		}
	}
}

func TestAgentKeepsStateAfterError(t *testing.T) {
	llm := &fakeLLM{}
	agent := New(llm, "model-a")

	if _, err := agent.Ask(context.Background(), Request{Message: "first"}); err != nil {
		t.Fatalf("first Ask() error = %v", err)
	}
	llm.fail = true
	if _, err := agent.Ask(context.Background(), Request{Message: "second"}); err == nil {
		t.Fatal("second Ask() error = nil, want an error")
	}
	llm.fail = false
	if _, err := agent.Ask(context.Background(), Request{Message: "retry"}); err != nil {
		t.Fatalf("retry Ask() error = %v", err)
	}

	if got := llm.requests[2].PreviousResponseID; got != "resp_1" {
		t.Fatalf("previous response ID after error = %q, want resp_1", got)
	}
}

func TestAgentNormalizesRequestAndBuildsResponse(t *testing.T) {
	llm := &fakeLLM{}
	agent := New(llm, "gpt-5.3-codex")
	response, err := agent.Ask(context.Background(), Request{
		Message:             "  question  ",
		Format:              "  JSON  ",
		LengthLimit:         "  200 words ",
		CompletionCondition: " after summary  ",
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}

	request := llm.requests[0]
	if request.Input != "question" || request.Model != "gpt-5.3-codex" || request.Format != "JSON" || request.LengthLimit != "200 words" || request.CompletionCondition != "after summary" {
		t.Fatalf("completion request = %#v", request)
	}
	if response.Text != "answer" || response.Model != "gpt-5.3-codex" || response.Usage.TotalTokens != 120 || response.Duration <= 0 || response.CostUSD == nil {
		t.Fatalf("agent response = %#v", response)
	}
}

func TestAgentReplaysHistoryAfterModelChange(t *testing.T) {
	llm := &fakeLLM{}
	agent := New(llm, "model-a")

	if _, err := agent.Ask(context.Background(), Request{Message: "first", Model: "model-a"}); err != nil {
		t.Fatalf("first Ask() error = %v", err)
	}
	if _, err := agent.Ask(context.Background(), Request{Message: "second", Model: "model-a"}); err != nil {
		t.Fatalf("second Ask() error = %v", err)
	}
	if _, err := agent.Ask(context.Background(), Request{Message: "third", Model: "model-b"}); err != nil {
		t.Fatalf("third Ask() error = %v", err)
	}
	if _, err := agent.Ask(context.Background(), Request{Message: "fourth", Model: "model-b"}); err != nil {
		t.Fatalf("fourth Ask() error = %v", err)
	}

	want := []string{"", "resp_1", "", "resp_3"}
	for index := range want {
		if llm.requests[index].PreviousResponseID != want[index] {
			t.Fatalf("previous response ID %d = %q, want %q", index, llm.requests[index].PreviousResponseID, want[index])
		}
	}
	wantHistory := []ContextMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "second"},
		{Role: "assistant", Content: "answer"},
	}
	if len(llm.requests[2].History) != len(wantHistory) {
		t.Fatalf("replayed history = %#v, want %#v", llm.requests[2].History, wantHistory)
	}
	for index := range wantHistory {
		if llm.requests[2].History[index] != wantHistory[index] {
			t.Fatalf("replayed history[%d] = %#v, want %#v", index, llm.requests[2].History[index], wantHistory[index])
		}
	}
	if len(llm.requests[3].History) != 0 {
		t.Fatalf("history was replayed again instead of continuing the new chain: %#v", llm.requests[3].History)
	}
}

func TestAgentRejectsEmptyMessageBeforeCallingLLM(t *testing.T) {
	llm := &fakeLLM{}
	agent := New(llm, "model-a")
	if _, err := agent.Ask(context.Background(), Request{Message: "  "}); err == nil {
		t.Fatal("Ask() error = nil, want an error")
	}
	if len(llm.requests) != 0 {
		t.Fatalf("LLM calls = %d, want 0", len(llm.requests))
	}
}

func TestAgentComparesShortLongAndOverflowDialogs(t *testing.T) {
	llm := &countingLLM{counts: []TokenCounts{
		{CurrentRequestTokens: 40, ProjectedInputTokens: 40},
		{CurrentRequestTokens: 40, ProjectedInputTokens: 120_000},
		{CurrentRequestTokens: 40, ProjectedInputTokens: 300_000},
	}}
	a := New(llm, "gpt-5.3-codex")

	short, err := a.Ask(context.Background(), Request{Message: "short"})
	if err != nil {
		t.Fatalf("short Ask() error = %v", err)
	}
	long, err := a.Ask(context.Background(), Request{Message: "long"})
	if err != nil {
		t.Fatalf("long Ask() error = %v", err)
	}
	overflow, err := a.Ask(context.Background(), Request{Message: "overflow"})
	if err != nil {
		t.Fatalf("overflow Ask() error = %v", err)
	}

	if short.TokenMetrics.CurrentRequestTokens != 40 || short.TokenMetrics.HistoryTokens != 0 {
		t.Fatalf("short metrics = %#v", short.TokenMetrics)
	}
	if long.TokenMetrics.HistoryTokens != 119_960 || long.TokenMetrics.ContextWarning != "" {
		t.Fatalf("long metrics = %#v", long.TokenMetrics)
	}
	if overflow.TokenMetrics.HistoryTokens != 299_960 || overflow.TokenMetrics.ContextWarning == "" {
		t.Fatalf("overflow metrics = %#v", overflow.TokenMetrics)
	}
	if overflow.TokenMetrics.ProjectedInputTokens <= overflow.TokenMetrics.MaxInputTokens {
		t.Fatalf("overflow was not detected: %#v", overflow.TokenMetrics)
	}
	if llm.callCount != 3 {
		t.Fatalf("LLM calls = %d, want 3; overflow must not block the request", llm.callCount)
	}
	if overflow.TokenMetrics.CumulativeInputTokens != 300 || overflow.TokenMetrics.CumulativeOutputTokens != 60 || overflow.TokenMetrics.CumulativeTotalTokens != 360 {
		t.Fatalf("cumulative metrics = %#v", overflow.TokenMetrics)
	}
	if overflow.TokenMetrics.CumulativeCostUSD == nil {
		t.Fatal("cumulative cost is missing")
	}
}

func TestAgentContinuesWhenTokenCountingFails(t *testing.T) {
	llm := &countingLLM{countError: errors.New("counter unavailable")}
	a := New(llm, "gpt-5.3-codex")
	response, err := a.Ask(context.Background(), Request{Message: "question"})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if llm.callCount != 1 {
		t.Fatalf("LLM calls = %d, want 1", llm.callCount)
	}
	if response.TokenMetrics.TokenCountAvailable {
		t.Fatalf("token metrics = %#v", response.TokenMetrics)
	}
	if response.TokenMetrics.SummaryMessages != 0 || response.TokenMetrics.RetainedMessages != 2 {
		t.Fatalf("conversation message counters = %#v, want 0 summary and 2 retained", response.TokenMetrics)
	}
}

func TestAgentCompressesOldMessagesAndKeepsRecentMessagesVerbatim(t *testing.T) {
	llm := &compressionLLM{}
	history := &memoryHistory{}
	a, err := NewPersistent(llm, "model-a", "conversation", history, WithCompression(CompressionConfig{
		Enabled:   true,
		KeepLast:  2,
		BatchSize: 2,
	}))
	if err != nil {
		t.Fatalf("NewPersistent() error = %v", err)
	}

	for _, message := range []string{"first", "second", "third"} {
		if _, err := a.Ask(context.Background(), Request{Message: message}); err != nil {
			t.Fatalf("Ask(%q) error = %v", message, err)
		}
	}

	if len(llm.requests) != 4 {
		t.Fatalf("LLM requests = %d, want 4 (three answers and one summary)", len(llm.requests))
	}
	summaryRequest := llm.requests[2]
	if summaryRequest.Instructions != summaryInstructions || !strings.Contains(summaryRequest.Input, "[user]\nfirst") {
		t.Fatalf("summary request = %#v", summaryRequest)
	}
	mainRequest := llm.requests[3]
	if mainRequest.PreviousResponseID != "" {
		t.Fatalf("compressed request continued old chain: %#v", mainRequest)
	}
	wantHistory := []ContextMessage{
		{Role: "developer", Content: "Summary of earlier conversation. Use it as context together with the recent messages below:\nThe user asked the first question and received an answer."},
		{Role: "user", Content: "second"},
		{Role: "assistant", Content: "answer"},
	}
	if !reflect.DeepEqual(mainRequest.History, wantHistory) {
		t.Fatalf("compressed history = %#v, want %#v", mainRequest.History, wantHistory)
	}
	if history.state.Summary == nil || history.state.Summary.MessageCount != 2 || history.state.Summary.Text == "" || history.state.Summary.Runs != 1 {
		t.Fatalf("persisted summary = %#v", history.state.Summary)
	}
	if len(history.state.Messages) != 6 {
		t.Fatalf("visible transcript messages = %d, want 6", len(history.state.Messages))
	}
}

func TestCompressionDoesNotWaitForFullBatchAfterKeepLastBoundary(t *testing.T) {
	llm := &compressionLLM{}
	history := &memoryHistory{}
	a, err := NewPersistent(llm, "model-a", "conversation", history, WithCompression(CompressionConfig{
		Enabled:   true,
		KeepLast:  2,
		BatchSize: 10,
	}))
	if err != nil {
		t.Fatalf("NewPersistent() error = %v", err)
	}

	for _, message := range []string{"first", "second", "third"} {
		if _, err := a.Ask(context.Background(), Request{Message: message}); err != nil {
			t.Fatalf("Ask(%q) error = %v", message, err)
		}
	}

	if len(llm.requests) != 4 || llm.requests[2].Instructions != summaryInstructions {
		t.Fatalf("requests = %#v, want compression before the third answer", llm.requests)
	}
	if history.state.Summary == nil || history.state.Summary.MessageCount != 2 {
		t.Fatalf("summary = %#v, want first 2 messages compressed", history.state.Summary)
	}
}

func TestCompressionReportsTokenSavingsAndSurvivesRestart(t *testing.T) {
	llm := &compressionLLM{}
	history := &memoryHistory{}
	option := WithCompression(CompressionConfig{Enabled: true, KeepLast: 2, BatchSize: 4})
	a, err := NewPersistent(llm, "model-a", "conversation", history, option)
	if err != nil {
		t.Fatalf("NewPersistent() error = %v", err)
	}
	for _, message := range []string{"first", "second", "third"} {
		if _, err := a.Ask(context.Background(), Request{Message: message}); err != nil {
			t.Fatalf("Ask(%q) error = %v", message, err)
		}
	}
	response, err := a.Ask(context.Background(), Request{Message: "fourth"})
	if err != nil {
		t.Fatalf("compressed Ask() error = %v", err)
	}
	if !response.TokenMetrics.CompressionApplied || response.TokenMetrics.UncompressedInputTokens != 300 || response.TokenMetrics.CompressedInputTokens != 100 || response.TokenMetrics.SavedInputTokens != 200 {
		t.Fatalf("compression metrics = %#v", response.TokenMetrics)
	}
	if response.TokenMetrics.SummaryMessages != 4 || response.TokenMetrics.RetainedMessages != 4 {
		t.Fatalf("compression message counters = %#v, want 4 summary and 4 retained", response.TokenMetrics)
	}

	restarted, err := NewPersistent(llm, "model-a", "conversation", history, option)
	if err != nil {
		t.Fatalf("restart error = %v", err)
	}
	if _, err := restarted.Ask(context.Background(), Request{Message: "fifth"}); err != nil {
		t.Fatalf("Ask after restart error = %v", err)
	}
	lastSummary := llm.requests[len(llm.requests)-2]
	last := llm.requests[len(llm.requests)-1]
	if lastSummary.Instructions != summaryInstructions || !strings.Contains(lastSummary.Input, "Existing summary:\n") {
		t.Fatalf("persisted summary was not extended after restart: %#v", lastSummary)
	}
	if last.PreviousResponseID != "" || len(last.History) == 0 || last.History[0].Role != "developer" {
		t.Fatalf("compressed chain was not rebuilt after restart: %#v", last)
	}
}

func TestSessionCompressionSettingsPersistAndLockAfterFirstMessage(t *testing.T) {
	llm := &fakeLLM{}
	history := &memoryHistory{}
	a, err := NewPersistent(llm, "model-a", "conversation", history, WithCompression(CompressionConfig{
		Enabled:   true,
		KeepLast:  10,
		BatchSize: 10,
	}))
	if err != nil {
		t.Fatalf("NewPersistent() error = %v", err)
	}
	enabled := false
	keepLast := 6
	if _, err := a.Ask(context.Background(), Request{
		Message:            "first",
		CompressionEnabled: &enabled,
		ContextKeepLast:    &keepLast,
	}); err != nil {
		t.Fatalf("first Ask() error = %v", err)
	}
	if history.state.Compression == nil || history.state.Compression.Enabled || history.state.Compression.KeepLast != 6 {
		t.Fatalf("persisted compression = %#v", history.state.Compression)
	}

	enabled = true
	if _, err := a.Ask(context.Background(), Request{Message: "second", CompressionEnabled: &enabled}); !errors.Is(err, ErrCompressionLocked) {
		t.Fatalf("settings change error = %v, want ErrCompressionLocked", err)
	}
	if llm.callCount != 1 {
		t.Fatalf("LLM calls = %d, want 1", llm.callCount)
	}
}
