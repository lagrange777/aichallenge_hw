package rag

import (
	"codex-chat-cli/internal/agent"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type groundedRecorder struct{ requests []agent.CompletionRequest }

func (l *groundedRecorder) Complete(_ context.Context, r agent.CompletionRequest) (agent.CompletionResponse, error) {
	l.requests = append(l.requests, r)
	out := "Answer [S1]"
	switch r.Instructions {
	case agent.GroundingPrompt:
		out = `{"status":"answered","claims":[{"text":"The TTL is 12 hours.","evidence":[{"ref":"S1","start":1,"end":1}]}]}`
	case agent.GroundingCheckPrompt:
		out = `{"claims":[{"index":0,"verdict":"supported","reason":"Explicitly stated"}],"complete":true}`
	}
	return agent.CompletionResponse{Output: out, Model: r.Model, Usage: agent.Usage{InputTokens: 2, OutputTokens: 3}}, nil
}
func TestGroundingComparisonSharesEvidenceAndIsolatesCalls(t *testing.T) {
	o := agent.DefaultRetrievalOptions()
	o.Mode = "filter"
	r := &agent.Retrieval{Options: &o, Sources: []agent.Source{{Ref: "S1", Source: "test.md", ChunkID: "one", Section: "TTL", Text: "The TTL is 12 hours."}}, Steps: []agent.RetrievalStep{{Name: "rerank", Usage: agent.Usage{TotalTokens: 100}}}, EmbeddingTokens: 10}
	saved, _ := json.Marshal(r)
	l := &groundedRecorder{}
	before, err := FrozenAnswer(context.Background(), l, "test", "TTL?", r, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := FrozenAnswer(context.Background(), l, "test", "TTL?", r, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Retrieval.Sources, after.Retrieval.Sources) || after.Usage.TotalTokens != 10 || before.Usage.TotalTokens != 5 || len(l.requests) != 3 {
		t.Fatal("unfair evidence or double charged retrieval")
	}
	now, _ := json.Marshal(r)
	if string(now) != string(saved) {
		t.Fatal("original retrieval mutated")
	}
	for _, req := range l.requests {
		if req.Profile != nil || len(req.Tools) > 0 || req.PreviousResponseID != "" {
			t.Fatal("state leaked")
		}
	}
	if l.requests[1].Instructions != agent.GroundingPrompt || l.requests[2].Instructions != agent.GroundingCheckPrompt {
		t.Fatal("comparison replaced internal instructions")
	}
}
func TestGroundingDatasetHasSevenAnswersThreeUnknowns(t *testing.T) {
	qs, _, err := LoadBenchmark("../../documents/rag24-questions.json")
	if err != nil {
		t.Fatal(err)
	}
	positive := 0
	for _, q := range qs {
		if !q.Answerable {
			continue
		}
		positive++
		for _, e := range q.Relevant {
			data, err := os.ReadFile("../../documents/corpus/" + e.Source)
			if err != nil || !strings.Contains(string(data), e.Quote) {
				t.Fatalf("missing evidence for %s: %v", q.ID, err)
			}
		}
	}
	if len(qs) != 10 || positive != 7 {
		t.Fatal("expected 7 + 3")
	}
}
