package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"codex-chat-cli/internal/chat"
	"codex-chat-cli/internal/cli"
	"codex-chat-cli/internal/config"
	"codex-chat-cli/internal/openai"
)

const instructions = "You are a helpful coding assistant. Answer clearly and concisely."

func main() {
	logger := log.New(os.Stderr, "", 0)

	cfg, err := config.Load()
	if err != nil {
		logger.Printf("Ошибка конфигурации: %v", err)
		os.Exit(1)
	}

	apiClient, err := openai.NewClient(
		cfg.APIKey,
		cfg.Model,
		cfg.BaseURL,
		instructions,
		&http.Client{Timeout: cfg.Timeout},
	)
	if err != nil {
		logger.Printf("Ошибка инициализации клиента: %v", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	session := chat.NewSession(apiClient)
	app := cli.New(os.Stdin, os.Stdout, os.Stderr, session, cfg.Model)
	if err := app.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Printf("Ошибка: %v", err)
		os.Exit(1)
	}

	if ctx.Err() != nil {
		fmt.Fprintln(os.Stdout)
	}
}
