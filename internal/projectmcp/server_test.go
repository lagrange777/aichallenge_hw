package projectmcp

import (
	"codex-chat-cli/internal/mcpclient"
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "initial", root)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("git init: %s %v", b, e)
	}
	for _, name := range []string{"README.md", ".env", "secret/.key", "internal/main.go"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700)
		os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0600)
	}
	cmd = exec.Command("git", "-C", root, "add", ".")
	if e := cmd.Run(); e != nil {
		t.Fatal(e)
	}
	return root
}
func TestLiveBranchUnbornDetachedAndFilesOverMCP(t *testing.T) {
	root := fixture(t)
	ctx := context.Background()
	server := httptest.NewServer(Handler(root))
	defer server.Close()
	d, err := mcpclient.Discover(ctx, server.URL+"/mcp", "")
	if err != nil || len(d.Tools) != 2 {
		t.Fatalf("discovery: %+v %v", d, err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	readBranch := func() Branch {
		result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: "git_branch", Arguments: map[string]any{}})
		if e != nil || result.IsError {
			t.Fatalf("call: %v %+v", e, result)
		}
		b, _ := json.Marshal(result.StructuredContent)
		var v Branch
		if json.Unmarshal(b, &v) != nil {
			t.Fatal("not structured")
		}
		return v
	}
	if v := readBranch(); v.Branch != "initial" || v.Commit != "" || v.Detached {
		t.Fatal(v)
	}
	if e := exec.Command("git", "-C", root, "symbolic-ref", "HEAD", "refs/heads/changed").Run(); e != nil {
		t.Fatal(e)
	}
	if v := readBranch(); v.Branch != "changed" {
		t.Fatal("cached branch", v)
	}
	cmd := exec.Command("git", "-C", root, "-c", "user.email=test@example.invalid", "-c", "user.name=Test", "commit", "-m", "test")
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("commit: %s %v", b, e)
	}
	if e := exec.Command("git", "-C", root, "checkout", "--detach").Run(); e != nil {
		t.Fatal(e)
	}
	if v := readBranch(); !v.Detached || v.Branch != "" || len(v.Commit) != 40 {
		t.Fatal(v)
	}
	files, e := ProjectFiles(ctx, root)
	if e != nil || len(files.Files) != 2 || strings.Contains(strings.Join(files.Files, " "), ".env") {
		t.Fatalf("%+v %v", files, e)
	}
	bad, e := session.CallTool(ctx, &mcp.CallToolParams{Name: "git_branch", Arguments: map[string]any{"command": "rm -rf /"}})
	if e == nil && (bad == nil || !bad.IsError) {
		t.Fatal("unexpected arguments accepted")
	}
}
func TestMCPSameOriginAndGitUnavailable(t *testing.T) {
	h := Handler(t.TempDir())
	r := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
