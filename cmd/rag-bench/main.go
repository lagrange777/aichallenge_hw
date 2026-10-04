// rag-bench calibrates and evaluates the four task-23 retrieval modes.
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
	phase := flag.String("phase", "compare", "calibrate | compare | review")
	index := flag.String("index", "artifacts/docindex/index.sqlite", "SQLite index")
	dataset := flag.String("questions", "documents/rag23-questions.json", "held-out questions")
	calibrationQuestions := flag.String("calibration-questions", "documents/rag23-calibration.json", "separate calibration set")
	out := flag.String("out", "artifacts/rag23", "output directory")
	model := flag.String("model", "gpt-5.3-codex", "same answering and auxiliary model")
	env := flag.String("env-file", "", "credentials file")
	resume := flag.Bool("resume", false, "resume exact saved experiment, no regeneration of completed modes")
	reviews := flag.String("reviews", "", "review file for phase=review")
	kBefore := flag.Int("k-before", 20, "candidate limit for calibration")
	kAfter := flag.Int("k-after", 5, "context chunk limit for calibration")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("неожиданные аргументы")
	}
	if *phase != "calibrate" && *phase != "compare" && *phase != "review" {
		return fmt.Errorf("неизвестная фаза")
	}
	path := filepath.Join(*out, "comparison.json")
	if *phase == "review" {
		var r rag.Experiment
		if err := readJSON(path, &r); err != nil {
			return err
		}
		var grades map[string]map[string]rag.Review
		if err := readJSON(*reviews, &grades); err != nil {
			return err
		}
		if err := rag.ApplyExperimentReviews(&r, grades); err != nil {
			return err
		}
		return rag.SaveExperiment(*out, r)
	}
	if *env != "" {
		if err := loadEnv(*env); err != nil {
			return err
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
	r := rag.Retriever{Documents: &docindex.Service{Store: store, Embedder: emb}, LLM: llm, Model: *model}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	calibrationPath := filepath.Join(*out, "calibration.json")
	if *phase == "calibrate" {
		if _, err := os.Stat(calibrationPath); err == nil {
			return fmt.Errorf("калибровка уже существует; выберите новый -out")
		}
		qs, hash, err := rag.LoadBenchmark(*calibrationQuestions)
		if err != nil {
			return err
		}
		o := agent.DefaultRetrievalOptions()
		o.TopKBefore = *kBefore
		o.TopKAfter = *kAfter
		if err = o.Validate(); err != nil {
			return err
		}
		fmt.Printf("Калибровка: %d отдельных вопросов, пороги 1/2/3\n", len(qs))
		c, err := rag.Calibrate(ctx, r, qs, hash, o)
		if err != nil {
			return err
		}
		fmt.Printf("Выбран порог %d; результаты: %+v\n", c.Settings.RelevanceThreshold, c.Scores)
		return rag.SaveCalibration(*out, c)
	}
	var calibration rag.Calibration
	if err = readJSON(calibrationPath, &calibration); err != nil {
		return err
	}
	if calibration.CorpusHash != info.CorpusHash || calibration.Model != *model {
		return fmt.Errorf("калибровка относится к другому корпусу/модели")
	}
	if err = calibration.Settings.Validate(); err != nil {
		return err
	}
	qs, hash, err := rag.LoadBenchmark(*dataset)
	if err != nil {
		return err
	}
	for _, q := range qs {
		for _, c := range calibration.Questions {
			if q.ID == c.ID || q.Query == c.Query {
				return fmt.Errorf("пересечение калибровки и итогового набора")
			}
		}
	}
	report := rag.Experiment{Version: 1, CreatedAt: time.Now().UTC(), Model: *model, Prompt: rag.EvaluationPrompt, RewritePrompt: rag.RewritePrompt, RerankPrompt: rag.RerankPrompt, CorpusHash: info.CorpusHash, DatasetHash: hash, Settings: calibration.Settings, Calibration: &calibration, Evaluation: "Ответы ещё не оценены."}
	if _, err := os.Stat(path); err == nil {
		if !*resume {
			return fmt.Errorf("отчёт существует; используйте -resume или новый -out")
		}
		if err = readJSON(path, &report); err != nil {
			return err
		}
		if report.Model != *model || report.CorpusHash != info.CorpusHash || report.DatasetHash != hash || report.Settings != calibration.Settings || report.Prompt != rag.EvaluationPrompt || report.RewritePrompt != rag.RewritePrompt || report.RerankPrompt != rag.RerankPrompt {
			return fmt.Errorf("конфигурация изменилась; нужен новый -out")
		}
	}
	for i, q := range qs {
		if len(report.Comparisons) <= i {
			report.Comparisons = append(report.Comparisons, rag.Comparison{Question: q})
		}
		c := &report.Comparisons[i]
		if c.Question.ID != q.ID {
			return fmt.Errorf("неверный порядок вопросов в отчёте")
		}
		for j, mode := range rag.Modes {
			if len(c.Results) > j {
				if c.Results[j].Mode != mode {
					return fmt.Errorf("неверный порядок режимов")
				}
				continue
			}
			fmt.Printf("%s %s\n", q.ID, mode)
			o := report.Settings
			o.Mode = mode
			v, err := rag.RunMode(ctx, r, *model, q, o)
			if err != nil {
				return fmt.Errorf("%s/%s: %w; выполненные режимы сохранены, доступен -resume", q.ID, mode, err)
			}
			c.Results = append(c.Results, v)
			report.Complete = i == len(qs)-1 && j == len(rag.Modes)-1
			if err = rag.SaveExperiment(*out, report); err != nil {
				return err
			}
		}
	}
	return nil
}
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
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
