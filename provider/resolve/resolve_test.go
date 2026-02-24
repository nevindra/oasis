package resolve

import (
	"testing"
)

func TestDefaultBaseURL(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"openai", "https://api.openai.com/v1"},
		{"groq", "https://api.groq.com/openai/v1"},
		{"deepseek", "https://api.deepseek.com/v1"},
		{"together", "https://api.together.xyz/v1"},
		{"mistral", "https://api.mistral.ai/v1"},
		{"ollama", "http://localhost:11434/v1"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		if got := defaultBaseURL(tt.provider); got != tt.want {
			t.Errorf("defaultBaseURL(%q) = %q, want %q", tt.provider, got, tt.want)
		}
	}
}

func TestProvider_Gemini(t *testing.T) {
	p, err := Provider(Config{
		Provider: "gemini",
		APIKey:   "test-key",
		Model:    "gemini-2.5-flash",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("provider is nil")
	}
	if p.Name() != "gemini" {
		t.Errorf("Name() = %q, want %q", p.Name(), "gemini")
	}
}

func TestProvider_GeminiWithOptions(t *testing.T) {
	temp := 0.7
	topP := 0.95
	thinking := true
	p, err := Provider(Config{
		Provider:    "gemini",
		APIKey:      "test-key",
		Model:       "gemini-2.5-flash",
		Temperature: &temp,
		TopP:        &topP,
		Thinking:    &thinking,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("provider is nil")
	}
}

func TestProvider_OpenAICompat(t *testing.T) {
	providers := []string{"openai", "groq", "deepseek", "together", "mistral", "ollama"}
	for _, name := range providers {
		t.Run(name, func(t *testing.T) {
			p, err := Provider(Config{
				Provider: name,
				APIKey:   "test-key",
				Model:    "test-model",
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p == nil {
				t.Fatal("provider is nil")
			}
			if p.Name() != name {
				t.Errorf("Name() = %q, want %q", p.Name(), name)
			}
		})
	}
}

func TestProvider_OpenAICompatWithOptions(t *testing.T) {
	temp := 0.5
	topP := 0.9
	p, err := Provider(Config{
		Provider:    "openai",
		APIKey:      "test-key",
		Model:       "gpt-4o",
		Temperature: &temp,
		TopP:        &topP,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("provider is nil")
	}
}

func TestProvider_OpenAICompatCustomBaseURL(t *testing.T) {
	p, err := Provider(Config{
		Provider: "openai",
		APIKey:   "test-key",
		Model:    "custom-model",
		BaseURL:  "https://custom.api.com/v1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("provider is nil")
	}
}

func TestProvider_ThinkingSkippedForOpenAI(t *testing.T) {
	thinking := true
	p, err := Provider(Config{
		Provider: "openai",
		APIKey:   "test-key",
		Model:    "gpt-4o",
		Thinking: &thinking,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("provider is nil")
	}
	// Thinking is silently ignored for openai-compat — no error, no panic.
}

func TestProvider_UnknownProvider(t *testing.T) {
	_, err := Provider(Config{
		Provider: "unknown-llm",
		APIKey:   "test-key",
		Model:    "test-model",
	})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestProvider_EmptyProvider(t *testing.T) {
	_, err := Provider(Config{
		APIKey: "test-key",
		Model:  "test-model",
	})
	if err == nil {
		t.Fatal("expected error for empty provider")
	}
}

func TestEmbeddingProvider_Gemini(t *testing.T) {
	ep, err := EmbeddingProvider(EmbeddingConfig{
		Provider:   "gemini",
		APIKey:     "test-key",
		Model:      "gemini-embedding-001",
		Dimensions: 768,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep == nil {
		t.Fatal("embedding provider is nil")
	}
}

func TestEmbeddingProvider_Unsupported(t *testing.T) {
	_, err := EmbeddingProvider(EmbeddingConfig{
		Provider:   "openai",
		APIKey:     "test-key",
		Model:      "text-embedding-3-small",
		Dimensions: 1536,
	})
	if err == nil {
		t.Fatal("expected error for unsupported embedding provider")
	}
}
