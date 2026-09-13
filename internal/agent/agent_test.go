package agent

import (
	"context"
	"errors"
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
}
