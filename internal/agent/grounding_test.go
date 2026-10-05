package agent

import (
	"codex-chat-cli/internal/memory"
	"context"
	"errors"
	"strings"
	"testing"
)

type groundingLLM struct {
	calls   []CompletionRequest
	outputs []string
	fail    bool
}

func (l *groundingLLM) Complete(_ context.Context, r CompletionRequest) (CompletionResponse, error) {
	l.calls = append(l.calls, r)
	if l.fail {
		return CompletionResponse{}, errors.New("API unavailable")
	}
	i := len(l.calls) - 1
	if i >= len(l.outputs) {
		return CompletionResponse{}, errors.New("unexpected call")
	}
	return CompletionResponse{Output: l.outputs[i], Model: "test", Usage: Usage{InputTokens: 2, OutputTokens: 3}}, nil
}

type groundingRetriever struct{ empty bool }

func (r groundingRetriever) Retrieve(context.Context, string) (*Retrieval, error) {
	s := []Source{{Ref: "S1", ChunkID: "chunk1", Source: "file.md", Section: "Storage", Text: "History lives on disk.\nTTL is 12 hours."}}
	if r.empty {
		s = nil
	}
	return &Retrieval{Sources: s}, nil
}
func (r groundingRetriever) RetrieveConfigured(ctx context.Context, q string, o RetrievalOptions) (*Retrieval, error) {
	v, e := r.Retrieve(ctx, q)
	v.Options = &o
	return v, e
}

const goodDraft = `{"status":"answered","claims":[{"text":"TTL составляет 12 часов.","evidence":[{"ref":"S1","start":2,"end":2}]}]}`
const goodCheck = `{"claims":[{"index":0,"verdict":"supported","reason":"12 часов прямо указаны"}],"complete":true}`

func strictAsk(l *groundingLLM, empty bool) (Response, error) {
	o := DefaultRetrievalOptions()
	o.Mode = "filter"
	o.Grounded = true
	a := New(l, "test", WithRetriever(groundingRetriever{empty}))
	return a.Ask(context.Background(), Request{Message: "Какой TTL?", RAG: true, RAGOptions: &o})
}
func TestGroundingExactQuotesAndUsage(t *testing.T) {
	l := &groundingLLM{outputs: []string{goodDraft, goodCheck}}
	r, err := strictAsk(l, false)
	if err != nil {
		t.Fatal(err)
	}
	g := r.Retrieval.Grounding
	if g.Status != "answered" || g.Quotes[0].Text != "TTL is 12 hours." || len(g.Sources) != 1 || !strings.Contains(r.Text, "chunk1") || r.Usage.TotalTokens != 10 {
		t.Fatalf("%+v %+v", r, g)
	}
	for _, c := range l.calls {
		if !c.Internal || len(c.Tools) > 0 || c.PreviousResponseID != "" {
			t.Fatal("grounding leaked state")
		}
	}
}
func TestGroundingRejectsForgedEvidence(t *testing.T) {
	for _, raw := range []string{strings.ReplaceAll(goodDraft, "S1", "S99"), strings.ReplaceAll(goodDraft, `"end":2`, `"end":30`), strings.ReplaceAll(goodDraft, `"evidence":[{"ref":"S1","start":2,"end":2}]`, `"evidence":[]`), goodDraft + " trailing", `{"status":"unknown","claims":[{}]}`} {
		if _, err := validateGroundedDraft(raw, []Source{{Ref: "S1", Text: "one\ntwo"}}); err == nil {
			t.Fatal(raw)
		}
	}
}
func TestGroundingRepairAndRecheck(t *testing.T) {
	bad := `{"claims":[{"index":0,"verdict":"contradicted","reason":"wrong value"}],"complete":true}`
	l := &groundingLLM{outputs: []string{strings.ReplaceAll(goodDraft, "12 часов", "24 часа"), bad, goodDraft, goodCheck}}
	r, err := strictAsk(l, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Retrieval.Grounding.Attempts != 2 || len(l.calls) != 4 || strings.Contains(r.Text, "24 часа") {
		t.Fatal(r)
	}
	if !strings.Contains(l.calls[2].Input, "validationError") {
		t.Fatal("missing correction feedback")
	}
}
func TestGroundingFailureNeverSaved(t *testing.T) {
	bad := `{"claims":[],"complete":true}`
	l := &groundingLLM{outputs: []string{goodDraft, bad, goodDraft, bad}}
	a := New(l, "test", WithRetriever(groundingRetriever{}))
	o := DefaultRetrievalOptions()
	o.Mode = "filter"
	o.Grounded = true
	_, err := a.Ask(context.Background(), Request{Message: "TTL?", RAG: true, RAGOptions: &o})
	if err == nil || len(a.messages) != 0 {
		t.Fatal("unverified draft saved")
	}
}
func TestGroundingEmptyNoLLMAndClarification(t *testing.T) {
	l := &groundingLLM{}
	r, err := strictAsk(l, true)
	if err != nil || len(l.calls) != 0 || r.Text != UnknownRAGAnswer || r.Retrieval.Grounding.Status != "unknown" || len(r.Retrieval.Grounding.Quotes) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestGroundingPartialAndErrors(t *testing.T) {
	l := &groundingLLM{outputs: []string{goodDraft, strings.ReplaceAll(goodCheck, `"complete":true`, `"complete":false`)}}
	r, err := strictAsk(l, false)
	if err != nil || r.Retrieval.Grounding.Status != "partial" || !strings.Contains(r.Text, "Уточните") {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err = strictAsk(&groundingLLM{fail: true}, false); err == nil {
		t.Fatal("API failure must not be unknown")
	}
}
func TestGroundingOptionsAndClone(t *testing.T) {
	o := DefaultRetrievalOptions()
	o.Grounded = true
	if o.Validate() == nil {
		t.Fatal("baseline accepted")
	}
	o.Mode = "filter"
	o.RelevanceThreshold = 1
	if o.Validate() == nil {
		t.Fatal("weak threshold")
	}
	g, _ := validateGroundedDraft(goodDraft, []Source{{Ref: "S1", Text: "one\ntwo"}})
	r := &Retrieval{Grounding: g}
	copy := cloneRetrieval(r)
	copy.Grounding.Claims[0].Evidence[0].Ref = "evil"
	copy.Grounding.Quotes[0].Text = "evil"
	if r.Grounding.Claims[0].Evidence[0].Ref != "S1" || r.Grounding.Quotes[0].Text != "two" {
		t.Fatal("aliasing")
	}
}

func TestGroundingChecksRenderedTextAgainstInvariants(t *testing.T) {
	l := &groundingLLM{outputs: []string{goodDraft, goodCheck, `{"verdict":"violation","checkedIds":["rule"],"conflictingIds":["rule"],"explanation":"Не допускается"}`}}
	a := New(l, "test")
	r, _ := groundingRetriever{}.Retrieve(context.Background(), "TTL?")
	_, _, err := a.completeGrounded(context.Background(), CompletionRequest{Model: "test", Input: "TTL?"}, r, invariantPlan{Rules: []memory.Invariant{{ID: "rule"}}, Verdict: invariantVerdict{Verdict: "allow"}})
	if !errors.Is(err, ErrInvariant) || r.Grounding != nil {
		t.Fatal("invariant violation was accepted")
	}
	if len(l.calls) != 3 || !strings.Contains(l.calls[2].Input, "Источники и цитаты:") || !strings.Contains(l.calls[2].Input, "chunk1") {
		t.Fatal("checker did not receive final rendered answer")
	}
}
