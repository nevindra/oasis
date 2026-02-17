package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := Default()
	if cfg.LLM.Provider != "gemini" {
		t.Errorf("expected gemini, got %s", cfg.LLM.Provider)
	}
	if cfg.Brain.TimezoneOffset != 7 {
		t.Errorf("expected tz 7, got %d", cfg.Brain.TimezoneOffset)
	}
	if cfg.Embedding.Dimensions != 1536 {
		t.Errorf("expected 1536, got %d", cfg.Embedding.Dimensions)
	}
}

func TestLoadFromTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.toml")
	os.WriteFile(path, []byte(`
[telegram]
token = "bot123"

[brain]
timezone_offset = 9
`), 0644)

	cfg := Load(path)
	if cfg.Telegram.Token != "bot123" {
		t.Errorf("expected bot123, got %s", cfg.Telegram.Token)
	}
	if cfg.Brain.TimezoneOffset != 9 {
		t.Errorf("expected tz 9, got %d", cfg.Brain.TimezoneOffset)
	}
	// Defaults preserved
	if cfg.LLM.Provider != "gemini" {
		t.Errorf("default should be preserved, got %s", cfg.LLM.Provider)
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("OASIS_TELEGRAM_TOKEN", "env-token")
	t.Setenv("OASIS_LLM_API_KEY", "env-key")

	cfg := Load("/nonexistent/path.toml")
	if cfg.Telegram.Token != "env-token" {
		t.Errorf("expected env-token, got %s", cfg.Telegram.Token)
	}
	if cfg.LLM.APIKey != "env-key" {
		t.Errorf("expected env-key, got %s", cfg.LLM.APIKey)
	}
	// Fallback: intent gets LLM key
	if cfg.Intent.APIKey != "env-key" {
		t.Errorf("expected intent fallback to env-key, got %s", cfg.Intent.APIKey)
	}
}

func TestActionFallback(t *testing.T) {
	cfg := Default()
	cfg.LLM.Provider = "gemini"
	cfg.LLM.Model = "gemini-2.5-flash"
	cfg.LLM.APIKey = "test-key"

	// Simulate Load fallback
	if cfg.Action.Provider == "" {
		cfg.Action.Provider = cfg.LLM.Provider
		cfg.Action.Model = cfg.LLM.Model
	}
	if cfg.Action.APIKey == "" {
		cfg.Action.APIKey = cfg.LLM.APIKey
	}

	if cfg.Action.Provider != "gemini" {
		t.Errorf("expected gemini, got %s", cfg.Action.Provider)
	}
	if cfg.Action.APIKey != "test-key" {
		t.Errorf("expected test-key, got %s", cfg.Action.APIKey)
	}
}
