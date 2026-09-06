package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"codex-chat-cli/internal/config"
	"codex-chat-cli/internal/openai"
	chatweb "codex-chat-cli/internal/web"
)

const instructions = "You are a helpful coding assistant. Answer clearly and concisely."

func main() {
	logger := log.New(os.Stderr, "", 0)
	if err := run(logger); err != nil {
		logger.Printf("Ошибка: %v", err)
		os.Exit(1)
	}
}

func run(logger *log.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("конфигурация: %w", err)
	}

	apiClient, err := openai.NewClient(
		cfg.APIKey,
		cfg.Model,
		cfg.BaseURL,
		instructions,
		&http.Client{Timeout: cfg.Timeout},
	)
	if err != nil {
		return fmt.Errorf("инициализация клиента: %w", err)
	}

	server := &http.Server{
		Addr:              cfg.WebAddr,
		Handler:           chatweb.NewHandler(apiClient, cfg.Model),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      cfg.Timeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverError := make(chan error, 1)
	go func() {
		logger.Printf("Codex Chat Web запущен: %s", browserURL(cfg.WebAddr))
		serverError <- server.ListenAndServe()
	}()

	select {
	case err := <-serverError:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("веб-сервер: %w", err)
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("остановка веб-сервера: %w", err)
		}
		return nil
	}
}

func browserURL(addr string) string {
	host, port, ok := strings.Cut(addr, ":")
	if !ok {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + host + ":" + port
}
