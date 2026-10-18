// Package projectmcp exposes a fixed, read-only Git interface through MCP.
package projectmcp

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Branch struct {
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	Detached   bool   `json:"detached"`
	ObservedAt string `json:"observedAt"`
}
type Files struct {
	Files      []string `json:"files"`
	Truncated  bool     `json:"truncated"`
	ObservedAt string   `json:"observedAt"`
}

// Bounded output prevents a very large repository from filling memory.
type output struct{ data []byte }

func (b *output) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 1<<20 {
		return 0, errors.New("Git output too large")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func git(ctx context.Context, root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	argv := append([]string{"-c", "safe.directory=" + root, "-c", "core.fsmonitor=false", "-C", root}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	// Do not pass the application's credentials or user Git configuration.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}
	var out output
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return "", errors.New("Git context unavailable")
	}
	return strings.TrimSpace(string(out.data)), nil
}
func CurrentBranch(ctx context.Context, root string) (Branch, error) {
	commit, err := git(ctx, root, "rev-parse", "--verify", "HEAD")
	branch, branchErr := git(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil && branchErr != nil {
		return Branch{}, errors.New("Git repository unavailable")
	}
	return Branch{Branch: branch, Commit: commit, Detached: branchErr != nil, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}
func ProjectFiles(ctx context.Context, root string) (Files, error) {
	raw, err := git(ctx, root, "ls-files", "--cached", "-z")
	if err != nil {
		return Files{}, err
	}
	result := Files{Files: []string{}, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, name := range strings.Split(raw, "\x00") {
		if publicPath(name) {
			result.Files = append(result.Files, name)
		}
	}
	sort.Strings(result.Files)
	if len(result.Files) > 160 {
		result.Files = result.Files[:160]
		result.Truncated = true
	}
	return result, nil
}
func publicPath(name string) bool {
	if name == "" || strings.ContainsAny(name, "\r\n") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || part == "data" || part == "artifacts" || part == "tools" || part == "documents" {
			return false
		}
	}
	ext := filepath.Ext(name)
	return ext == ".go" || ext == ".md" || ext == ".yaml" || ext == ".js" || ext == ".html" || ext == ".css" || name == "go.mod" || name == "Dockerfile"
}
func New(root string) *mcp.Server {
	root, _ = filepath.Abs(root)
	s := mcp.NewServer(&mcp.Implementation{Name: "codex-project-mcp", Version: "1.0.0"}, nil)
	closed := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closed}
	mcp.AddTool(s, &mcp.Tool{Name: "git_branch", Description: "Read the live current branch, HEAD commit and detached-HEAD state of the configured project. No arguments, no modifications.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, Branch, error) {
		v, e := CurrentBranch(ctx, root)
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "project_files", Description: "List up to 160 tracked public source/documentation paths in the configured project. Excludes private and generated directories. Does not read file contents.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, Files, error) {
		v, e := ProjectFiles(ctx, root)
		return nil, v, e
	})
	return s
}
func Handler(root string) http.Handler {
	s := New(root)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		// This local service accepts programmatic clients, not cross-site browsers.
		if r.Header.Get("Origin") != "" {
			http.Error(w, "Origin forbidden", 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		h.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if _, err := CurrentBranch(r.Context(), root); err != nil {
			http.Error(w, "Git unavailable", 503)
			return
		}
		w.Write([]byte("ok\n"))
	})
	return mux
}
