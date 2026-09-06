package config

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

const (
	defaultModel   = "gpt-5.3-codex"
	defaultBaseURL = "https://api.openai.com/v1"
	defaultTimeout = 2 * time.Minute
	defaultWebAddr = "127.0.0.1:8080"
)

// Config contains runtime configuration loaded from environment variables.
type Config struct {
	APIKey  string
	Model   string
	BaseURL string
	Timeout time.Duration
	WebAddr string
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
		APIKey:  apiKey,
		Model:   model,
		BaseURL: baseURL,
		Timeout: timeout,
		WebAddr: webAddr,
	}, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
