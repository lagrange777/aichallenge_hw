package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultModel       = "gpt-5.3-codex"
	defaultBaseURL     = "https://api.openai.com/v1"
	defaultTimeout     = 2 * time.Minute
	defaultWebAddr     = "127.0.0.1:8080"
	defaultHistoryPath = "data/history.json"
	defaultKeepLast    = 10
	defaultStrategy    = "sliding_window"
)

// Config contains runtime configuration loaded from environment variables.
type Config struct {
	APIKey          string
	Model           string
	BaseURL         string
	Timeout         time.Duration
	WebAddr         string
	HistoryPath     string
	MemoryPath      string
	ContextKeepLast int
	ContextStrategy string
}

// Load reads and validates configuration without logging secret values.
func Load() (Config, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return Config{}, fmt.Errorf("переменная окружения OPENAI_API_KEY не задана")
	}

	model := valueOrDefault("OPENAI_MODEL", defaultModel)
	baseURL := strings.TrimRight(valueOrDefault("OPENAI_BASE_URL", defaultBaseURL), "/")
	webAddr := valueOrDefault("WEB_ADDR", defaultWebAddr)
	historyPath := valueOrDefault("HISTORY_PATH", defaultHistoryPath)
	contextKeepLast, err := positiveInt("CONTEXT_KEEP_LAST", defaultKeepLast)
	if err != nil {
		return Config{}, err
	}
	contextStrategy := valueOrDefault("CONTEXT_STRATEGY", defaultStrategy)
	if contextStrategy != "none" && contextStrategy != "sliding_window" && contextStrategy != "sticky_facts" && contextStrategy != "branching" {
		return Config{}, fmt.Errorf("CONTEXT_STRATEGY должен быть none, sliding_window, sticky_facts или branching")
	}
	if _, port, err := net.SplitHostPort(webAddr); err != nil || port == "" {
		return Config{}, fmt.Errorf("WEB_ADDR должен иметь формат host:port, например 127.0.0.1:8080")
	}

	timeout := defaultTimeout
	if raw := strings.TrimSpace(os.Getenv("OPENAI_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("OPENAI_TIMEOUT должен быть положительной длительностью, например 2m")
		}
		timeout = parsed
	}

	return Config{
		APIKey:          apiKey,
		Model:           model,
		BaseURL:         baseURL,
		Timeout:         timeout,
		WebAddr:         webAddr,
		HistoryPath:     historyPath,
		MemoryPath:      valueOrDefault("MEMORY_PATH", filepath.Join(filepath.Dir(historyPath), "memory")),
		ContextKeepLast: contextKeepLast,
		ContextStrategy: contextStrategy,
	}, nil
}

func positiveInt(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s должен быть положительным целым числом", name)
	}
	return value, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
