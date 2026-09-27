package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"codex-chat-cli/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPSettingsAndDiscoveryAPI(t *testing.T) {
	remoteMCP := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	mcp.AddTool(remoteMCP, &mcp.Tool{Name: "list_items", Description: "<script>test</script>"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		t.Error("unexpected tool execution")
		return nil, nil, nil
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return remoteMCP }, &mcp.StreamableHTTPOptions{Stateless: true})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-key" {
			http.Error(w, "bad key", 401)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	defer remote.Close()
	path := filepath.Join(t.TempDir(), "mcp.json")
	store, err := mcpclient.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithMCP(&fakeLLM{}, "test-model", nil, store)
	call := func(path string, body any, header bool, origin string) (int, apiResponse) {
		t.Helper()
		encoded, _ := json.Marshal(body)
		method := "POST"
		if body == nil {
			method = "GET"
		}
		r := httptest.NewRequest(method, path, strings.NewReader(string(encoded)))
		r.Header.Set("Content-Type", "application/json")
		if header {
			r.Header.Set("X-Codex-Chat", "1")
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "private-key") {
			t.Fatal("secret leaked to browser")
		}
		var payload apiResponse
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return w.Code, payload
	}
	body := map[string]any{"name": "Demo", "url": remote.URL, "token": "private-key"}
	if code, _ := call("/api/mcp/save", body, false, ""); code != 403 {
		t.Fatal("missing CSRF header accepted")
	}
	if code, _ := call("/api/mcp/save", body, true, "https://evil.example"); code != 403 {
		t.Fatal("cross-origin accepted")
	}
	if code, _ := call("/api/mcp", nil, false, ""); code != 403 {
		t.Fatal("unprotected settings read")
	}
	code, payload := call("/api/mcp/save", body, true, "")
	if code != 200 || len(payload.MCP.Connections) != 1 {
		t.Fatalf("save: %d %+v", code, payload)
	}
	c := payload.MCP.Connections[0]
	if !c.HasToken {
		t.Fatal("token not saved")
	}
	store, err = mcpclient.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandlerWithMCP(&fakeLLM{}, "test-model", nil, store)
	code, payload = call("/api/mcp", nil, true, "")
	if code != 200 || len(payload.MCP.Connections) != 1 {
		t.Fatal("restart lost settings")
	}
	check := map[string]any{"id": c.ID, "version": c.Version}
	code, payload = call("/api/mcp/check", check, true, "")
	if code != 200 || payload.MCP.Discovery.ServerName != "fixture" || len(payload.MCP.Discovery.Tools) != 1 || payload.MCP.Discovery.Tools[0].Name != "list_items" {
		t.Fatalf("check: %d %+v", code, payload)
	}
	if code, _ = call("/api/mcp/check", map[string]any{"id": c.ID, "version": 0}, true, ""); code != 409 {
		t.Fatal("stale check accepted")
	}
	if code, _ = call("/api/mcp/delete", check, true, ""); code != 200 {
		t.Fatal("delete failed")
	}
	if code, _ = call("/api/mcp/check", check, true, ""); code != 409 {
		t.Fatal("deleted connection checked")
	}
}

func TestMCPPermissionsValidateDiscoveryAndVersion(t *testing.T) {
	remoteMCP := mcp.NewServer(&mcp.Implementation{Name: "permissions", Version: "1"}, nil)
	for _, name := range []string{"read", "write"} {
		mcp.AddTool(remoteMCP, &mcp.Tool{Name: name, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: name == "read"}}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			t.Error("permission save executed a tool")
			return nil, nil, nil
		})
	}
	remote := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return remoteMCP }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer remote.Close()
	store, _ := mcpclient.NewStore(filepath.Join(t.TempDir(), "mcp.json"))
	c, _ := store.Save(mcpclient.Connection{Name: "Mock", URL: remote.URL}, "", false)
	handler := NewHandlerWithMCP(&fakeLLM{}, "test", nil, store)
	grant := func(version int, enabled bool, names []string) int {
		b, _ := json.Marshal(map[string]any{"id": c.ID, "version": version, "enabled": enabled, "allowedTools": names})
		r := httptest.NewRequest("POST", "/api/mcp/permissions", strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Codex-Chat", "1")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	for _, name := range []string{"write", "missing"} {
		if code := grant(c.Version, true, []string{name}); code != 400 {
			t.Fatalf("accepted %s: %d", name, code)
		}
	}
	if code := grant(c.Version, true, []string{"read"}); code != 200 {
		t.Fatalf("grant: %d", code)
	}
	if code := grant(c.Version, false, nil); code != 409 {
		t.Fatalf("stale grant: %d", code)
	}
	saved, _, err := store.Get(c.ID, c.Version+1)
	if err != nil || !saved.Enabled || len(saved.AllowedTools) != 1 {
		t.Fatalf("not saved: %+v %v", saved, err)
	}
	if code := grant(saved.Version, false, nil); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
}
