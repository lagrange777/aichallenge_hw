package agent

import (
	"codex-chat-cli/internal/memory"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type toolLLM struct {
	requests []CompletionRequest
	fn       func(CompletionRequest, int) (CompletionResponse, error)
}

func (f *toolLLM) Complete(_ context.Context, r CompletionRequest) (CompletionResponse, error) {
	f.requests = append(f.requests, r)
	return f.fn(r, len(f.requests))
}

type toolFixture struct {
	calls   int
	failure bool
}

func (p *toolFixture) Tools(context.Context) ([]ToolDefinition, []ToolRun) {
	return []ToolDefinition{{Name: "fixture", ToolName: "get_issue", ServerName: "Mock"}}, nil
}
func (p *toolFixture) Call(_ context.Context, _ ToolDefinition, args string) (string, error) {
	p.calls++
	if p.failure {
		return "", errors.New("MCP unavailable")
	}
	return `{"key":"DEMO-101","blockedBy":"DEMO-100"}`, nil
}
func TestToolRoundTripAndPersistedTrace(t *testing.T) {
	p := &toolFixture{}
	llm := &toolLLM{fn: func(r CompletionRequest, n int) (CompletionResponse, error) {
		if n == 1 {
			if len(r.Tools) != 1 {
				t.Fatal("no tools")
			}
			return CompletionResponse{ResponseID: "resp1", ToolCalls: []ToolCall{{CallID: "c1", Name: "fixture", Arguments: `{"key":"DEMO-101"}`}}, Usage: Usage{TotalTokens: 10}}, nil
		}
		if r.PreviousResponseID != "resp1" || len(r.ToolOutputs) != 1 || r.ToolOutputs[0].CallID != "c1" || !strings.Contains(r.ToolOutputs[0].Output, "DEMO-100") || len(r.History) != 0 || r.Input != "" {
			t.Fatalf("bad continuation: %+v", r)
		}
		return CompletionResponse{ResponseID: "resp2", Output: "DEMO-101 blocked by DEMO-100", Usage: Usage{TotalTokens: 20}}, nil
	}}
	a := New(llm, "test", WithTools(p))
	response, err := a.Ask(context.Background(), Request{Message: "Why blocked?"})
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 || response.Usage.TotalTokens != 30 {
		t.Fatalf("calls=%d usage=%+v", p.calls, response.Usage)
	}
	messages := a.Messages()
	if len(messages) != 2 || len(messages[1].ToolRuns) != 1 || messages[1].ToolRuns[0].Error {
		t.Fatalf("trace missing: %+v", messages)
	}
	messages[1].ToolRuns[0].Output = "mutated"
	if a.Messages()[1].ToolRuns[0].Output == "mutated" {
		t.Fatal("trace alias")
	}
}
func TestToolBudgetAndFailures(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			p := &toolFixture{failure: failure}
			llm := &toolLLM{fn: func(r CompletionRequest, n int) (CompletionResponse, error) {
				if n > 1 && len(r.ToolOutputs) != 1 {
					t.Fatal("missing output")
				}
				if n == 7 {
					if len(r.Tools) != 0 {
						t.Fatal("tools not disabled")
					}
					return CompletionResponse{Output: "done"}, nil
				}
				return CompletionResponse{ResponseID: fmt.Sprint(n), ToolCalls: []ToolCall{{CallID: fmt.Sprint(n), Name: "fixture", Arguments: `{}`}}}, nil
			}}
			a := New(llm, "test", WithTools(p))
			defs, _ := p.Tools(context.Background())
			c, err := a.completeWithTools(context.Background(), CompletionRequest{Tools: defs}, invariantPlan{})
			if err != nil || p.calls != 6 || len(c.ToolRuns) != 6 || c.ToolRuns[0].Error != failure {
				t.Fatalf("bad result: %+v %v calls=%d", c, err, p.calls)
			}
		})
	}
}
func TestUnknownToolNeverExecutes(t *testing.T) {
	p := &toolFixture{}
	llm := &toolLLM{fn: func(r CompletionRequest, n int) (CompletionResponse, error) {
		if n == 1 {
			return CompletionResponse{ResponseID: "1", ToolCalls: []ToolCall{{CallID: "1", Name: "forbidden", Arguments: `{}`}}}, nil
		}
		return CompletionResponse{Output: "unavailable"}, nil
	}}
	a := New(llm, "test", WithTools(p))
	c, err := a.completeWithTools(context.Background(), CompletionRequest{}, invariantPlan{})
	if err != nil || p.calls != 0 || len(c.ToolRuns) != 1 || !c.ToolRuns[0].Error {
		t.Fatalf("unexpected execution: %+v %v", c, err)
	}
}
func TestToolTraceSurvivesLLMFailure(t *testing.T) {
	p := &toolFixture{}
	llm := &toolLLM{fn: func(r CompletionRequest, n int) (CompletionResponse, error) {
		if n == 1 {
			return CompletionResponse{ResponseID: "1", ToolCalls: []ToolCall{{CallID: "1", Name: "fixture", Arguments: `{}`}}, Usage: Usage{TotalTokens: 10}}, nil
		}
		return CompletionResponse{}, errors.New("offline")
	}}
	a := New(llm, "test", WithTools(p))
	defs, _ := p.Tools(context.Background())
	c, err := a.completeWithTools(context.Background(), CompletionRequest{Tools: defs}, invariantPlan{})
	if err != nil || len(c.ToolRuns) != 1 || c.Usage.TotalTokens != 10 || c.Output == "" {
		t.Fatalf("trace lost: %+v %v", c, err)
	}
}
func TestInternalAndRejectedRequestsHaveNoTools(t *testing.T) {
	p := &toolFixture{}
	a := New(&fakeLLM{}, "test", WithTools(p))
	r := CompletionRequest{Internal: true}
	a.prepareTools(context.Background(), &r, invariantPlan{})
	if len(r.Tools) > 0 {
		t.Fatal("internal tools")
	}
	r = CompletionRequest{}
	a.prepareTools(context.Background(), &r, invariantPlan{Failed: true})
	if len(r.Tools) > 0 {
		t.Fatal("failed preflight tools")
	}
}

func TestToolInvariantViolationPreventsExecution(t *testing.T) {
	p := &toolFixture{}
	llm := &toolLLM{fn: func(r CompletionRequest, n int) (CompletionResponse, error) {
		if r.Internal {
			if len(r.Tools) > 0 {
				t.Fatal("checker got tools")
			}
			return CompletionResponse{Output: `{"verdict":"violation","checkedIds":["privacy"],"conflictingIds":["privacy"],"explanation":"Доступ запрещён"}`}, nil
		}
		if n == 1 {
			return CompletionResponse{ResponseID: "1", ToolCalls: []ToolCall{{CallID: "1", Name: "fixture", Arguments: `{}`}}}, nil
		}
		if len(r.ToolOutputs) != 1 || !strings.Contains(r.ToolOutputs[0].Output, "инвариантов") {
			t.Fatal("blocked result not returned")
		}
		return CompletionResponse{Output: "Доступ запрещён"}, nil
	}}
	a := New(llm, "test", WithTools(p))
	defs, _ := p.Tools(context.Background())
	c, err := a.completeWithTools(context.Background(), CompletionRequest{Tools: defs}, invariantPlan{Rules: []memory.Invariant{{ID: "privacy", Rule: "Не читать задачи", Status: "active"}}})
	if err != nil || p.calls != 0 || len(c.ToolRuns) != 1 || !c.ToolRuns[0].Error {
		t.Fatalf("guard failed: %+v %v", c, err)
	}
}
