package oasis

import "context"

// Provider abstracts the LLM backend.
type Provider interface {
	// Chat sends a request and returns a complete response.
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	// ChatWithTools sends a request with tool definitions, returns response (may contain tool calls).
	ChatWithTools(ctx context.Context, req ChatRequest, tools []ToolDefinition) (ChatResponse, error)
	// ChatStream streams tokens into ch, then returns the final response with usage stats.
	ChatStream(ctx context.Context, req ChatRequest, ch chan<- string) (ChatResponse, error)
	// Name returns the provider name (e.g. "gemini", "anthropic").
	Name() string
}

// EmbeddingProvider abstracts text embedding.
type EmbeddingProvider interface {
	// Embed returns embedding vectors for the given texts.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimensions returns the embedding vector size.
	Dimensions() int
	// Name returns the provider name.
	Name() string
}
