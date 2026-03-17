package resolve

import (
	"fmt"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/provider/gemini"
	"github.com/nevindra/oasis/provider/openaicompat"
)

// Config holds provider-agnostic configuration for creating a chat Provider.
type Config struct {
	Provider string  // "gemini", "openai", "groq", "deepseek", "together", "mistral", "ollama"
	APIKey   string
	Model    string
	BaseURL  string  // required for openai-compat; auto-filled for known providers

	// Common cross-provider options (nil = use provider default).
	Temperature *float64
	TopP        *float64
	Thinking    *bool
}

// EmbeddingConfig holds provider-agnostic configuration for creating an EmbeddingProvider.
type EmbeddingConfig struct {
	Provider   string
	APIKey     string
	Model      string
	BaseURL    string
	Dimensions int
}

// Provider creates an oasis.Provider from a provider-agnostic Config.
// For known providers, the BaseURL is auto-filled. For unknown providers,
// BaseURL must be set — the provider is assumed to be OpenAI-compatible.
func Provider(cfg Config) (oasis.Provider, error) {
	switch cfg.Provider {
	case "gemini":
		return geminiProvider(cfg), nil
	case "openai", "groq", "deepseek", "together", "mistral", "ollama":
		return openaiCompatProvider(cfg), nil
	default:
		if cfg.BaseURL != "" {
			return openaiCompatProvider(cfg), nil
		}
		return nil, fmt.Errorf("resolve: unknown provider %q (provide BaseURL for custom providers)", cfg.Provider)
	}
}

// EmbeddingProvider creates an oasis.EmbeddingProvider from a provider-agnostic EmbeddingConfig.
func EmbeddingProvider(cfg EmbeddingConfig) (oasis.EmbeddingProvider, error) {
	switch cfg.Provider {
	case "gemini":
		return gemini.NewEmbedding(cfg.APIKey, cfg.Model, cfg.Dimensions), nil
	case "openai", "vllm", "ollama", "together", "mistral":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = defaultBaseURL(cfg.Provider)
		}
		var opts []openaicompat.EmbeddingOption
		if cfg.Provider != "openai" {
			opts = append(opts, openaicompat.WithEmbeddingName(cfg.Provider))
		}
		return openaicompat.NewEmbedding(cfg.APIKey, cfg.Model, baseURL, cfg.Dimensions, opts...), nil
	default:
		return nil, fmt.Errorf("resolve: embedding provider %q not supported", cfg.Provider)
	}
}

func geminiProvider(cfg Config) oasis.Provider {
	var opts []gemini.Option
	if cfg.Temperature != nil {
		opts = append(opts, gemini.WithTemperature(*cfg.Temperature))
	}
	if cfg.TopP != nil {
		opts = append(opts, gemini.WithTopP(*cfg.TopP))
	}
	if cfg.Thinking != nil {
		opts = append(opts, gemini.WithThinking(*cfg.Thinking))
	}
	return gemini.New(cfg.APIKey, cfg.Model, opts...)
}

func openaiCompatProvider(cfg Config) oasis.Provider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL(cfg.Provider)
	}
	var provOpts []openaicompat.ProviderOption
	provOpts = append(provOpts, openaicompat.WithName(cfg.Provider))

	var reqOpts []openaicompat.Option
	if cfg.Temperature != nil {
		reqOpts = append(reqOpts, openaicompat.WithTemperature(*cfg.Temperature))
	}
	if cfg.TopP != nil {
		reqOpts = append(reqOpts, openaicompat.WithTopP(*cfg.TopP))
	}
	if len(reqOpts) > 0 {
		provOpts = append(provOpts, openaicompat.WithOptions(reqOpts...))
	}
	return openaicompat.NewProvider(cfg.APIKey, cfg.Model, baseURL, provOpts...)
}

func defaultBaseURL(provider string) string {
	switch provider {
	case "openai":
		return "https://api.openai.com/v1"
	case "groq":
		return "https://api.groq.com/openai/v1"
	case "deepseek":
		return "https://api.deepseek.com/v1"
	case "together":
		return "https://api.together.xyz/v1"
	case "mistral":
		return "https://api.mistral.ai/v1"
	case "ollama":
		return "http://localhost:11434/v1"
	case "vllm":
		return "http://localhost:8000/v1"
	default:
		return ""
	}
}
