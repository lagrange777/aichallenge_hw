// rag-eval generates real paired answers, or renders an explicitly reviewed run.
package main

import (
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
	"path/filepath"
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
	index := flag.String("index", "artifacts/docindex/index.sqlite", "SQLite index")
	questions := flag.String("questions", "documents/rag-questions.json", "10 questions with expectations")
	out := flag.String("out", "artifacts/rag", "output directory")
	model := flag.String("model", "gpt-5.3-codex", "answering model, identical for both modes")
	env := flag.String("env-file", "", "credentials file; environment takes priority")
	reviews := flag.String("reviews", "", "apply reviews JSON to existing comparison, no API calls")
	flag.Parse()
	if *reviews != "" {
		b, err := os.ReadFile(filepath.Join(*out, "comparison.json"))
		if err != nil {
			return err
		}
		var report rag.Report
		if err = json.Unmarshal(b, &report); err != nil {
			return err
		}
		b, err = os.ReadFile(*reviews)
		if err != nil {
			return err
		}
		var grades map[string]map[string]rag.Review
		if err = json.Unmarshal(b, &grades); err != nil {
			return err
		}
		if err = rag.ApplyReviews(&report, grades); err != nil {
			return err
		}
		return rag.WriteReport(*out, report)
	}
	// Never silently overwrite a completed experiment and its reviews.
	if _, err := os.Stat(filepath.Join(*out, "comparison.json")); err == nil {
		return fmt.Errorf("отчёт уже существует: выберите новый -out или примените -reviews")
	}
	if *env != "" {
		if err := loadEnv(*env); err != nil {
			return err
		}
	}
	qs, err := rag.LoadQuestions(*questions)
	if err != nil {
		return err
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
	embedder, err := docindex.NewOpenAIEmbedder(os.Getenv("OPENAI_API_KEY"), base, info.Model, info.Dimensions)
	if err != nil {
		return err
	}
	llm, err := openai.NewClient(os.Getenv("OPENAI_API_KEY"), base, rag.EvaluationPrompt, &http.Client{Timeout: 2 * time.Minute})
	if err != nil {
		return err
	}
	retriever := rag.Retriever{Documents: &docindex.Service{Store: store, Embedder: embedder}}
	report := rag.Report{Version: 1, CreatedAt: time.Now().UTC(), Model: *model, Prompt: rag.EvaluationPrompt, CorpusHash: info.CorpusHash, Strategy: docindex.Structured, TopK: rag.TopK, ContextCharacters: rag.ContextCharacters, Evaluation: "Ответы ещё не оценены. Нужна смысловая проверка по ожиданиям и источникам."}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for _, q := range qs {
		fmt.Printf("%s: без RAG → с RAG\n", q.ID)
		pair, err := rag.Compare(ctx, llm, retriever, *model, q)
		if err != nil {
			return fmt.Errorf("%s: %w (готовые пары сохранены)", q.ID, err)
		}
		report.Pairs = append(report.Pairs, pair)
		report.Complete = len(report.Pairs) == len(qs)
		if err = rag.WriteReport(*out, report); err != nil {
			return err
		}
	}
	return nil
}

// Deliberately no shell evaluation/interpolation, and no secret values in errors.
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
