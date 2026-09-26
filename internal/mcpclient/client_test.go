package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiscoverSDKServers(t *testing.T) {
	for _, stateless := range []bool{false, true} {
		t.Run(map[bool]string{false: "stateful", true: "stateless"}[stateless], func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0"}, nil)
			mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo a message"}, func(context.Context, *mcp.CallToolRequest, struct {
				Text string `json:"text"`
			}) (*mcp.CallToolResult, any, error) { t.Error("discovery must not call tools"); return nil, nil, nil })
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: stateless, JSONResponse: stateless})
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Error("missing bearer authentication")
					w.WriteHeader(401)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer remote.Close()
			result, err := Discover(context.Background(), remote.URL, "test-secret")
			if err != nil {
				t.Fatal(err)
			}
			if result.ServerName != "test-server" || len(result.Tools) != 1 || result.Tools[0].Name != "echo" || result.ProtocolVersion == "" {
				t.Fatalf("unexpected discovery: %+v", result)
			}
		})
	}
}

// Neurly-style stateless endpoint speaking the legacy initialize protocol.
func TestDiscoverLegacyPagination(t *testing.T) {
	var initialized atomic.Bool
	var pages atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch request.Method {
		case "server/discover":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
			return
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "legacy", "version": "1"}}
		case "notifications/initialized":
			initialized.Store(true)
			w.WriteHeader(202)
			return
		case "tools/list":
			if !initialized.Load() {
				t.Error("tools/list before initialized")
			}
			pages.Add(1)
			name, next := "first", "page2"
			if request.Params.Cursor == "page2" {
				name, next = "second", ""
			}
			result = map[string]any{"tools": []any{map[string]any{"name": name, "inputSchema": map[string]string{"type": "object"}}}, "nextCursor": next}
		default:
			t.Errorf("unexpected method %s", request.Method)
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer remote.Close()
	result, err := Discover(context.Background(), remote.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 2 || pages.Load() != 2 || result.Tools[1].Name != "second" {
		t.Fatalf("pagination failed: %+v", result)
	}
}

func TestDiscoverFailures(t *testing.T) {
	t.Run("auth does not expose response", func(t *testing.T) {
		remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret-reflected", 401) }))
		defer remote.Close()
		_, err := Discover(context.Background(), remote.URL, "secret-reflected")
		if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "secret-reflected") {
			t.Fatalf("unsafe error: %v", err)
		}
	})
	t.Run("redirect not followed", func(t *testing.T) {
		var called atomic.Bool
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true) }))
		defer target.Close()
		remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
		defer remote.Close()
		_, err := Discover(context.Background(), remote.URL, "private-key")
		if err == nil || called.Load() {
			t.Fatal("redirect followed")
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		started := time.Now()
		_, err := Discover(ctx, "http://127.0.0.1:1", "")
		if err == nil || time.Since(started) > time.Second {
			t.Fatalf("cancellation ignored: %v", err)
		}
	})
}

func TestValidateEndpoint(t *testing.T) {
	for _, endpoint := range []string{"https://neurly.ru/v1/mcp", "http://localhost:9000/mcp", "http://127.0.0.1:9000/mcp", "http://[::1]:9000/mcp"} {
		if err := ValidateEndpoint(endpoint); err != nil {
			t.Errorf("%s: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"file:///etc/passwd", "http://remote.example/mcp", "https://user:key@example.com/mcp", "https://example.com/mcp?token=key", "https://example.com/#key", "https:///mcp"} {
		if ValidateEndpoint(endpoint) == nil {
			t.Errorf("accepted %s", endpoint)
		}
	}
}
