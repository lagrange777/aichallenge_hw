package filework

import (
	"codex-chat-cli/internal/agent"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{"README.md": "Project\n", "docs/api.md": "Old endpoint\n", "internal/server.go": "GET /healthz\n", ".env": "TOP_SECRET", "data/private.md": "PRIVATE"} {
		p := filepath.Join(root, name)
		if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(body), 0644); e != nil {
			t.Fatal(e)
		}
	}
	s, e := New(root, t.TempDir(), nil, "test")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s, root
}
func task(s *Service) *Task {
	t := &Task{ID: ID(), Owner: "owner", Status: "running", Reads: map[string]File{}, Events: []Event{}, Patches: []Patch{}}
	s.tasks[t.ID] = t
	return t
}
func call(t *testing.T, s *Service, id, name string, args map[string]any) string {
	t.Helper()
	cs, close, e := s.connect(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	defer close()
	out, e := callMCP(context.Background(), cs, name, args)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func readThree(t *testing.T, s *Service, id string) {
	for _, p := range []string{"README.md", "docs/api.md", "internal/server.go"} {
		call(t, s, id, "read_file", map[string]any{"path": p})
	}
}
func TestMCPAndApplicationLifecycle(t *testing.T) {
	s, root := fixture(t)
	ts := task(s)
	names := call(t, s, ts.ID, "list_files", nil)
	if strings.Contains(names, ".env") || strings.Contains(names, "private.md") {
		t.Fatal(names)
	}
	matches := call(t, s, ts.ID, "search_files", map[string]any{"query": "endpoint"})
	if !strings.Contains(matches, "docs/api.md") {
		t.Fatal(matches)
	}
	readThree(t, s, ts.ID)
	proposal := call(t, s, ts.ID, "propose_patch", map[string]any{"path": "docs/api.md", "content": "GET /healthz\n"})
	if !strings.Contains(proposal, "proposed_not_applied") {
		t.Fatal(proposal)
	}
	b, _ := os.ReadFile(filepath.Join(root, "docs/api.md"))
	if string(b) != "Old endpoint\n" {
		t.Fatal("proposal wrote files")
	}
	diff := call(t, s, ts.ID, "get_diff", nil)
	if !strings.Contains(diff, "+GET /healthz") {
		t.Fatal(diff)
	}
	ts.Status = "ready"
	if _, e := s.Apply("other", ts.ID); e == nil {
		t.Fatal("foreign apply allowed")
	}
	if _, e := s.Get("other", ts.ID); e == nil {
		t.Fatal("foreign read allowed")
	}
	if _, e := s.Apply("owner", ts.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Apply("owner", ts.ID); e != nil {
		t.Fatal("repeat apply", e)
	}
	b, _ = os.ReadFile(filepath.Join(root, "docs/api.md"))
	if string(b) != "GET /healthz\n" {
		t.Fatal(string(b))
	}
	restored, e := New(root, s.state, nil, "test")
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	got, e := restored.Get("owner", ts.ID)
	if e != nil || got.Status != "applied" {
		t.Fatal(got, e)
	}
	// A repeat proposal for identical content produces no patch.
	again := task(s)
	readThree(t, s, again.ID)
	call(t, s, again.ID, "propose_patch", map[string]any{"path": "docs/api.md", "content": "GET /healthz\n"})
	if len(again.Patches) != 0 {
		t.Fatal("duplicate change")
	}
}
func TestConflictingSourceRejectsAllChanges(t *testing.T) {
	s, root := fixture(t)
	ts := task(s)
	readThree(t, s, ts.ID)
	call(t, s, ts.ID, "propose_patch", map[string]any{"path": "docs/adr/mcp.md", "content": "# MCP\n"})
	ts.Status = "ready"
	os.WriteFile(filepath.Join(root, "internal/server.go"), []byte("changed dependency"), 0644)
	if _, e := s.Apply("owner", ts.ID); e == nil {
		t.Fatal("stale dependency accepted")
	}
	if _, e := os.Stat(filepath.Join(root, "docs/adr/mcp.md")); !os.IsNotExist(e) {
		t.Fatal("wrote before preflight")
	}
}
func TestToolGuardsAndRootConfinement(t *testing.T) {
	s, root := fixture(t)
	ts := task(s)
	cs, close, e := s.connect(context.Background(), ts.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer close()
	if _, e := callMCP(context.Background(), cs, "propose_patch", map[string]any{"path": "docs/a.md", "content": "x"}); e == nil {
		t.Fatal("allowed without reads")
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret"), 0644)
	os.Symlink(outside, filepath.Join(root, "docs/link"))
	for _, p := range []string{"../README.md", ".env", "data/private.md", "/etc/passwd", "docs/link/secret.md", "docs/../README.md"} {
		if _, e := callMCP(context.Background(), cs, "read_file", map[string]any{"path": p}); e == nil {
			t.Fatal("accepted", p)
		}
	}
	readThree(t, s, ts.ID)
	for _, p := range []string{"internal/server.go", "docs/link/new.md", ".git/config"} {
		if _, e := callMCP(context.Background(), cs, "propose_patch", map[string]any{"path": p, "content": "bad"}); e == nil {
			t.Fatal("write accepted", p)
		}
	}
	if _, e := callMCP(context.Background(), cs, "apply_patch", nil); e == nil {
		t.Fatal("model has apply tool")
	}
}
func TestHTTPGuards(t *testing.T) {
	s, _ := fixture(t)
	h := s.Handler()
	for _, tc := range []struct {
		origin, header string
		want           int
	}{{"https://evil.test", "1", 403}, {"", "", 415}} {
		r := httptest.NewRequest("POST", "http://localhost/api/work/start", strings.NewReader(`{"goal":"Update API docs"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-File-Work", tc.header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/work/tasks", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal(w.Code)
	}
}

type scripted struct{ step int }

func (m *scripted) Complete(ctx context.Context, r agent.CompletionRequest) (agent.CompletionResponse, error) {
	m.step++
	if m.step == 1 {
		calls := []agent.ToolCall{}
		for i, p := range []string{"README.md", "docs/api.md", "internal/server.go"} {
			b, _ := json.Marshal(map[string]string{"path": p})
			calls = append(calls, agent.ToolCall{CallID: string(rune('a' + i)), Name: "read_file", Arguments: string(b)})
		}
		return agent.CompletionResponse{ResponseID: "one", ToolCalls: calls}, nil
	}
	if m.step == 2 {
		b, _ := json.Marshal(map[string]string{"path": "docs/api.md", "content": "GET /healthz\n"})
		return agent.CompletionResponse{ResponseID: "two", ToolCalls: []agent.ToolCall{{CallID: "d", Name: "propose_patch", Arguments: string(b)}}}, nil
	}
	if m.step == 3 {
		return agent.CompletionResponse{ResponseID: "finish", ToolCalls: []agent.ToolCall{{CallID: "f", Name: "finish_task", Arguments: `{"outcome":"ready","summary":"Diff готов: docs/api.md. Прочитаны три файла."}`}}}, nil
	}
	return agent.CompletionResponse{ResponseID: "three", Output: "Diff готов: docs/api.md. Прочитаны три файла."}, nil
}
func TestGoalDrivenAgentLoop(t *testing.T) {
	s, _ := fixture(t)
	s.llm = &scripted{}
	ts, e := s.Start("owner", "Update documentation from actual routes")
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, e := s.Get("owner", ts.ID)
		if e != nil {
			t.Fatal(e)
		}
		if got.Status != "running" {
			if got.Status != "ready" || len(got.Reads) != 3 || len(got.Patches) != 1 || got.Calls != 3 {
				t.Fatalf("%+v", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("agent did not finish")
}

type offersOnly struct{ step int }

func (m *offersOnly) Complete(ctx context.Context, r agent.CompletionRequest) (agent.CompletionResponse, error) {
	m.step++
	if m.step == 1 {
		return (&scripted{}).Complete(ctx, r)
	}
	return agent.CompletionResponse{ResponseID: "offer", Output: "Нашёл ошибку. Если хотите, подготовлю патч."}, nil
}
func TestAnOfferIsNotSuccessfulCompletion(t *testing.T) {
	s, _ := fixture(t)
	s.llm = &offersOnly{}
	ts, e := s.Start("owner", "Fix actual documentation discrepancies")
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.Get("owner", ts.ID)
		if got.Status != "running" {
			if got.Status != "failed" || got.Calls != 4 {
				t.Fatalf("offer falsely counted as success: %+v", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("agent did not finish")
}

// ServeMux matches URL.Path, so a query must not break /work navigation.
func TestWorkPageAcceptsGoalQuery(t *testing.T) {
	s, _ := fixture(t)
	r := httptest.NewRequest(http.MethodGet, "/work?goal=%2Fwork+Update+API+documentation", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "/work/app.js") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

type unavailableLLM struct{}

func (unavailableLLM) Complete(context.Context, agent.CompletionRequest) (agent.CompletionResponse, error) {
	// CompletionResponse is a value struct; its zero Usage is safe to read.
	return agent.CompletionResponse{}, context.DeadlineExceeded
}
func TestLLMErrorCompletesTaskAsFailed(t *testing.T) {
	s, _ := fixture(t)
	s.llm = unavailableLLM{}
	ts, e := s.Start("owner", "Update API documentation from source")
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.Get("owner", ts.ID)
		if got.Status != "running" {
			if got.Status != "failed" || got.Calls != 1 || !strings.Contains(got.Error, "deadline exceeded") {
				t.Fatalf("%+v", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("LLM failure left task running")
}
