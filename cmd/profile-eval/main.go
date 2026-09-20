// profile-eval compares synthetic profiles using the configured real model.
// It does not touch application history, memory or profiles.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/config"
	"codex-chat-cli/internal/openai"
	"codex-chat-cli/internal/profile"
)

func main() {
	output := flag.String("output", "profile-evaluation.json", "JSON report path")
	flag.Parse()
	if err := run(*output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(output string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	client, err := openai.NewClient(cfg.APIKey, cfg.BaseURL, "You are a helpful coding assistant. Answer clearly and adapt to the profile and current request.", &http.Client{Timeout: cfg.Timeout})
	if err != nil {
		return err
	}
	type result struct {
		Profile  profile.Profile `json:"profile"`
		Question string          `json:"question"`
		Answer   string          `json:"answer"`
		Usage    agent.Usage     `json:"usage"`
	}
	report := struct {
		Model   string    `json:"model"`
		Date    time.Time `json:"date"`
		Results []result  `json:"results"`
	}{Model: cfg.Model, Date: time.Now().UTC()}
	question := "Как организовать кеширование запросов к API?"
	profiles := profile.Presets()
	for i := 0; i < 4; i++ {
		p := profiles[i%3]
		q := question
		if i == 3 {
			p = profiles[1]
			q = "Для этого ответа ответь по-русски одним коротким абзацем без кода: как организовать кеширование запросов к API?"
		}
		fmt.Fprintf(os.Stderr, "Проверка %d/4: %s\n", i+1, p.Name)
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		response, err := client.Complete(ctx, agent.CompletionRequest{Model: cfg.Model, Input: q, Profile: &p})
		cancel()
		if err != nil {
			return err
		}
		report.Results = append(report.Results, result{p, q, response.Output, response.Usage})
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(output, data, 0600); err != nil {
			return err
		}
	}
	return nil
}
