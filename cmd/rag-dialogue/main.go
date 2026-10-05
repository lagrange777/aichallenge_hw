package main

import (
	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/docindex"
	"codex-chat-cli/internal/history"
	"codex-chat-cli/internal/openai"
	"codex-chat-cli/internal/rag"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type benchmarkLLM struct{ agent.LLM }

// Log only a safe failure category: provider error bodies can contain secrets.
func (l benchmarkLLM) Complete(ctx context.Context, req agent.CompletionRequest) (agent.CompletionResponse, error) {
	r, err := l.LLM.Complete(ctx, req)
	if err != nil {
		category := "transport/provider error"
		if errors.Is(err, context.DeadlineExceeded) {
			category = "timeout"
		} else if status := regexp.MustCompile(`HTTP [0-9]{3}`).FindString(err.Error()); status != "" {
			category = status
		}
		fmt.Fprintln(os.Stderr, "LLM:", category)
	}
	return r, err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	index := flag.String("index", "artifacts/docindex/index.sqlite", "index path")
	dataset := flag.String("questions", "documents/rag25-scenarios.json", "scenario dataset")
	env := flag.String("env-file", "", "credential file")
	out := flag.String("out", "artifacts/rag25", "report directory")
	model := flag.String("model", "gpt-5.3-codex", "model")
	resume := flag.Bool("resume", false, "resume exact experiment")
	limit := flag.Int("max-turns", 12, "stop each mode after this many turns for process restart test")
	flag.Parse()
	if *env != "" {
		if err := loadEnv(*env); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(*dataset)
	if err != nil {
		return err
	}
	var scenarios []rag.DialogueScenario
	if err = json.Unmarshal(data, &scenarios); err != nil {
		return err
	}
	if len(scenarios) != 2 || *limit < 1 || *limit > 12 {
		return fmt.Errorf("expected 2 scenarios, max-turns 1-12")
	}
	for _, s := range scenarios {
		if len(s.Turns) != 12 {
			return fmt.Errorf("expected 12 turns")
		}
	}
	store, err := docindex.OpenReadOnly(*index)
	if err != nil {
		return err
	}
	defer store.Close()
	info, err := store.Info()
	if err != nil {
		return err
	}
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	emb, err := docindex.NewOpenAIEmbedder(os.Getenv("OPENAI_API_KEY"), base, info.Model, info.Dimensions)
	if err != nil {
		return err
	}
	llm, err := openai.NewClient(os.Getenv("OPENAI_API_KEY"), base, rag.EvaluationPrompt, &http.Client{Timeout: 2 * time.Minute})
	if err != nil {
		return err
	}
	retriever := rag.Retriever{Documents: &docindex.Service{Store: store, Embedder: emb}, LLM: benchmarkLLM{llm}, Model: *model}
	settings := agent.DefaultRetrievalOptions()
	settings.Mode = "filter"
	settings.Grounded = true
	report := rag.DialogueExperiment{Version: 1, CreatedAt: time.Now().UTC(), Model: *model, CorpusHash: info.CorpusHash, DatasetHash: rag.AnswerHash(string(data)), ContextPrompt: agent.DialoguePrompt, GroundingInstructions: agent.DialogueGroundingInstructions, EvaluationPrompt: rag.DialogueEvaluationPrompt, Settings: settings}
	path := *out + "/comparison.json"
	if b, e := os.ReadFile(path); e == nil {
		if !*resume {
			return fmt.Errorf("report exists; use -resume")
		}
		var old rag.DialogueExperiment
		if json.Unmarshal(b, &old) != nil || old.Model != report.Model || old.CorpusHash != report.CorpusHash || old.DatasetHash != report.DatasetHash || old.ContextPrompt != report.ContextPrompt || old.GroundingInstructions != report.GroundingInstructions || old.EvaluationPrompt != report.EvaluationPrompt || old.Settings != settings {
			return fmt.Errorf("resume mismatch")
		}
		report = old
	} else if !os.IsNotExist(e) {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	save := func() error { return rag.SaveDialogueExperiment(*out, report) }
	for si, scenario := range scenarios {
		if si >= len(report.Runs) {
			report.Runs = append(report.Runs, rag.DialogueRun{Scenario: scenario})
		}
		for _, enhanced := range []bool{false, true} {
			mode := "before"
			results := &report.Runs[si].Before
			if enhanced {
				mode = "after"
				results = &report.Runs[si].After
			}
			options := settings
			options.Dialogue = enhanced
			for ti, turn := range scenario.Turns[:*limit] {
				// Reopen the durable store and recreate the agent before EVERY turn.
				hist, e := history.NewJSONStore(*out + "/sessions/" + scenario.ID + "-" + mode + ".json")
				if e != nil {
					return e
				}
				a, e := agent.NewPersistent(benchmarkLLM{llm}, *model, scenario.ID, hist, agent.WithRetriever(retriever), agent.WithContextStrategy(agent.StrategyConfig{Type: agent.StrategySlidingWindow, KeepLast: 4}))
				if e != nil {
					return e
				}
				if ti >= len(*results) {
					messages := a.Messages()
					var answer rag.Answer
					if len(messages) == 2*(ti+1) { // Recover a successful answer if the process stopped before report save.
						m := messages[len(messages)-1]
						if messages[len(messages)-2].Text != turn.Query || m.Retrieval == nil {
							return fmt.Errorf("history/report mismatch")
						}
						answer = rag.Answer{Text: m.Text, Model: m.Model, Retrieval: m.Retrieval, Usage: agent.Usage{InputTokens: m.Metrics.InputTokens, OutputTokens: m.Metrics.OutputTokens, CachedInputTokens: m.Metrics.CachedInputTokens, ReasoningTokens: m.Metrics.ReasoningTokens, TotalTokens: m.Metrics.TotalTokens}, DurationMS: m.Metrics.DurationMS}
					} else {
						if len(messages) != 2*ti {
							return fmt.Errorf("unexpected transcript length")
						}
						fmt.Printf("%s %s %d/12 answer\n", scenario.ID, mode, ti+1)
						response, e := a.Ask(ctx, agent.Request{Message: turn.Query, RAG: true, RAGOptions: &options})
						if e != nil {
							return e
						}
						answer = rag.Answer{Text: response.Text, Model: response.Model, Usage: response.Usage, DurationMS: response.Duration.Milliseconds(), Retrieval: response.Retrieval}
					}
					state := a.DialogueSnapshot()
					*results = append(*results, rag.DialogueResult{Answer: answer, State: state, Restored: ti > 0, MemoryMatches: rag.CheckDialogueMemory(state, turn), AnswerHash: rag.AnswerHash(answer.Text)})
					if e = save(); e != nil {
						return e
					}
				}
				result := &(*results)[ti]
				if result.Grade == nil {
					fmt.Printf("%s %s %d/12 grade\n", scenario.ID, mode, ti+1)
					g, step, e := rag.GradeDialogue(ctx, retriever, scenario, ti, result.Answer)
					if e != nil {
						return e
					}
					result.Grade = g
					result.EvaluationStep = &step
					if e = save(); e != nil {
						return e
					}
				}
			}
		}
	}
	report.Complete = *limit == 12
	return save()
}
func loadEnv(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if key != "OPENAI_API_KEY" && key != "OPENAI_BASE_URL" {
			continue
		}
		if !ok {
			return fmt.Errorf("некорректный env-файл, строка %d", i+1)
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") {
			v, err := strconv.Unquote(value)
			if err != nil {
				return fmt.Errorf("некорректные кавычки env, строка %d", i+1)
			}
			value = v
		} else if strings.HasPrefix(value, "'") {
			if len(value) < 2 || !strings.HasSuffix(value, "'") {
				return fmt.Errorf("некорректные кавычки env, строка %d", i+1)
			}
			value = value[1 : len(value)-1]
		}
		if os.Getenv(key) == "" {
			if err = os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}
