package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-chat-cli/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type taskExecutor struct {
	calls int
	err   error
}

func (*taskExecutor) Catalog(context.Context, string) (Catalog, error) {
	return Catalog{Source: SourceInfo{"source", "Mock tasks", 1}, Tools: []Tool{{Name: "list_issues", InputSchema: json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["open","closed"]}},"required":["status"],"additionalProperties":false}`)}}}, nil
}
func (e *taskExecutor) Execute(context.Context, Job) (json.RawMessage, error) {
	e.calls++
	return json.RawMessage(`{"structuredContent":{"issues":[{"id":"ISS-1","status":"open"}]}}`), e.err
}
func taskSpec() Spec {
	return Spec{Name: "Open issues", ConnectionID: "source", ToolName: "list_issues", Arguments: map[string]any{"status": "open"}, Schedule: Schedule{Kind: "interval", IntervalSeconds: 60}, SummaryMode: "period", SummarySeconds: 120}
}
func TestGenericTaskPersistenceAndSummary(t *testing.T) {
	s, path := fixture(t)
	executor := &taskExecutor{}
	s.Executor = executor
	s.LLM = &fakeSummary{}
	j, err := s.Create(context.Background(), taskSpec())
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.Create(context.Background(), taskSpec())
	if err != nil || duplicate.ID != j.ID {
		t.Fatal("idempotency", err)
	}
	now := time.Unix(j.Created, 0)
	if err = s.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	s.Store.Close()
	s.Store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Store.Close()
	if err = s.Tick(context.Background(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	h, err := s.History(j.ID)
	if err != nil || len(h.Runs) != 2 || len(h.Summaries) != 1 || !strings.Contains(string(h.Runs[0].Result), "ISS-1") || h.Summaries[0].Model != "fixture" {
		t.Fatalf("history: %+v %v", h, err)
	}
	if h.Job.NextCollect <= now.Add(2*time.Minute).Unix() {
		t.Fatal("missed intervals replayed")
	}
	paused, err := s.Store.Pause(j.ID, 1, true, now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.Update(context.Background(), j.ID, paused.Version, taskSpec())
	if err != nil || !updated.Paused {
		t.Fatal("update lost pause", err)
	}
	if _, err = s.Update(context.Background(), j.ID, 1, taskSpec()); !errors.Is(err, ErrConflict) {
		t.Fatal("stale edit accepted")
	}
	if err = s.Store.Delete(j.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	runs, _ := s.Store.Runs(j.ID)
	summaries, _ := s.Store.Summaries(j.ID)
	if len(runs)+len(summaries) != 0 {
		t.Fatal("orphaned history")
	}
}
func TestOnceRetriesAndNoSummary(t *testing.T) {
	s, _ := fixture(t)
	e := &taskExecutor{err: errors.New("offline")}
	s.Executor = e
	spec := taskSpec()
	spec.Schedule = Schedule{Kind: "once", At: time.Now().Unix() + 60}
	spec.SummaryMode = "none"
	j, err := s.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(context.Background(), time.Unix(j.NextCollect-1, 0)); err != nil || e.calls != 0 {
		t.Fatal("early execution")
	}
	for _, offset := range []int64{0, 30, 90} {
		if err = s.Tick(context.Background(), time.Unix(spec.Schedule.At+offset, 0)); err != nil {
			t.Fatal(err)
		}
	}
	h, _ := s.History(j.ID)
	if !h.Job.Completed || len(h.Runs) != 3 || len(h.Summaries) != 0 {
		t.Fatalf("once retries: %+v", h)
	}
	if err = s.Tick(context.Background(), time.Unix(spec.Schedule.At+3600, 0)); err != nil || e.calls != 3 {
		t.Fatal("completed job ran again")
	}
	if _, err = s.Store.Pause(j.ID, h.Job.Version, false, time.Now().Unix()); err == nil {
		t.Fatal("completed task resumed without new schedule")
	}
}
func TestDailyZonesAndDST(t *testing.T) {
	for _, tc := range []struct{ zone, clock, now, want string }{
		{"Asia/Novosibirsk", "18:00", "2026-09-27T10:00:00Z", "2026-09-27T11:00:00Z"},
		{"Europe/Moscow", "18:00", "2026-09-27T15:00:00Z", "2026-09-28T15:00:00Z"},
		{"America/New_York", "02:30", "2026-03-08T05:00:00Z", "2026-03-09T06:30:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		actual := time.Unix(nextDaily(Schedule{Kind: "daily", Timezone: tc.zone, Time: tc.clock}, now), 0).UTC().Format(time.RFC3339)
		if actual != tc.want {
			t.Fatalf("%s: %s != %s", tc.zone, actual, tc.want)
		}
	}
	// A repeated autumn minute must not run twice on the same day.
	loc, _ := time.LoadLocation("America/New_York")
	first := time.Date(2026, 11, 1, 1, 30, 0, 0, loc)
	next := time.Unix(nextDaily(Schedule{Timezone: loc.String(), Time: "01:30"}, first), 0).In(loc)
	if next.Day() != 2 {
		t.Fatal("repeated DST hour executed twice")
	}
}
func TestTaskValidationPreviewAndRunNow(t *testing.T) {
	s, _ := fixture(t)
	e := &taskExecutor{}
	s.Executor = e
	for _, change := range []func(*Spec){func(v *Spec) { v.Arguments["status"] = "wrong" }, func(v *Spec) { v.ToolName = "delete_issue" }, func(v *Spec) { v.Schedule.IntervalSeconds = 0 }, func(v *Spec) { v.Schedule = Schedule{Kind: "daily", Time: "18:00", Timezone: "No/SuchZone"} }, func(v *Spec) { v.SummaryMode = "bad" }} {
		spec := taskSpec()
		change(&spec)
		if _, err := s.Create(context.Background(), spec); err == nil {
			t.Fatal("invalid accepted", spec)
		}
	}
	if _, err := s.Preview(context.Background(), taskSpec()); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.Store.List()
	if len(jobs) != 0 || e.calls != 1 {
		t.Fatal("preview created job")
	}
	spec := taskSpec()
	spec.Schedule = Schedule{Kind: "daily", Time: "18:00", Timezone: "Europe/Moscow"}
	spec.SummaryMode = "each"
	j, err := s.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := s.Store.RunOnce(j.ID, j.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	h, _ := s.History(j.ID)
	if len(h.Runs) != 1 || len(h.Summaries) != 1 || h.Job.NextCollect != j.NextCollect || h.Job.RunNow {
		t.Fatalf("manual run changed schedule: %+v", h)
	}
	if _, err = s.Store.RunOnce(j.ID, queued.Version-1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale manual run")
	}
}
func TestMCPExecutorRealRoundTripAndRevision(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "mock", Version: "1"}, nil)
	type args struct {
		Status string `json:"status"`
	}
	type response struct {
		Total int `json:"total"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "list_issues", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest, args) (*mcp.CallToolResult, response, error) {
		return nil, response{3}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "delete_issue"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return nil, struct{}{}, nil
	})
	endpoint := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer endpoint.Close()
	connections, err := mcpclient.NewStore(filepath.Join(t.TempDir(), "connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := connections.Save(mcpclient.Connection{Name: "Mock", URL: endpoint.URL}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := fixture(t)
	s.Executor = &MCPExecutor{Store: connections}
	spec := taskSpec()
	spec.ConnectionID = c.ID
	cat, err := s.Executor.Catalog(context.Background(), c.ID)
	if err != nil || len(cat.Tools) != 1 {
		t.Fatalf("read-only discovery: %+v %v", cat, err)
	}
	j, err := s.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(context.Background(), time.Unix(j.Created, 0)); err != nil {
		t.Fatal(err)
	}
	runs, _ := s.Store.Runs(j.ID)
	if !strings.Contains(string(runs[0].Result), `"total":3`) {
		t.Fatalf("missing source response %s", runs[0].Result)
	}
	if _, err = connections.Save(c, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Executor.Execute(context.Background(), j); err == nil {
		t.Fatal("changed connection executed")
	}
}

func TestGenericAgentProviderFlow(t *testing.T) {
	s, _ := fixture(t)
	s.Executor = &taskExecutor{}
	s.Store.SetAllowChat(true)
	p := &Provider{Service: s, Server: s.MCP(nil)}
	defs, runs := p.Tools(context.Background())
	if len(runs) != 0 || len(defs) != 8 {
		t.Fatal("catalog", defs, runs)
	}
	call := func(name string, args any) string {
		t.Helper()
		for _, def := range defs {
			if def.ToolName == name {
				b, _ := json.Marshal(args)
				out, err := p.Call(context.Background(), def, string(b))
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				return out
			}
		}
		t.Fatal("tool missing", name)
		return ""
	}
	if out := call("list_schedulable_tools", map[string]any{"connectionId": "source"}); !strings.Contains(out, "list_issues") {
		t.Fatal(out)
	}
	call("create_scheduled_task", taskSpec())
	jobs, _ := s.Store.List()
	j := jobs[0]
	if err := s.Tick(context.Background(), time.Unix(j.Created, 0)); err != nil {
		t.Fatal(err)
	}
	if out := call("get_task_results", map[string]any{"jobId": j.ID}); !strings.Contains(out, "ISS-1") {
		t.Fatal("raw result lost", out)
	}
	call("run_scheduled_task", map[string]any{"jobId": j.ID, "version": j.Version})
	j, _ = s.Store.Get(j.ID)
	call("update_scheduled_task", map[string]any{"jobId": j.ID, "version": j.Version, "spec": taskSpec()})
	j, _ = s.Store.Get(j.ID)
	call("delete_scheduled_task", map[string]any{"jobId": j.ID, "version": j.Version})
	jobs, _ = s.Store.List()
	if len(jobs) != 0 {
		t.Fatal("delete did not persist")
	}
	s.Store.SetAllowChat(false)
	for _, def := range defs {
		if def.Mutates {
			if _, err := p.Call(context.Background(), def, `{}`); err == nil {
				t.Fatal("revoked permission", def.Name)
			}
		}
	}
}

func TestMarketProcessorComputesFromRawResults(t *testing.T) {
	s, _ := fixture(t)
	j := Job{Spec: Spec{ToolName: "moex_quotes", Processor: "market", Arguments: map[string]any{"tickers": []string{"SBER"}}}, SummaryFrom: 100}
	raw := func(price int, stamp string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"structuredContent": map[string]any{"marketdata": map[string]any{"columns": []string{"SECID", "LAST", "SYSTIME", "TRADINGSTATUS"}, "data": [][]any{{"SBER", price, stamp, "T"}}}}})
		return b
	}
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	first := raw(100, "2026-09-27T09:58:00Z")
	last := raw(110, "2026-09-27T09:59:00Z")
	summary := s.summarize(context.Background(), j, []Run{{Result: first}, {Result: first}, {Result: last}}, now.Unix())
	if len(summary.Stats) != 1 || summary.Stats[0].Count != 2 || summary.Stats[0].ChangePercent != 10 {
		t.Fatalf("market aggregate: %+v", summary)
	}
}
