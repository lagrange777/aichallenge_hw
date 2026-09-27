package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"codex-chat-cli/internal/scheduler"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type monitorSource struct{}

func (monitorSource) Resolve(context.Context, string) (scheduler.SourceInfo, error) {
	return scheduler.SourceInfo{ID: "test", Name: "Fixture", Version: 1}, nil
}
func (monitorSource) Collect(context.Context, scheduler.Job) ([]scheduler.Quote, error) {
	return nil, nil
}
func TestMonitorAPIAndMCPHTTP(t *testing.T) {
	store, err := scheduler.Open(filepath.Join(t.TempDir(), "scheduler.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := &scheduler.Service{Store: store, Source: monitorSource{}}
	handler := NewHandlerWithScheduler(&fakeLLM{}, "test", nil, nil, service)
	call := func(method, path, body string, csrf bool, origin string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if csrf {
			r.Header.Set("X-Codex-Chat", "1")
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if code, _ := call("GET", "/api/monitors", "", false, ""); code != 403 {
		t.Fatal("unprotected read")
	}
	if code, _ := call("POST", "/api/monitors/settings", `{"enabled":true}`, true, "https://evil.example"); code != 403 {
		t.Fatal("CSRF grant")
	}
	if code, _ := call("POST", "/api/monitors/create", `{"unknown":true}`, true, ""); code != 400 {
		t.Fatal("unknown input accepted")
	}
	code, body := call("POST", "/api/monitors/create", `{"name":"Test","connectionId":"test","tickers":["SBER"],"collectSeconds":60,"summarySeconds":60}`, true, "")
	if code != 200 {
		t.Fatalf("create: %d %s", code, body)
	}
	var job scheduler.Job
	json.Unmarshal([]byte(body), &job)
	if code, _ = call("GET", "/api/monitors?id="+job.ID, "", true, ""); code != 200 {
		t.Fatal("history")
	}
	if code, _ = call("POST", "/api/monitors/pause", `{"id":"`+job.ID+`","version":0,"paused":true}`, true, ""); code != 409 {
		t.Fatal("stale pause accepted")
	}
	if code, _ = call("POST", "/mcp/scheduler", `{}`, false, ""); code != 403 {
		t.Fatal("unprotected MCP")
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	// Custom header is mandatory even for local HTTP MCP clients.
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp/scheduler", HTTPClient: headerClient(server.Client()), DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 8 {
		t.Fatalf("MCP tools: %+v %v", tools, err)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_scheduled_tasks", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("MCP call: %+v %v", result, err)
	}
}

type monitorHeaderTransport struct{ base http.RoundTripper }

func (t monitorHeaderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Codex-Chat", "1")
	return t.base.RoundTrip(r)
}
func headerClient(c *http.Client) *http.Client {
	copy := *c
	copy.Transport = monitorHeaderTransport{c.Transport}
	return &copy
}

type genericMonitorExecutor struct{}

func (genericMonitorExecutor) Catalog(context.Context, string) (scheduler.Catalog, error) {
	return scheduler.Catalog{Source: scheduler.SourceInfo{ID: "test", Name: "Mock", Version: 1}, Tools: []scheduler.Tool{{Name: "list", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}}, nil
}
func (genericMonitorExecutor) Execute(context.Context, scheduler.Job) (json.RawMessage, error) {
	return json.RawMessage(`{"structuredContent":{"total":3}}`), nil
}
func TestGenericMonitorHTTPActions(t *testing.T) {
	store, err := scheduler.Open(filepath.Join(t.TempDir(), "scheduler.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := &scheduler.Service{Store: store, Executor: genericMonitorExecutor{}}
	handler := NewHandlerWithScheduler(&fakeLLM{}, "test", nil, nil, service)
	call := func(method, path string, body any) (int, []byte) {
		t.Helper()
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(b)))
		r.Header.Set("X-Codex-Chat", "1")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes()
	}
	if code, _ := call("GET", "/api/monitors?connectionId=test", nil); code != 200 {
		t.Fatal("catalog", code)
	}
	spec := map[string]any{"name": "Generic", "connectionId": "test", "toolName": "list", "arguments": map[string]any{}, "schedule": map[string]any{"kind": "interval", "intervalSeconds": 60}, "summaryMode": "none"}
	if code, b := call("POST", "/api/monitors/preview", spec); code != 200 || !strings.Contains(string(b), `"total":3`) {
		t.Fatal("preview", code, string(b))
	}
	jobs, _ := store.List()
	if len(jobs) != 0 {
		t.Fatal("preview persisted a job")
	}
	code, b := call("POST", "/api/monitors/create", spec)
	if code != 200 {
		t.Fatal(code, string(b))
	}
	var job scheduler.Job
	json.Unmarshal(b, &job)
	spec["id"] = job.ID
	spec["version"] = job.Version
	spec["name"] = "Changed"
	code, b = call("POST", "/api/monitors/update", spec)
	if code != 200 {
		t.Fatal("update", code, string(b))
	}
	json.Unmarshal(b, &job)
	if code, _ = call("POST", "/api/monitors/update", spec); code != 409 {
		t.Fatal("stale edit", code)
	}
	code, b = call("POST", "/api/monitors/run", map[string]any{"id": job.ID, "version": job.Version})
	if code != 200 {
		t.Fatal("run", code, string(b))
	}
	json.Unmarshal(b, &job)
	code, b = call("POST", "/api/monitors/delete", map[string]any{"id": job.ID, "version": job.Version})
	if code != 200 {
		t.Fatal("delete", code, string(b))
	}
	if code, _ = call("GET", "/api/monitors?id="+job.ID, nil); code != 404 {
		t.Fatal("deleted job available")
	}
}
