package mcpclient

import (
	"codex-chat-cli/internal/agent"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProviderExecutesAllowedValidatedToolAndRevokes(t *testing.T) {
	var calls atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "mock", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "get_issue", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
		Key string `json:"key"`
	}) (*mcp.CallToolResult, any, error) {
		calls.Add(1)
		return nil, map[string]string{"key": args.Key, "blockedBy": "DEMO-100"}, nil
	})
	remote := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer remote.Close()
	store, err := NewStore(filepath.Join(t.TempDir(), "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := store.Save(Connection{Name: "Mock", URL: remote.URL}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	p := NewProvider(store)
	ctx := context.Background()
	defs, _ := p.Tools(ctx)
	if len(defs) != 0 {
		t.Fatal("enabled by default")
	}
	if err = store.SetPermissions(c.ID, c.Version, true, []string{"get_issue"}); err != nil {
		t.Fatal(err)
	}
	defs, runs := p.Tools(ctx)
	if len(defs) != 1 || len(runs) > 0 {
		t.Fatalf("catalog: %+v %+v", defs, runs)
	}
	if _, err = p.Call(ctx, defs[0], `{"key":123}`); err == nil {
		t.Fatal("invalid args accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("bad arguments sent")
	}
	output, err := p.Call(ctx, defs[0], `{"key":"DEMO-101"}`)
	if err != nil || !strings.Contains(output, "DEMO-100") || calls.Load() != 1 {
		t.Fatalf("call: %s %v", output, err)
	}
	if err = store.SetPermissions(c.ID, defs[0].Version, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Call(ctx, defs[0], `{"key":"DEMO-101"}`); err == nil || calls.Load() != 1 {
		t.Fatal("revoked call executed")
	}
}

func TestProviderAllowsOnlyCompanionReportWriterMutation(t *testing.T) {
	falseValue := false
	var writes atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "mock-issue-mcp", Version: "1.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "save_issue_report", Annotations: &mcp.ToolAnnotations{DestructiveHint: &falseValue, OpenWorldHint: &falseValue}}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}) (*mcp.CallToolResult, any, error) {
		writes.Add(1)
		return nil, map[string]any{"path": "reports/" + args.Filename, "bytes": len(args.Content)}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "other_write", Annotations: &mcp.ToolAnnotations{DestructiveHint: &falseValue, OpenWorldHint: &falseValue}}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		t.Fatal("unapproved mutation executed")
		return nil, nil, nil
	})
	remote := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer remote.Close()
	store, _ := NewStore(filepath.Join(t.TempDir(), "mcp.json"))
	connection, _ := store.Save(Connection{Name: "Mock", URL: remote.URL}, "", false)
	if err := store.SetPermissions(connection.ID, connection.Version, true, []string{"save_issue_report", "other_write"}); err != nil {
		t.Fatal(err)
	}
	provider := NewProvider(store)
	definitions, runs := provider.Tools(context.Background())
	if len(definitions) != 1 || definitions[0].ToolName != "save_issue_report" || !definitions[0].Mutates || len(runs) != 1 || runs[0].Name != "other_write" {
		t.Fatalf("catalog: %+v runs=%+v", definitions, runs)
	}
	if _, err := provider.Call(context.Background(), agent.ToolDefinition{ServerID: connection.ID, Version: definitions[0].Version, ToolName: "other_write", Mutates: true}, `{}`); err == nil {
		t.Fatal("other mutating tool reached provider")
	}
	if _, err := provider.Call(context.Background(), definitions[0], `{"filename":"report.md","content":"ok"}`); err != nil || writes.Load() != 1 {
		t.Fatalf("write: %v calls=%d", err, writes.Load())
	}
}
func TestEndpointExplicitHTTPAllowlist(t *testing.T) {
	t.Setenv("MCP_HTTP_ENDPOINTS", "http://mock-issue-mcp:8090/mcp")
	if err := ValidateEndpoint("http://mock-issue-mcp:8090/mcp"); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"http://mock-issue-mcp:8090/other", "http://mock-issue-mcp.evil:8090/mcp", "http://example.com/mcp"} {
		if ValidateEndpoint(url) == nil {
			t.Fatalf("accepted %s", url)
		}
	}
}
func TestEditingEndpointOrTokenClearsGrants(t *testing.T) {
	for _, change := range []string{"url", "token", "name"} {
		t.Run(change, func(t *testing.T) {
			s, _ := NewStore(filepath.Join(t.TempDir(), "mcp.json"))
			c, _ := s.Save(Connection{Name: "Mock", URL: "http://localhost:8090/mcp"}, "old", false)
			if err := s.SetPermissions(c.ID, c.Version, true, []string{"get_issue"}); err != nil {
				t.Fatal(err)
			}
			c.Version++
			token := ""
			switch change {
			case "url":
				c.URL = "http://localhost:8091/mcp"
			case "token":
				token = "new"
			case "name":
				c.Name = "Renamed"
			}
			saved, err := s.Save(c, token, false)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Enabled != (change == "name") {
				t.Fatalf("grants: %+v", saved)
			}
		})
	}
}
