// doc-index builds and evaluates a local document index without starting the chat.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"codex-chat-cli/internal/docindex"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("использование: doc-index plan|build|inspect|search|compare [флаги]; подробности: doc-index build -h")
	}
	command := os.Args[1]
	if command != "plan" && command != "build" && command != "inspect" && command != "search" && command != "compare" {
		return fmt.Errorf("неизвестная команда %q", command)
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	index := f.String("index", "artifacts/docindex/index.sqlite", "SQLite индекс")
	corpus := f.String("corpus", "documents/corpus", "каталог .md/.txt/.go (снимки .go.txt разбираются как Go)")
	questions := f.String("questions", "documents/questions.json", "контрольные вопросы")
	out := f.String("out", "artifacts/docindex", "каталог comparison.json и comparison.md")
	model := f.String("model", docindex.DefaultModel, "embedding-модель для build")
	dims := f.Int("dimensions", docindex.DefaultDimensions, "размерность для build")
	size := f.Int("size", 1800, "максимум Unicode-символов на чанк")
	overlap := f.Int("overlap", 240, "перекрытие в Unicode-символах")
	query := f.String("query", "", "поисковый вопрос")
	k := f.Int("k", 5, "число результатов на стратегию")
	envFile := f.String("env-file", "", "файл с OPENAI_API_KEY и OPENAI_BASE_URL; окружение имеет приоритет")
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("неожиданные аргументы")
	}
	if command == "plan" {
		docs, err := docindex.LoadCorpus(*corpus)
		if err != nil {
			return err
		}
		preview, err := docindex.Preview(docs, docindex.ChunkConfig{Size: *size, Overlap: *overlap})
		if err != nil {
			return err
		}
		e := json.NewEncoder(os.Stdout)
		e.SetIndent("", "  ")
		return e.Encode(preview)
	}
	if *envFile != "" {
		if err := loadEnv(*envFile); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var store *docindex.Store
	var err error
	if command == "build" || command == "compare" {
		store, err = docindex.Open(*index)
	} else {
		store, err = docindex.OpenReadOnly(*index)
	}
	if err != nil {
		return err
	}
	defer store.Close()
	print := func(v any) error { e := json.NewEncoder(os.Stdout); e.SetIndent("", "  "); return e.Encode(v) }
	if command == "inspect" {
		info, err := store.Info()
		if err != nil {
			return err
		}
		return print(info)
	}
	if command != "build" {
		info, err := store.Info()
		if err != nil {
			return err
		}
		*model = info.Model
		*dims = info.Dimensions
	}
	embedder, err := docindex.NewOpenAIEmbedder(os.Getenv("OPENAI_API_KEY"), os.Getenv("OPENAI_BASE_URL"), *model, *dims)
	if err != nil {
		return err
	}
	if command == "search" {
		result, err := (&docindex.Service{Store: store, Embedder: embedder}).Search(ctx, *query, *k)
		if err != nil {
			return err
		}
		return print(result)
	}
	docs, err := docindex.LoadCorpus(*corpus)
	if err != nil {
		return err
	}
	if command == "build" {
		info, err := store.Build(ctx, docs, embedder, docindex.ChunkConfig{Size: *size, Overlap: *overlap}, func(s string) { fmt.Fprintln(os.Stderr, s) })
		if err != nil {
			return err
		}
		return print(info)
	}
	qs, err := docindex.ReadQuestions(*questions)
	if err != nil {
		return err
	}
	report, err := store.Evaluate(ctx, docs, qs, embedder, *k)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(*out, 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err = writeFile(filepath.Join(*out, "comparison.json"), append(raw, '\n')); err != nil {
		return err
	}
	if err = writeFile(filepath.Join(*out, "comparison.md"), []byte(report.Markdown())); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Сравнение сохранено: %s\n", *out)
	return nil
}
func writeFile(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".report-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
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
