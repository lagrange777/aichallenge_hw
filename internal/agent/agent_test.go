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
	agent.Reset()
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

func TestAgentStartsFreshChainAfterModelChange(t *testing.T) {
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

	want := []string{"", "resp_1", ""}
	for index := range want {
		if llm.requests[index].PreviousResponseID != want[index] {
			t.Fatalf("previous response ID %d = %q, want %q", index, llm.requests[index].PreviousResponseID, want[index])
		}
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
