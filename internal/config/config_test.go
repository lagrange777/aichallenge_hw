package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_TIMEOUT", "")
	t.Setenv("WEB_ADDR", "")
	t.Setenv("HISTORY_PATH", "")
	t.Setenv("CONTEXT_KEEP_LAST", "")
	t.Setenv("CONTEXT_STRATEGY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.APIKey != "test-key" {
		t.Fatalf("APIKey = %q", cfg.APIKey)
	}
	if cfg.Model != defaultModel {
		t.Fatalf("Model = %q, want %q", cfg.Model, defaultModel)
	}
	if cfg.BaseURL != defaultBaseURL {
		t.Fatalf("BaseURL = %q, want %q", cfg.BaseURL, defaultBaseURL)
	}
	if cfg.Timeout != 2*time.Minute {
		t.Fatalf("Timeout = %s", cfg.Timeout)
	}
	if cfg.WebAddr != defaultWebAddr {
		t.Fatalf("WebAddr = %q, want %q", cfg.WebAddr, defaultWebAddr)
	}
	if cfg.HistoryPath != defaultHistoryPath {
		t.Fatalf("HistoryPath = %q, want %q", cfg.HistoryPath, defaultHistoryPath)
	}
	if cfg.ContextKeepLast != 10 {
		t.Fatalf("ContextKeepLast = %d", cfg.ContextKeepLast)
	}
	if cfg.ContextStrategy != "sliding_window" {
		t.Fatalf("ContextStrategy = %q", cfg.ContextStrategy)
	}
}

func TestLoadRequiresAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_MODEL", "another-model")
	t.Setenv("OPENAI_BASE_URL", "http://localhost:8080/v1/")
	t.Setenv("OPENAI_TIMEOUT", "30s")
	t.Setenv("WEB_ADDR", "0.0.0.0:9090")
	t.Setenv("HISTORY_PATH", "/tmp/chat-history.json")
	t.Setenv("CONTEXT_KEEP_LAST", "14")
	t.Setenv("CONTEXT_STRATEGY", "sticky_facts")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Model != "another-model" {
		t.Fatalf("Model = %q", cfg.Model)
	}
	if cfg.BaseURL != "http://localhost:8080/v1" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Timeout != 30*time.Second {
		t.Fatalf("Timeout = %s", cfg.Timeout)
	}
	if cfg.WebAddr != "0.0.0.0:9090" {
		t.Fatalf("WebAddr = %q", cfg.WebAddr)
	}
	if cfg.HistoryPath != "/tmp/chat-history.json" {
		t.Fatalf("HistoryPath = %q", cfg.HistoryPath)
	}
	if cfg.ContextKeepLast != 14 {
		t.Fatalf("ContextKeepLast = %d", cfg.ContextKeepLast)
	}
	if cfg.ContextStrategy != "sticky_facts" {
		t.Fatalf("ContextStrategy = %q", cfg.ContextStrategy)
	}
}

func TestLoadRejectsInvalidWebAddress(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("WEB_ADDR", "localhost")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
}

func TestLoadWithoutContextStrategy(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("CONTEXT_STRATEGY", "none")
	t.Setenv("CONTEXT_KEEP_LAST", "")
	t.Setenv("WEB_ADDR", "")
	t.Setenv("OPENAI_TIMEOUT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContextStrategy != "none" {
		t.Fatalf("ContextStrategy = %q, want none", cfg.ContextStrategy)
	}
}

func TestLoadRejectsInvalidContextSettings(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "CONTEXT_KEEP_LAST", value: "0"},
		{name: "CONTEXT_STRATEGY", value: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CONTEXT_KEEP_LAST", "")
			t.Setenv("CONTEXT_STRATEGY", "")
			t.Setenv(test.name, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() error = nil for %s=%q", test.name, test.value)
			}
		})
	}
}
