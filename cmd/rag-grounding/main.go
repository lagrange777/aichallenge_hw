package main

import (
	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/docindex"
	"codex-chat-cli/internal/openai"
	"codex-chat-cli/internal/rag"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	index := flag.String("index", "artifacts/docindex/index.sqlite", "index")
	dataset := flag.String("questions", "documents/rag24-questions.json", "10 questions")
	out := flag.String("out", "artifacts/rag24", "report directory")
	model := flag.String("model", "gpt-5.3-codex", "model")
	env := flag.String("env-file", "", "credential file")
	resume := flag.Bool("resume", false, "resume identical experiment")
	flag.Parse()
	if *env != "" {
		if err := loadEnv(*env); err != nil {
			return err
		}
	}
	qs, hash, err := rag.LoadBenchmark(*dataset)
	if err != nil {
		return err
	}
	if len(qs) != 10 {
		return fmt.Errorf("expected 10 questions")
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
	retriever := rag.Retriever{Documents: &docindex.Service{Store: store, Embedder: emb}, LLM: llm, Model: *model}
	settings := agent.DefaultRetrievalOptions()
	settings.Mode = "filter"
	report := rag.GroundingExperiment{Version: 1, CreatedAt: time.Now().UTC(), Model: *model, CorpusHash: info.CorpusHash, DatasetHash: hash, Settings: settings, AnswerPrompt: agent.GroundingPrompt, CheckPrompt: agent.GroundingCheckPrompt, EvaluationPrompt: rag.GroundingEvaluationPrompt}
	path := *out + "/comparison.json"
	if data, e := os.ReadFile(path); e == nil {
		if !*resume {
			return fmt.Errorf("report exists; use -resume")
		}
		if json.Unmarshal(data, &report) != nil || report.Version != 1 || report.Model != *model || report.CorpusHash != info.CorpusHash || report.DatasetHash != hash || report.Settings != settings || report.AnswerPrompt != agent.GroundingPrompt || report.CheckPrompt != agent.GroundingCheckPrompt || report.EvaluationPrompt != rag.GroundingEvaluationPrompt {
			return fmt.Errorf("resume mismatch")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	save := func() error { return rag.SaveGroundingExperiment(*out, report) }
	for i, q := range qs {
		if i >= len(report.Pairs) {
			report.Pairs = append(report.Pairs, rag.GroundingPair{Question: q})
		}
		p := &report.Pairs[i]
		if p.Retrieval == nil {
			fmt.Println(q.ID, "retrieval")
			p.Retrieval, err = retriever.RetrieveConfigured(ctx, q.Query, settings)
			if err != nil {
				return err
			}
			if err = save(); err != nil {
				return err
			}
		}
		for _, strict := range []bool{false, true} {
			a, g := &p.Before, &p.BeforeGrade
			if strict {
				a, g = &p.After, &p.AfterGrade
			}
			if *a == nil {
				fmt.Println(q.ID, "answer grounded=", strict)
				answer, e := rag.FrozenAnswer(ctx, llm, *model, q.Query, p.Retrieval, strict)
				if e != nil {
					return e
				}
				*a = &answer
				if err = save(); err != nil {
					return err
				}
			}
			if *g == nil {
				fmt.Println(q.ID, "grade grounded=", strict)
				grade, step, e := rag.GradeGrounding(ctx, retriever, q, **a)
				if e != nil {
					return e
				}
				*g = grade
				p.EvaluationSteps = append(p.EvaluationSteps, step)
				if err = save(); err != nil {
					return err
				}
			}
		}
	}
	report.Complete = true
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
