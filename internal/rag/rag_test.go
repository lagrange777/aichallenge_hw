package rag

import (
	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/docindex"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type embedding struct {
	model string
	calls int
}

func (e *embedding) Model() string   { return e.model }
func (e *embedding) Dimensions() int { return 2 }
func (e *embedding) Embed(_ context.Context, texts []string) (docindex.EmbeddingBatch, error) {
	e.calls++
	b := docindex.EmbeddingBatch{Tokens: 5}
	for range texts {
		b.Vectors = append(b.Vectors, []float32{1, 0})
	}
	return b, nil
}

type recorder struct{ requests []agent.CompletionRequest }

func (r *recorder) Complete(_ context.Context, q agent.CompletionRequest) (agent.CompletionResponse, error) {
	r.requests = append(r.requests, q)
	return agent.CompletionResponse{Output: "answer [S1]", Model: q.Model}, nil
}
func TestRetrievalAndFreshPairedAnswers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# A\n\nEvidence fact."), 0600); err != nil {
		t.Fatal(err)
	}
	docs, err := docindex.LoadCorpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := docindex.Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := &embedding{model: "test"}
	if _, err = s.Build(context.Background(), docs, e, docindex.ChunkConfig{Size: 100, Overlap: 10}, nil); err != nil {
		t.Fatal(err)
	}
	r := Retriever{Documents: &docindex.Service{Store: s, Embedder: e}}
	llm := &recorder{}
	q := Question{ID: "test", Query: "question", Expectations: []string{"SECRET EXPECTATION"}, Relevant: []docindex.Evidence{{Source: "a.md", Quote: "Evidence fact."}}}
	pair, err := Compare(context.Background(), llm, r, "model", q)
	if err != nil {
		t.Fatal(err)
	}
	if !pair.EvidenceHit || len(llm.requests) != 2 || len(llm.requests[0].History) != 0 || len(llm.requests[1].History) != 2 {
		t.Fatal("not isolated")
	}
	for _, req := range llm.requests {
		if req.PreviousResponseID != "" || len(req.Tools) > 0 || req.Profile != nil || req.Instructions != EvaluationPrompt {
			t.Fatalf("unfair comparison: %+v", req)
		}
		for _, h := range req.History {
			if strings.Contains(h.Content, "SECRET") {
				t.Fatal("expectations leaked")
			}
		}
	}
	if pair.With.Retrieval.EmbeddingTokens != 5 || pair.With.Retrieval.Sources[0].ChunkID == "" {
		t.Fatal("missing metadata")
	}
	e.model = "different"
	calls := e.calls
	if _, err = r.Retrieve(context.Background(), "question"); err == nil || e.calls != calls {
		t.Fatal("model mismatch not rejected before API call")
	}
}
func TestControlQuestionsGroundedInCorpus(t *testing.T) {
	qs, err := LoadQuestions("../../documents/rag-questions.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range qs {
		for _, e := range q.Relevant {
			b, err := os.ReadFile(filepath.Join("../../documents/corpus", e.Source))
			if err != nil {
				t.Fatal(err)
			}
			if e.Quote == "" || !strings.Contains(string(b), e.Quote) {
				t.Fatalf("missing source evidence %s", q.ID)
			}
		}
	}
}
func TestReviewsRejectStaleAnswers(t *testing.T) {
	r := Report{Complete: true}
	reviews := map[string]map[string]Review{}
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		r.Pairs = append(r.Pairs, Pair{Question: Question{ID: id}, Without: Answer{Text: "x"}, With: Answer{Text: "x"}})
		yes := true
		review := Review{AnswerHash: AnswerHash("x"), Rationale: "rationale", CitationSupport: &yes}
		reviews[id] = map[string]Review{"without": review, "with": review}
	}
	if err := ApplyReviews(&r, reviews); err != nil {
		t.Fatal(err)
	}
	r.Pairs[0].With.Text = "changed"
	if err := ApplyReviews(&r, reviews); err == nil {
		t.Fatal("stale review accepted")
	}
}
