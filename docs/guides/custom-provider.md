# Building a Custom Provider

Most OpenAI-compatible providers work out of the box with `openaicompat.NewProvider`. You only need a custom provider for APIs with non-OpenAI formats (like Google Gemini or Anthropic).

## Use openaicompat.NewProvider First

If your LLM provider uses the OpenAI chat completions API format, use the built-in provider directly:

```go
import "github.com/nevindra/oasis/provider/openaicompat"

// Any OpenAI-compatible API — just change the base URL
llm := openaicompat.NewProvider("sk-xxx", "gpt-4o", "https://api.openai.com/v1")
llm := openaicompat.NewProvider("gsk-xxx", "llama-3.3-70b-versatile", "https://api.groq.com/openai/v1")
llm := openaicompat.NewProvider("", "llama3", "http://localhost:11434/v1") // Ollama, no key

// With options
llm := openaicompat.NewProvider("sk-xxx", "gpt-4o", "https://api.openai.com/v1",
    openaicompat.WithName("openai"),
    openaicompat.WithOptions(
        openaicompat.WithTemperature(0.7),
        openaicompat.WithMaxTokens(4096),
    ),
)
```

This covers OpenAI, Groq, Together, Fireworks, DeepSeek, Mistral, Ollama, vLLM, LM Studio, OpenRouter, Azure OpenAI, and more.

## Custom Provider (Non-OpenAI APIs)

For APIs with their own format, implement the `Provider` interface directly:

```go
package myprovider

import (
    "context"
    "net/http"

    oasis "github.com/nevindra/oasis"
)

type Provider struct {
    apiKey  string
    model   string
    baseURL string
    client  *http.Client
}

func New(apiKey, model, baseURL string) *Provider {
    return &Provider{
        apiKey:  apiKey,
        model:   model,
        baseURL: baseURL,
        client:  &http.Client{},
    }
}

func (p *Provider) Name() string { return "myprovider" }

func (p *Provider) Chat(ctx context.Context, req oasis.ChatRequest) (oasis.ChatResponse, error) {
    // Convert req.Messages to your API format
    // Make HTTP request
    // Parse response
    return oasis.ChatResponse{
        Content: responseText,
        Usage:   oasis.Usage{InputTokens: in, OutputTokens: out},
    }, nil
}

func (p *Provider) ChatWithTools(ctx context.Context, req oasis.ChatRequest, tools []oasis.ToolDefinition) (oasis.ChatResponse, error) {
    // Same as Chat, but include tool definitions in the request body
    // If the LLM wants to call tools, parse them into ToolCalls
    return oasis.ChatResponse{
        Content:   responseText,
        ToolCalls: parsedToolCalls,
        Usage:     usage,
    }, nil
}

func (p *Provider) ChatStream(ctx context.Context, req oasis.ChatRequest, ch chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
    defer close(ch)  // MUST close when done

    // Make streaming HTTP request (SSE)
    // For each chunk:
    //   ch <- oasis.StreamEvent{Type: oasis.EventTextDelta, Content: chunkText}

    return oasis.ChatResponse{
        Content: fullText,
        Usage:   usage,
    }, nil
}

// compile-time check
var _ oasis.Provider = (*Provider)(nil)
```

### Using openaicompat Helpers

If your API is *mostly* OpenAI-compatible but needs custom headers or auth, use the shared helpers:

```go
func (p *Provider) Chat(ctx context.Context, req oasis.ChatRequest) (oasis.ChatResponse, error) {
    body := openaicompat.BuildBody(req.Messages, nil, p.model, req.ResponseSchema)
    // Custom HTTP request with special headers...
    var resp openaicompat.ChatResponse
    json.NewDecoder(httpResp.Body).Decode(&resp)
    return openaicompat.ParseResponse(resp)
}

func (p *Provider) ChatStream(ctx context.Context, req oasis.ChatRequest, ch chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
    // Don't defer close(ch) — StreamSSE handles it
    body := openaicompat.BuildBody(req.Messages, nil, p.model, req.ResponseSchema)
    // Custom HTTP request...
    return openaicompat.StreamSSE(ctx, httpResp.Body, ch)
}
```

## Key Requirements

1. **`ChatStream` must close `ch`** — callers rely on channel close for cleanup
2. **`ChatWithTools` populates `ToolCalls`** — each needs `ID`, `Name`, and `Args` (JSON)
3. **Raw HTTP only** — no SDKs. Use `net/http` + SSE parsing
4. **Return `ErrHTTP` for HTTP errors** — enables retry middleware

```go
func wrapErr(resp *http.Response) error {
    body, _ := io.ReadAll(resp.Body)
    return &oasis.ErrHTTP{Status: resp.StatusCode, Body: string(body)}
}
```

## Implementing EmbeddingProvider

```go
func (p *Provider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
    // Call embedding API
    // Return one vector per input text
    return vectors, nil
}

func (p *Provider) Dimensions() int { return 1536 }
```

## Composing with Middleware

```go
// Add retry
llm := oasis.WithRetry(myprovider.New(apiKey, model, baseURL))

// Add observability
llm = observer.WrapProvider(llm, model, inst)
```

## See Also

- [Provider Concept](../concepts/provider.md)
- [Observability](../concepts/observability.md) — wrapping providers
