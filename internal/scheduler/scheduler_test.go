package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-chat-cli/internal/agent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeSource struct {
	calls int
	fn    func(Job) ([]Quote, error)
}

func (f *fakeSource) Resolve(context.Context, string) (SourceInfo, error) {
	return SourceInfo{ID: "source", Name: "Fixture", Version: 1}, nil
}
func (f *fakeSource) Collect(_ context.Context, j Job) ([]Quote, error) { f.calls++; return f.fn(j) }

type fakeSummary struct {
	calls int
	fail  bool
}

func (f *fakeSummary) Complete(_ context.Context, r agent.CompletionRequest) (agent.CompletionResponse, error) {
	f.calls++
	if len(r.Tools) > 0 || !r.Internal {
		panic("background summary must not have tools")
	}
	if f.fail {
		return agent.CompletionResponse{}, errors.New("offline")
	}
	return agent.CompletionResponse{Output: "Сводка на основе наблюдений", Model: "fixture", Usage: agent.Usage{TotalTokens: 12}}, nil
}
func fixture(t *testing.T) (*Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scheduler.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &Service{Store: store, Source: &fakeSource{fn: func(Job) ([]Quote, error) { return nil, nil }}}, path
}
func create(t *testing.T, s *Service) Job {
	t.Helper()
	j, err := s.Create(context.Background(), Spec{Name: "Test", ConnectionID: "source", Tickers: []string{"SBER"}, CollectSeconds: 60, SummarySeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func TestDurableScheduleAggregatesAndDeduplicates(t *testing.T) {
	s, path := fixture(t)
	j := create(t, s)
	now := time.Unix(j.Created, 0)
	source := s.Source.(*fakeSource)
	model := &fakeSummary{}
	s.LLM = model
	source.fn = func(Job) ([]Quote, error) {
		price := 100.
		stamp := now
		if source.calls > 1 {
			price = 110
			stamp = now.Add(time.Minute)
		}
		return []Quote{{Ticker: "SBER", Price: price, SourceTime: stamp.UTC().Format(time.RFC3339), TradingStatus: "T"}}, nil
	}
	if err := s.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	s.Store.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s.Store = reopened
	if err = s.Tick(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	summaries, err := s.Store.Summaries(j.ID)
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summaries: %+v %v", summaries, err)
	}
	stat := summaries[0].Stats[0]
	if stat.Count != 2 || stat.First != 100 || stat.Last != 110 || stat.ChangePercent != 10 || stat.Min != 100 || stat.Max != 110 {
		t.Fatalf("bad aggregation: %+v", stat)
	}
	if model.calls != 1 || summaries[0].Tokens != 12 {
		t.Fatal("no model summary")
	}
	if err = s.Tick(context.Background(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	summaries, _ = s.Store.Summaries(j.ID)
	if len(summaries) != 2 || len(summaries[0].Stats) != 0 || model.calls != 1 {
		t.Fatalf("duplicate created new observations: %+v", summaries)
	}
	// Running the same due timestamp twice must not execute again.
	calls := source.calls
	if err = s.Tick(context.Background(), now.Add(2*time.Minute)); err != nil || source.calls != calls {
		t.Fatal("duplicate run")
	}
}
func TestIdempotencyValidationAndPause(t *testing.T) {
	s, _ := fixture(t)
	j := create(t, s)
	other := create(t, s)
	if other.ID != j.ID {
		t.Fatal("duplicate job")
	}
	spec := j.Spec
	spec.CollectSeconds = 0
	if _, err := s.Create(context.Background(), spec); err == nil {
		t.Fatal("invalid interval")
	}
	spec = j.Spec
	spec.Tickers = []string{"SBER", "SBER"}
	if _, err := s.Create(context.Background(), spec); err == nil {
		t.Fatal("duplicate ticker")
	}
	if _, err := s.Store.Pause(j.ID, 0, true, j.Created); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted old version")
	}
	paused, err := s.Store.Pause(j.ID, j.Version, true, j.Created)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(context.Background(), time.Unix(j.Created+3600, 0)); err != nil || s.Source.(*fakeSource).calls != 0 {
		t.Fatal("paused executed")
	}
	resumed, err := s.Store.Pause(j.ID, paused.Version, false, j.Created+3600)
	if err != nil || resumed.NextCollect != j.Created+3600 || resumed.SummaryFrom != j.Created+3600 {
		t.Fatal("resume did not reset period")
	}
}
func TestMissedIntervalsCoalesceAndModelFailureFallsBack(t *testing.T) {
	s, _ := fixture(t)
	j := create(t, s)
	now := time.Unix(j.Created+3600, 0)
	s.LLM = &fakeSummary{fail: true}
	s.Source.(*fakeSource).fn = func(Job) ([]Quote, error) {
		return []Quote{{Ticker: "SBER", Price: 100, SourceTime: now.UTC().Format(time.RFC3339), TradingStatus: "T"}}, nil
	}
	if err := s.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	updated, _ := s.Store.Get(j.ID)
	if updated.NextCollect <= now.Unix() || updated.NextSummary <= now.Unix() {
		t.Fatal("catch-up storm")
	}
	history, _ := s.History(j.ID)
	if len(history.Summaries) != 1 || !strings.Contains(history.Summaries[0].Text, "100.00") || history.Summaries[0].Warning == "" {
		t.Fatalf("fallback missing: %+v", history)
	}
}
func TestSourceFailuresRetryThenBackoff(t *testing.T) {
	s, _ := fixture(t)
	j := create(t, s)
	source := s.Source.(*fakeSource)
	source.fn = func(Job) ([]Quote, error) { return nil, errors.New("source offline") }
	for _, offset := range []int64{0, 30, 90} {
		if err := s.Tick(context.Background(), time.Unix(j.Created+offset, 0)); err != nil {
			t.Fatal(err)
		}
	}
	updated, _ := s.Store.Get(j.ID)
	if source.calls != 3 || updated.Failures != 3 || updated.NextCollect != j.Created+150 {
		t.Fatalf("bad retries: %+v calls=%d", updated, source.calls)
	}
	history, _ := s.History(j.ID)
	if len(history.Summaries) == 0 || len(history.Summaries[0].Stats) > 0 || !strings.Contains(history.Summaries[0].Text, "новых котировок") {
		t.Fatal("invented data on source failure")
	}
}
func TestLeaseAndPauseDuringRun(t *testing.T) {
	s, path := fixture(t)
	j := create(t, s)
	now := time.Unix(j.Created, 0)
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	source := s.Source.(*fakeSource)
	source.fn = func(Job) ([]Quote, error) {
		close(started)
		<-release
		return []Quote{{Ticker: "SBER", Price: 1, SourceTime: now.Format(time.RFC3339)}}, nil
	}
	done := make(chan error, 1)
	go func() { done <- s.Tick(context.Background(), now) }()
	<-started
	if _, _, err = second.claim(now.Unix()); err == nil {
		t.Fatal("second worker acquired leased job")
	}
	if _, err = second.Pause(j.ID, j.Version, true, now.Unix()); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	rows, _ := second.quotes(j.ID, 0, now.Unix()+1)
	if len(rows) != 0 {
		t.Fatal("paused lease committed results")
	}
}
func TestAbandonedLeaseExpires(t *testing.T) {
	s, _ := fixture(t)
	j := create(t, s)
	_, _, err := s.Store.claim(j.Created)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Store.claim(j.Created + 299); err == nil {
		t.Fatal("active lease stolen")
	}
	if _, _, err = s.Store.claim(j.Created + 301); err != nil {
		t.Fatal(err)
	}
}
func TestSchedulerMCPPermissionAndRoundTrip(t *testing.T) {
	s, _ := fixture(t)
	p := &Provider{Service: s, Server: s.MCP(nil)}
	ctx := context.Background()
	defs, runs := p.Tools(ctx)
	if len(runs) > 0 || len(defs) != 3 {
		t.Fatalf("read tools: %+v %+v", defs, runs)
	}
	for _, d := range defs {
		if d.Mutates {
			t.Fatal("write tool exposed without grant")
		}
	}
	if err := s.Store.SetAllowChat(true); err != nil {
		t.Fatal(err)
	}
	defs, _ = p.Tools(ctx)
	var createDef, getDef, pauseDef agent.ToolDefinition
	for _, d := range defs {
		switch d.ToolName {
		case "create_scheduled_task":
			createDef = d
		case "get_task_results":
			getDef = d
		case "pause_scheduled_task":
			pauseDef = d
		}
	}
	if !createDef.Mutates || !pauseDef.Mutates || getDef.Mutates {
		t.Fatal("wrong mutation annotation")
	}
	out, err := p.Call(ctx, createDef, `{"name":"MCP","connectionId":"source","tickers":["SBER"],"collectSeconds":60,"summarySeconds":60}`)
	if err != nil || !strings.Contains(out, "nextSummary") {
		t.Fatalf("create MCP: %s %v", out, err)
	}
	jobs, _ := s.Store.List()
	if len(jobs) != 1 {
		t.Fatal("not persisted")
	}
	data, _ := json.Marshal(map[string]any{"jobId": jobs[0].ID})
	out, err = p.Call(ctx, getDef, string(data))
	if err != nil || !strings.Contains(out, "summaries") {
		t.Fatalf("get: %s %v", out, err)
	}
	if err = s.Store.SetAllowChat(false); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Call(ctx, pauseDef, `{"jobId":"x","version":1,"paused":true}`); err == nil {
		t.Fatal("revoked permission executed")
	}
}
func TestISSQuotesParser(t *testing.T) {
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	r := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"marketdata":{"columns":["LAST","SECID","SYSTIME","TRADINGSTATUS","BOARDID"],"data":[[100,"SBER","2026-09-27 12:59:00","T","TQBR"],[null,"GAZP","2026-09-27 12:59:00","N","TQBR"],[300,"LKOH","12:59:00","T","TQBR"]]}}`}}}
	quotes, err := parseQuotes(r, []string{"SBER", "GAZP", "LKOH"}, now)
	if len(quotes) != 1 || quotes[0].SourceTime != "2026-09-27T09:59:00Z" || err == nil {
		t.Fatalf("quotes: %+v %v", quotes, err)
	}
	if !strings.Contains(err.Error(), "GAZP") || !strings.Contains(err.Error(), "LKOH") {
		t.Fatal("missing tickers not reported")
	}
}
func TestRunStopsWithContext(t *testing.T) {
	s, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.Run(ctx, func(err error) { t.Error(err) }) }()
	cancel()
	wg.Wait()
}
