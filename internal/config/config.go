package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultModel   = "gpt-5.3-codex"
	defaultBaseURL = "https://api.openai.com/v1"
	defaultTimeout = 2 * time.Minute
)

// Config contains runtime configuration loaded from environment variables.
type Config struct {
	APIKey  string
	Model   string
	BaseURL string
	Timeout time.Duration
}

// Load reads and validates configuration without logging secret values.
func Load() (Config, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return Config{}, fmt.Errorf("переменная окружения OPENAI_API_KEY не задана")
	}

	model := valueOrDefault("OPENAI_MODEL", defaultModel)
	baseURL := strings.TrimRight(valueOrDefault("OPENAI_BASE_URL", defaultBaseURL), "/")

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
	}, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
