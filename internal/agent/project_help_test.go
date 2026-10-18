package agent

import (
	"context"
	"strings"
	"testing"
)

type projectFixture struct{ queries []string }

func (p *projectFixture) Retrieve(_ context.Context, q string) (*Retrieval, error) {
	p.queries = append(p.queries, q)
	return &Retrieval{Project: &ProjectContext{Documents: 2, Chunks: 2, Runs: []ToolRun{{Name: "git_branch", Output: `{"branch":"current"}`}}}, Sources: []Source{{Ref: "S1", ChunkID: "readme", Source: "README.md", Section: "Run", Text: "History lives on disk.\nTTL is 12 hours."}}}, nil
}
func TestProjectHelpIsIsolatedGroundedAndRefreshesEveryTurn(t *testing.T) {
	l := &groundingLLM{outputs: []string{goodDraft, goodCheck, goodDraft, goodCheck}}
	p := &projectFixture{}
	a := New(l, "test", WithProjectRetriever(p), WithContextStrategy(StrategyConfig{Type: StrategyStickyFacts, KeepLast: 5}))
	a.messages = []Message{{Role: "user", Text: "PRIVATE TASK DATA"}}
	a.previousResponseID = "hidden-old-response"
	a.facts["secret"] = "PRIVATE TASK DATA"
	for i := 0; i < 2; i++ {
		response, e := a.Ask(context.Background(), Request{Message: "/help Какой TTL?", RAG: true, RAGOptions: &RetrievalOptions{Mode: "not-a-real-mode"}})
		if e != nil {
			t.Fatal(e)
		}
		if response.Retrieval.Project == nil || !strings.Contains(response.Text, "TTL is 12 hours.") {
			t.Fatal(response)
		}
		if response.TokenMetrics.CumulativeTotalTokens != 10*(i+1) {
			t.Fatal("incorrect cumulative usage", response.TokenMetrics)
		}
	}
	if len(p.queries) != 2 || len(l.calls) != 4 || len(a.Messages()) != 5 {
		t.Fatal("request/history counts")
	}
	for _, c := range l.calls {
		if c.PreviousResponseID != "" || len(c.Tools) != 0 || len(c.History) != 0 || strings.Contains(c.Input, "PRIVATE TASK DATA") {
			t.Fatal("help leaked unrelated state")
		}
	}
	if a.facts["secret"] != "PRIVATE TASK DATA" {
		t.Fatal("help changed task facts")
	}
}
func TestProjectHelpMenuAndCommandBoundary(t *testing.T) {
	l := &groundingLLM{}
	a := New(l, "test", WithProjectRetriever(&projectFixture{}))
	r, e := a.Ask(context.Background(), Request{Message: "/help"})
	if e != nil || len(l.calls) != 0 || !strings.Contains(r.Text, "/help Как устроен") {
		t.Fatal(r, e)
	}
	for _, input := range []string{"/helper", "text /help", "/helpful"} {
		if _, ok := ProjectHelpQuestion(input); ok {
			t.Fatal(input)
		}
	}
	if q, ok := ProjectHelpQuestion(" /help\nвопрос "); !ok || q != "вопрос" {
		t.Fatal(q, ok)
	}
}
func TestProjectHelpFailureDoesNotSaveDraft(t *testing.T) {
	l := &groundingLLM{fail: true}
	a := New(l, "test", WithProjectRetriever(&projectFixture{}))
	if _, e := a.Ask(context.Background(), Request{Message: "/help TTL?"}); e == nil || len(a.Messages()) != 0 {
		t.Fatal("failed help was saved")
	}
}
