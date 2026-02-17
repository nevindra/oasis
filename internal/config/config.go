package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Telegram  TelegramConfig  `toml:"telegram"`
	LLM       LLMConfig       `toml:"llm"`
	Embedding EmbeddingConfig `toml:"embedding"`
	Database  DatabaseConfig  `toml:"database"`
	Brain     BrainConfig     `toml:"brain"`
	Intent    IntentConfig    `toml:"intent"`
	Action    ActionConfig    `toml:"action"`
	Search    SearchConfig    `toml:"search"`
	Observer  ObserverConfig  `toml:"observer"`
}

type TelegramConfig struct {
	Token         string `toml:"token"`
	AllowedUserID string `toml:"allowed_user_id"`
}

type LLMConfig struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	APIKey   string `toml:"api_key"`
}

type EmbeddingConfig struct {
	Provider   string `toml:"provider"`
	Model      string `toml:"model"`
	Dimensions int    `toml:"dimensions"`
	APIKey     string `toml:"api_key"`
}

type DatabaseConfig struct {
	Path       string `toml:"path"`
	TursoURL   string `toml:"turso_url"`
	TursoToken string `toml:"turso_token"`
}

type BrainConfig struct {
	ContextWindow  int    `toml:"context_window"`
	VectorTopK     int    `toml:"vector_top_k"`
	TimezoneOffset int    `toml:"timezone_offset"`
	WorkspacePath  string `toml:"workspace_path"`
}

type IntentConfig struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	APIKey   string `toml:"api_key"`
}

type ActionConfig struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	APIKey   string `toml:"api_key"`
}

type SearchConfig struct {
	BraveAPIKey string `toml:"brave_api_key"`
}

type ObserverConfig struct {
	Enabled bool                       `toml:"enabled"`
	Pricing map[string]ObserverPricing `toml:"pricing"`
}

type ObserverPricing struct {
	Input  float64 `toml:"input"`
	Output float64 `toml:"output"`
}

// Default returns a Config with all defaults applied.
func Default() Config {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/tmp"
	}
	return Config{
		LLM:       LLMConfig{Provider: "gemini", Model: "gemini-2.5-flash"},
		Embedding: EmbeddingConfig{Provider: "gemini", Model: "gemini-embedding-001", Dimensions: 1536},
		Database:  DatabaseConfig{Path: "oasis.db"},
		Brain:     BrainConfig{ContextWindow: 20, VectorTopK: 10, TimezoneOffset: 7, WorkspacePath: filepath.Join(home, "oasis-workspace")},
		Intent:    IntentConfig{Provider: "gemini", Model: "gemini-2.5-flash-lite"},
	}
}

// Load reads config: defaults -> TOML file -> env vars (env wins).
func Load(path string) Config {
	cfg := Default()

	if path == "" {
		path = "oasis.toml"
	}

	if data, err := os.ReadFile(path); err == nil {
		_ = toml.Unmarshal(data, &cfg)
	}

	// Env overrides
	if v := os.Getenv("OASIS_TELEGRAM_TOKEN"); v != "" {
		cfg.Telegram.Token = v
	}
	if v := os.Getenv("OASIS_LLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := os.Getenv("OASIS_EMBEDDING_API_KEY"); v != "" {
		cfg.Embedding.APIKey = v
	}
	if v := os.Getenv("OASIS_INTENT_API_KEY"); v != "" {
		cfg.Intent.APIKey = v
	}
	if v := os.Getenv("OASIS_ACTION_API_KEY"); v != "" {
		cfg.Action.APIKey = v
	}
	if v := os.Getenv("OASIS_TURSO_URL"); v != "" {
		cfg.Database.TursoURL = v
	}
	if v := os.Getenv("OASIS_TURSO_TOKEN"); v != "" {
		cfg.Database.TursoToken = v
	}
	if v := os.Getenv("OASIS_BRAVE_API_KEY"); v != "" {
		cfg.Search.BraveAPIKey = v
	}
	if os.Getenv("OASIS_OBSERVER_ENABLED") == "true" || os.Getenv("OASIS_OBSERVER_ENABLED") == "1" {
		cfg.Observer.Enabled = true
	}

	// Fallbacks
	if cfg.Intent.APIKey == "" {
		cfg.Intent.APIKey = cfg.LLM.APIKey
	}
	if cfg.Action.Provider == "" {
		cfg.Action.Provider = cfg.LLM.Provider
		cfg.Action.Model = cfg.LLM.Model
	}
	if cfg.Action.APIKey == "" {
		cfg.Action.APIKey = cfg.LLM.APIKey
	}

	return cfg
}
