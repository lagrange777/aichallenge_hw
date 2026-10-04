package rag

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/docindex"
)

type processorLLM struct {
	requests []agent.CompletionRequest
	output   string
	err      error
}

func (l *processorLLM) Complete(_ context.Context, r agent.CompletionRequest) (agent.CompletionResponse, error) {
	l.requests = append(l.requests, r)
	return agent.CompletionResponse{Output: l.output, Model: r.Model, Usage: agent.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}, l.err
}
func TestRewriteValidationAndIsolation(t *testing.T) {
	for _, tc := range []struct {
		out   string
		valid bool
	}{{`{"query":"JSON limit 20 bytes"}`, true}, {`{"query":"JSON limit 21 bytes"}`, false}, {`{"query":"limit 20 bytes"}`, false}, {`{"query":"JSON limit 20 bytes","answer":"secret"}`, false}} {
		l := &processorLLM{output: tc.out}
		r := Retriever{LLM: l, Model: "m"}
		_, _, err := r.rewrite(context.Background(), "JSON 20")
		if (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.out, err)
		}
		q := l.requests[0]
		if len(q.History) != 0 || q.PreviousResponseID != "" || len(q.Tools) != 0 || q.Profile != nil || !q.Internal || q.Instructions != RewritePrompt {
			t.Fatal("rewrite got conversation context")
		}
	}
}
func TestRerankRejectsMalformedOrUnsupportedEvidence(t *testing.T) {
	for _, out := range []string{
		`{"scores":[]}`, `{"scores":[{"id":"unknown","score":3,"reason":"x","evidenceLine":1}]}`,
		`{"scores":[{"id":"a","score":4,"reason":"x","evidenceLine":1}]}`,
		`{"scores":[{"id":"a","score":3,"reason":"x","evidenceLine":999}]}`,
		`{"scores":[{"id":"a","reason":"x","evidenceLine":1}]}`,
	} {
		l := &processorLLM{output: out}
		r := Retriever{LLM: l, Model: "m"}
		c := []agent.Candidate{{Source: agent.Source{ChunkID: "a", Text: "fact"}}}
		if _, err := r.rerank(context.Background(), "q", c); err == nil {
			t.Fatal("invalid grading accepted", out)
		}
		if c[0].Evaluated {
			t.Fatal("partial mutation")
		}
	}
}
func TestSelectionThresholdOrderingAndContextBudget(t *testing.T) {
	r := &agent.Retrieval{Candidates: []agent.Candidate{
		{Source: agent.Source{ChunkID: "noise", Text: "noise"}, Relevance: 1, Evaluated: true},
		{Source: agent.Source{ChunkID: "partial", Text: "part"}, Relevance: 2, Evaluated: true},
		{Source: agent.Source{ChunkID: "direct", Text: strings.Repeat("я", 10000)}, Relevance: 3, Evaluated: true},
	}}
	o := agent.DefaultRetrievalOptions()
	o.Mode = "filter"
	selectSources(r, o)
	if len(r.Sources) != 1 || r.Sources[0].ChunkID != "direct" || len([]rune(r.Sources[0].Text)) != ContextCharacters || r.Candidates[0].Decision != "below_threshold" || r.Candidates[1].Decision != "context_budget" {
		t.Fatalf("selection: %+v", r)
	}
	o.RelevanceThreshold = 3
	r.Candidates[2].Relevance = 2
	selectSources(r, o)
	if !r.Empty || len(r.Sources) != 0 {
		t.Fatal("empty filter fell back")
	}
}
func TestFusionDeduplicatesAndPreservesSingleList(t *testing.T) {
	a := docindex.Match{Chunk: docindex.Chunk{ID: "a"}, Score: .1}
	b := docindex.Match{Chunk: docindex.Chunk{ID: "b"}, Score: .9}
	one := fuse([][]docindex.Match{{a, b}}, 20)
	if one[0].ChunkID != "a" {
		t.Fatal("changed retrieval order")
	}
	both := fuse([][]docindex.Match{{a, b}, {b}}, 20)
	if len(both) != 2 || both[0].ChunkID != "b" || both[0].OriginalRank != 2 || both[0].RewriteRank != 1 {
		t.Fatalf("bad fusion: %+v", both)
	}
}
func TestRetrievalOptions(t *testing.T) {
	for _, o := range []agent.RetrievalOptions{{Mode: "wrong", TopKBefore: 20, TopKAfter: 5}, {Mode: "full", TopKBefore: 2, TopKAfter: 5}, {Mode: "filter", TopKBefore: 20, TopKAfter: 5, RelevanceThreshold: 4}} {
		if o.Validate() == nil {
			t.Fatal("bad settings accepted")
		}
	}
}
func TestRewriteFailureIsVisibleAndRerankFailureIsFatal(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("# Fact\n\nA useful fact."), 0600)
	docs, err := docindex.LoadCorpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := docindex.Open(filepath.Join(t.TempDir(), "i.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	emb := &embedding{model: "test"}
	if _, err = s.Build(context.Background(), docs, emb, docindex.ChunkConfig{Size: 100, Overlap: 10}, nil); err != nil {
		t.Fatal(err)
	}
	llm := &processorLLM{err: errors.New("offline")}
	r := Retriever{Documents: &docindex.Service{Store: s, Embedder: emb}, LLM: llm, Model: "m"}
	o := agent.DefaultRetrievalOptions()
	o.Mode = "rewrite"
	result, err := r.RetrieveConfigured(context.Background(), "question", o)
	if err != nil || result.Warning == "" || len(result.Sources) == 0 {
		t.Fatalf("rewrite fallback: %v %+v", err, result)
	}
	o.Mode = "filter"
	if _, err = r.RetrieveConfigured(context.Background(), "question", o); err == nil {
		t.Fatal("reranker silently bypassed")
	}
}
func TestBenchmarkSeparationAndSourceAnchors(t *testing.T) {
	test, _, err := LoadBenchmark("../../documents/rag23-questions.json")
	if err != nil {
		t.Fatal(err)
	}
	calibration, _, err := LoadBenchmark("../../documents/rag23-calibration.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(test) != 15 || len(calibration) != 6 {
		t.Fatal("unexpected dataset size")
	}
	ids := map[string]bool{}
	queries := map[string]bool{}
	for _, q := range append(test, calibration...) {
		if ids[q.ID] || queries[q.Query] {
			t.Fatal("dataset overlap")
		}
		ids[q.ID] = true
		queries[q.Query] = true
		for _, e := range q.Relevant {
			b, err := os.ReadFile(filepath.Join("../../documents/corpus", e.Source))
			if err != nil || e.Quote == "" || !strings.Contains(string(b), e.Quote) {
				t.Fatalf("missing anchor %s", q.ID)
			}
		}
	}
}
func TestRerankDoesNotSeeExpectedAnswersOrRetrievalScores(t *testing.T) {
	l := &processorLLM{output: `{"scores":[{"id":"a","score":3,"reason":"Есть факт","evidenceLine":1}]}`}
	r := Retriever{LLM: l, Model: "m"}
	c := []agent.Candidate{{Source: agent.Source{ChunkID: "a", Text: "fact", Score: .8}, Rank: 1}}
	if _, err := r.rerank(context.Background(), "original question", c); err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal([]byte(l.requests[0].Input), &data); err != nil {
		t.Fatal(err)
	}
	if len(data) != 2 || data["question"] == nil || data["candidates"] == nil || strings.Contains(l.requests[0].Input, "0.8") {
		t.Fatal("unexpected grader inputs")
	}
}

type repairLLM struct{ calls int }

func (l *repairLLM) Complete(_ context.Context, r agent.CompletionRequest) (agent.CompletionResponse, error) {
	l.calls++
	output := `{"scores":[{"id":"a","score":3,"reason":"direct","evidenceLine":999}]}`
	if l.calls == 2 {
		if !strings.Contains(r.Input, "validationError") || !strings.Contains(r.Input, "previousOutput") {
			return agent.CompletionResponse{}, errors.New("repair feedback missing")
		}
		output = `{"scores":[{"id":"a","score":3,"reason":"direct","evidenceLine":1}]}`
	}
	return agent.CompletionResponse{Output: output, Model: r.Model, Usage: agent.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}, nil
}
func TestRerankRepairKeepsValidationAndAccountsForBothCalls(t *testing.T) {
	llm := &repairLLM{}
	candidates := []agent.Candidate{{Source: agent.Source{ChunkID: "a", Text: "real evidence"}}}
	step, err := (Retriever{LLM: llm, Model: "test"}).rerank(context.Background(), "q", candidates)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 2 || step.Attempts != 2 || step.Warning == "" || step.Usage.TotalTokens != 30 || step.Usage.InputTokens != 20 || !candidates[0].Evaluated || candidates[0].Evidence != "real evidence" {
		t.Fatalf("repair lost accounting or validation: %+v %+v", step, candidates)
	}
}
