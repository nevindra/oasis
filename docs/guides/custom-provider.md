# Building a Custom Provider

Implement the `Provider` interface to add support for a new LLM backend. All Oasis providers use raw HTTP — no SDK dependencies.

## Implement Provider

```go
package myprovider

import (
    "context"
    "encoding/json"
    "fmt"
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

func (p *Provider) ChatStream(ctx context.Context, req oasis.ChatRequest, ch chan<- string) (oasis.ChatResponse, error) {
    defer close(ch)  // MUST close when done

    // Make streaming HTTP request (SSE)
    // For each chunk:
    //   ch <- chunkText

    return oasis.ChatResponse{
        Content: fullText,
        Usage:   usage,
    }, nil
}

// compile-time check
var _ oasis.Provider = (*Provider)(nil)
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
