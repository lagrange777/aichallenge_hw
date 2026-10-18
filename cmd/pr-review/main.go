package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"codex-chat-cli/internal/openai"
	"codex-chat-cli/internal/prreview"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	root := flag.String("root", ".", "Git repository (candidate objects are read, never executed)")
	base := flag.String("base", "", "full target base SHA")
	head := flag.String("head", "", "full PR head SHA")
	repo := flag.String("repo", "lagrange777/aichallenge_hw", "owner/repository")
	pr := flag.Int("pr", 0, "PR number")
	out := flag.String("out", "artifacts/review32", "report directory")
	env := flag.String("env-file", "", "optional env file; never evaluated as shell")
	publish := flag.Bool("publish", false, "publish/update a bot comment; requires GITHUB_TOKEN")
	collect := flag.Bool("collect-only", false, "save retrieval input without model calls")
	flag.Parse()
	if *env != "" {
		if err := loadEnv(*env); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	httpClient := &http.Client{Timeout: 4 * time.Minute}
	github := prreview.GitHub{Token: os.Getenv("GITHUB_TOKEN"), Client: httpClient}
	if *publish {
		if github.Token == "" {
			return fmt.Errorf("GITHUB_TOKEN required")
		}
		if err := github.Check(ctx, *repo, *pr, *base, *head); err != nil {
			return err
		}
	}
	in, err := (prreview.Repository{Root: *root}).Collect(ctx, *base, *head)
	if err != nil {
		return err
	}
	if *collect {
		b, _ := json.MarshalIndent(in, "", "  ")
		if err = os.MkdirAll(*out, 0755); err != nil {
			return err
		}
		return os.WriteFile(*out+"/input.json", b, 0644)
	}
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-5.3-codex"
	}
	api := os.Getenv("OPENAI_BASE_URL")
	if api == "" {
		api = "https://api.openai.com/v1"
	}
	llm, err := openai.NewClient(os.Getenv("OPENAI_API_KEY"), api, "", httpClient)
	if err != nil {
		return err
	}
	report, reviewErr := prreview.Review(ctx, llm, model, in)
	report.Repository = *repo
	report.PR = *pr
	if err = prreview.Save(*out, report); err != nil {
		return err
	}
	if reviewErr != nil {
		return reviewErr
	}
	if *publish {
		if err = github.Publish(ctx, report); err != nil {
			return err
		}
	}
	fmt.Printf("Review %s: %d findings, %d changed files, %d RAG sources; reports: %s\n", report.Status, len(report.Findings), len(in.Files), len(in.Sources), *out)
	return nil
}
func loadEnv(p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("cannot read env file")
	}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		k = strings.TrimPrefix(k, "export ")
		if k != "OPENAI_API_KEY" && k != "OPENAI_MODEL" && k != "OPENAI_BASE_URL" {
			continue
		}
		if os.Getenv(k) == "" {
			os.Setenv(k, strings.Trim(strings.TrimSpace(v), "\"'"))
		}
	}
	return nil
}
