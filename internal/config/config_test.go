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
}

func TestLoadRejectsInvalidWebAddress(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("WEB_ADDR", "localhost")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
}
