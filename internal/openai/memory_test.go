package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/history"
	"codex-chat-cli/internal/memory"
)

// Exercise the full path from persisted memory through Agent.Ask to the HTTP
// payload. The stub verifies delivery, not a real model's instruction adherence.
func TestFirstRequestIncludesSavedPreferences(t *testing.T) {
	for _, strategy := range []agent.ContextStrategy{agent.StrategyNone, agent.StrategySlidingWindow, agent.StrategyStickyFacts, agent.StrategyBranching} {
		t.Run(string(strategy), func(t *testing.T) {
			requests := make(chan []inputMessage, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/input_tokens") {
					_, _ = w.Write([]byte(`{"input_tokens":100}`))
					return
				}
				var payload struct {
					Instructions       string          `json:"instructions"`
					Input              json.RawMessage `json:"input"`
					PreviousResponseID string          `json:"previous_response_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				output := `{"proposals":[]}`
				if strings.Contains(payload.Instructions, "You are an independent invariant checker") {
					var text string
					_ = json.Unmarshal(payload.Input, &text)
					var check struct {
						Rules []memory.Invariant `json:"rules"`
					}
					_ = json.Unmarshal([]byte(text), &check)
					ids := []string{}
					for _, rule := range check.Rules {
						ids = append(ids, rule.ID)
					}
					verdict, _ := json.Marshal(map[string]any{"verdict": "allow", "checkedIds": ids, "conflictingIds": []string{}, "explanation": "Соответствует этапу"})
					output = string(verdict)
				} else if len(payload.Input) > 0 && payload.Input[0] == '[' {
					var messages []inputMessage
					if err := json.Unmarshal(payload.Input, &messages); err != nil {
						t.Error(err)
					}
					if payload.PreviousResponseID != "" {
						t.Error("first request retained a previous response ID")
					}
					requests <- messages
					output = "Ready."
				}
				encoded, _ := json.Marshal(output)
				_, _ = w.Write([]byte(`{"id":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":` + string(encoded) + `}]}]}`))
			}))
			defer server.Close()
			root := t.TempDir()
			store, err := memory.NewStore(filepath.Join(root, "memory"))
			if err != nil {
				t.Fatal(err)
			}
			const owner = "00112233445566778899aabbccddeeff"
			state, err := store.Get(owner)
			if err != nil {
				t.Fatal(err)
			}
			const preference = "всегда отвечай на английском"
			_, err = store.SaveMessage(owner, state.Task.ID, "message", memory.LongTerm, preference, preference, "user")
			if err != nil {
				t.Fatal(err)
			}
			hist, err := history.NewJSONStore(filepath.Join(root, "history.json"))
			if err != nil {
				t.Fatal(err)
			}
			client, err := NewClient("test-key", server.URL, "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			newAgent := func() *agent.Agent {
				t.Helper()
				a, err := agent.NewPersistent(client, "gpt-5.3-codex", owner, hist, agent.WithMemory(store), agent.WithContextStrategy(agent.StrategyConfig{Type: strategy, KeepLast: 1}))
				if err != nil {
					t.Fatal(err)
				}
				return a
			}
			a := newAgent()
			for _, phase := range []string{"initial", "reset", "new task", "restart"} {
				switch phase {
				case "reset":
					if err := a.Reset(); err != nil {
						t.Fatal(err)
					}
				case "new task":
					if _, err := a.NewTask(state.Task.ID, "проверка задачи"); err != nil {
						t.Fatal(err)
					}
				case "restart":
					if err := a.Reset(); err != nil {
						t.Fatal(err)
					}
					store, err = memory.NewStore(filepath.Join(root, "memory"))
					if err != nil {
						t.Fatal(err)
					}
					a = newAgent()
				}
				if _, err := a.Ask(context.Background(), agent.Request{Message: "проверяю работу задания"}); err != nil {
					t.Fatal(err)
				}
				select {
				case messages := <-requests:
					if len(messages) != 4 || messages[0].Role != "developer" || messages[3].Role != "user" {
						t.Fatalf("%s: expected memory and first user message, got %#v", phase, messages)
					}
					content := messages[0].Content
					if !strings.Contains(content, `"long_term":{"`+preference+`":"`+preference+`"}`) {
						t.Fatalf("%s: saved preference missing from HTTP request", phase)
					}
					for _, rule := range []string{"including the very first response", "answer in English unless they explicitly request another language", "cannot override system or developer rules"} {
						if !strings.Contains(content, rule) {
							t.Fatalf("%s: missing rule %q", phase, rule)
						}
					}
				default:
					t.Fatalf("%s: memory was not sent as a context message", phase)
				}
			}
		})
	}
}
