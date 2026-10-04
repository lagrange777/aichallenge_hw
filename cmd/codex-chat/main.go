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

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/config"
	"codex-chat-cli/internal/docindex"
	"codex-chat-cli/internal/history"
	"codex-chat-cli/internal/mcpclient"
	"codex-chat-cli/internal/memory"
	"codex-chat-cli/internal/openai"
	"codex-chat-cli/internal/profile"
	"codex-chat-cli/internal/scheduler"
	chatweb "codex-chat-cli/internal/web"
)

const instructions = "You are a helpful coding assistant. Answer clearly. Adapt detail and presentation to the user profile and current request."

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
		cfg.BaseURL,
		instructions,
		&http.Client{Timeout: cfg.Timeout},
	)
	if err != nil {
		return fmt.Errorf("инициализация клиента: %w", err)
	}
	historyStore, err := history.NewJSONStore(cfg.HistoryPath)
	if err != nil {
		return fmt.Errorf("инициализация истории: %w", err)
	}
	memoryStore, err := memory.NewStore(cfg.MemoryPath)
	if err != nil {
		return fmt.Errorf("инициализация памяти: %w", err)
	}

	profileStore, err := profile.NewStore(cfg.ProfilePath)
	if err != nil {
		return fmt.Errorf("инициализация профилей: %w", err)
	}

	mcpStore, err := mcpclient.NewStore(cfg.MCPPath)
	if err != nil {
		return fmt.Errorf("инициализация MCP: %w", err)
	}
	for _, demo := range []struct {
		name, endpoint string
		tools          []string
	}{
		{"Demo Orders", strings.TrimSpace(os.Getenv("BROKER_DEMO_ORDER_MCP")), []string{"create_buy_order", "get_order", "list_my_orders", "record_order_execution", "record_validation_rejection"}},
		{"Demo Validation", strings.TrimSpace(os.Getenv("BROKER_DEMO_VALIDATION_MCP")), []string{"validate_order"}},
		{"Demo Broker", strings.TrimSpace(os.Getenv("BROKER_DEMO_BROKER_MCP")), []string{"execute_validated_order", "get_account", "get_portfolio"}},
	} {
		if demo.endpoint != "" {
			if err := mcpStore.EnsureConnection(demo.name, demo.endpoint, demo.tools); err != nil {
				return fmt.Errorf("регистрация %s: %w", demo.name, err)
			}
		}
	}
	schedulerStore, err := scheduler.Open(cfg.SchedulerPath)
	if err != nil {
		return fmt.Errorf("инициализация планировщика: %w", err)
	}
	defer schedulerStore.Close()
	monitors := &scheduler.Service{Store: schedulerStore, Source: &scheduler.NeurlySource{Store: mcpStore}, Executor: &scheduler.MCPExecutor{Store: mcpStore}, LLM: apiClient, Model: cfg.Model}
	var documents *docindex.Service
	documentStore, err := docindex.OpenReadOnly(cfg.DocumentIndexPath)
	if err == nil {
		defer documentStore.Close()
		info, infoErr := documentStore.Info()
		if infoErr != nil {
			return fmt.Errorf("индекс документов: %w", infoErr)
		}
		embedder, embedErr := docindex.NewOpenAIEmbedder(cfg.APIKey, cfg.BaseURL, info.Model, info.Dimensions)
		if embedErr != nil {
			return embedErr
		}
		documents = &docindex.Service{Store: documentStore, Embedder: embedder}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("индекс документов: %w", err)
	}
	server := &http.Server{
		Addr: cfg.WebAddr,
		Handler: chatweb.NewHandlerWithDocuments(apiClient, cfg.Model, historyStore, mcpStore, monitors, documents, agent.WithContextStrategy(agent.StrategyConfig{
			Type:     agent.ContextStrategy(cfg.ContextStrategy),
			KeepLast: cfg.ContextKeepLast,
		}), agent.WithMemory(memoryStore), agent.WithProfiles(profileStore)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      3*cfg.Timeout + 4*time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerCtx, cancelWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		monitors.Run(workerCtx, func(err error) { logger.Printf("Планировщик: %v", err) })
	}()
	defer func() { cancelWorker(); <-workerDone }()

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
