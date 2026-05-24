# Providers Examples

## Recipe 1: Connect to Gemini

**Goal:** Create a Gemini provider and make a single non-streaming request.

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/provider/gemini"
)

func main() {
    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")

    resp, err := oasis.Chat(context.Background(), llm, oasis.ChatRequest{
        Messages: []oasis.ChatMessage{
            oasis.SystemMessage("You are a helpful assistant."),
            oasis.UserMessage("What is the capital of France?"),
        },
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(resp.Content)
    fmt.Printf("tokens: %d in / %d out\n", resp.Usage.InputTokens, resp.Usage.OutputTokens)
}
```

**Plain-English walkthrough:**
- `gemini.New` creates the provider with default temperature (0.1) and top-p (0.9).
- `oasis.Chat` is a non-streaming convenience wrapper — it calls `ChatStream` internally, discards the delta events, and returns the assembled response.
- `resp.Usage` gives you the token counts for cost tracking.

**Variations:**
- Swap `"gemini-2.0-flash"` for `"gemini-2.5-pro"` for a more capable model.
- Add `gemini.WithThinking(true)` to enable extended reasoning; the thinking text will appear in `resp.Thinking`.
- Add `gemini.WithGoogleSearch(true)` to ground responses in live web results.

---

## Recipe 2: Connect to OpenAI (or any OpenAI-compatible service)

**Goal:** Use the same code pattern to talk to OpenAI, Groq, Ollama, or any OpenAI-compatible endpoint.

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/provider/openaicompat"
)

func main() {
    // OpenAI
    openai := openaicompat.NewProvider(
        os.Getenv("OPENAI_API_KEY"),
        "gpt-4o",
        "https://api.openai.com/v1",
        openaicompat.WithName("openai"),
    )

    // Groq — identical code, different URL and key
    groq := openaicompat.NewProvider(
        os.Getenv("GROQ_API_KEY"),
        "llama-3.3-70b-versatile",
        "https://api.groq.com/openai/v1",
        openaicompat.WithName("groq"),
    )

    // Ollama — no API key needed for local models
    ollama := openaicompat.NewProvider(
        "",
        "llama3.2",
        "http://localhost:11434/v1",
        openaicompat.WithName("ollama"),
    )

    _ = groq
    _ = ollama

    resp, err := oasis.Chat(context.Background(), openai, oasis.ChatRequest{
        Messages: []oasis.ChatMessage{oasis.UserMessage("What is 2 + 2?")},
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(resp.Content)
}
```

**Plain-English walkthrough:**
- `openaicompat.NewProvider` takes API key, model, and base URL. The `/chat/completions` path is appended automatically.
- `WithName` sets the string returned by `Provider.Name()` — important for logs and error messages.
- Ollama runs locally so you pass an empty string for the API key.
- All three providers satisfy `oasis.Provider` identically; your agent code doesn't change when you switch.

**Variations:**
- For DeepSeek: `"https://api.deepseek.com/v1"`.
- For Mistral: `"https://api.mistral.ai/v1"`.
- For vLLM: `"http://localhost:8000/v1"` with your served model name.

---

## Recipe 3: Use `resolve.Provider` to avoid importing satellites directly

**Goal:** Create a provider from a config struct without importing both `gemini` and `openaicompat`.

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/provider/resolve"
)

func main() {
    p, err := resolve.Provider(resolve.Config{
        Provider: "groq",
        APIKey:   os.Getenv("GROQ_API_KEY"),
        Model:    "llama-3.3-70b-versatile",
    })
    if err != nil {
        panic(err)
    }

    resp, err := oasis.Chat(context.Background(), p, oasis.ChatRequest{
        Messages: []oasis.ChatMessage{oasis.UserMessage("Tell me a joke.")},
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(resp.Content)
}
```

**Plain-English walkthrough:**
- `resolve.Provider` is the "I don't want to import two satellite packages" path.
- Known provider names auto-fill `BaseURL`; for an unknown provider, set `BaseURL` yourself and `resolve` treats it as OpenAI-compatible.
- `Temperature` and `TopP` in `Config` are optional — nil means "use provider default".

**Variations:**
- For Gemini with thinking: `Config{Provider: "gemini", Thinking: oasis.Ptr(true), ...}`.
- For a custom endpoint: `Config{Provider: "myprovider", BaseURL: "https://...", ...}`.

---

## Recipe 4: Add retry and rate limiting

**Goal:** Make a provider resilient to rate-limit errors and enforce a quota.

```go
package main

import (
    "context"
    "fmt"
    "os"
    "time"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/provider/gemini"
)

func main() {
    raw := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")

    // Retry transient errors with up to 5 attempts.
    retried := agent.WithRetry(raw,
        agent.RetryMaxAttempts(5),
        agent.RetryBaseDelay(500*time.Millisecond),
        agent.RetryTimeout(30*time.Second),
    )

    // Rate-limit to 60 RPM and 100k TPM on top of the retry layer.
    llm := oasis.WithRateLimit(retried, oasis.RPM(60), oasis.TPM(100_000))

    ag := oasis.NewLLMAgent("assistant", "Helpful assistant", llm)
    result, err := ag.Execute(context.Background(), oasis.AgentTask{Input: "Summarize Go generics."})
    if err != nil {
        panic(err)
    }
    fmt.Println(result.Output)
}
```

**Plain-English walkthrough:**
- `agent.WithRetry` catches HTTP 429 and 503 responses and backs off before retrying. The 30-second `RetryTimeout` ensures you never wait forever.
- `oasis.WithRateLimit` sits on the outside — it prevents sending requests that would trigger rate limits in the first place, using a sliding 1-minute window.
- The order matters: rate-limiter → retry → raw provider. Rate limiting guards the budget; retry handles the server saying "slow down".

**Variations:**
- For TPM-only limiting (no RPM cap): `oasis.WithRateLimit(raw, oasis.TPM(100_000))`.
- Add `agent.RetryLogger(slog.Default())` to see retry events in your logs.

---

## Recipe 5: Use the catalog to discover and create providers dynamically

**Goal:** Let the catalog discover available models and create a provider without hardcoding model names.

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/nevindra/oasis/provider/catalog"
)

func main() {
    cat := catalog.NewModelCatalog()

    // Register providers with their API keys.
    cat.Add("gemini", os.Getenv("GEMINI_API_KEY"))
    cat.Add("openai", os.Getenv("OPENAI_API_KEY"))

    // List all available Gemini models.
    models, err := cat.ListProvider(context.Background(), "gemini")
    if err != nil {
        panic(err)
    }
    for _, m := range models {
        fmt.Printf("%s: %s (deprecated=%v)\n", m.ID, m.Description, m.Deprecated)
    }

    // Create a provider by "provider/model" ID after validating it exists.
    p, err := cat.CreateProvider(context.Background(), "gemini/gemini-2.0-flash")
    if err != nil {
        panic(err) // err if model not found, deprecated, or unavailable
    }
    fmt.Println("Created provider:", p.Name())
}
```

**Plain-English walkthrough:**
- `cat.Add("gemini", apiKey)` registers Gemini. The catalog knows Gemini's base URL — you only supply the key.
- `ListProvider` merges static model data (capabilities, pricing) with live model availability from the Gemini API. Results are cached for 1 hour by default.
- `CreateProvider` validates the model ID before constructing the provider, so you get a clear error instead of a cryptic HTTP 404 later.

**Variations:**
- For a local Ollama instance: `cat.AddCustom("local-llama", "http://localhost:11434/v1", "")`.
- Pass `catalog.WithRefresh(catalog.RefreshNone)` to skip live API calls and use static data only.
- Call `cat.Validate(ctx, "openai/gpt-5")` before running a job to fail fast if the model is gone.

---

## Recipe 6: Embed text with Gemini or OpenAI

**Goal:** Turn text into embedding vectors for semantic search or RAG.

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/nevindra/oasis/provider/gemini"
    "github.com/nevindra/oasis/provider/openaicompat"
)

func main() {
    // Gemini embedding
    geminiEmb := gemini.NewEmbedding(
        os.Getenv("GEMINI_API_KEY"),
        "text-embedding-004",
        768, // output dimensions
    )

    texts := []string{"Hello world", "Goodbye world"}
    vecs, err := geminiEmb.Embed(context.Background(), texts)
    if err != nil {
        panic(err)
    }
    fmt.Printf("Gemini: %d vectors of dim %d\n", len(vecs), len(vecs[0]))

    // OpenAI-compatible embedding (same interface, different provider)
    openaiEmb := openaicompat.NewEmbedding(
        os.Getenv("OPENAI_API_KEY"),
        "text-embedding-3-small",
        "https://api.openai.com/v1",
        1536,
    )

    vecs2, err := openaiEmb.Embed(context.Background(), texts)
    if err != nil {
        panic(err)
    }
    fmt.Printf("OpenAI: %d vectors of dim %d\n", len(vecs2), len(vecs2[0]))
}
```

**Plain-English walkthrough:**
- Both `gemini.NewEmbedding` and `openaicompat.NewEmbedding` return `oasis.EmbeddingProvider`.
- You pass the desired output dimensionality at construction time; the provider requests that size from the API.
- The returned slice is `[][]float32` — one `[]float32` per input text, in input order.

**Variations:**
- Use `resolve.EmbeddingProvider(cfg)` to create embedders without importing both packages.
- Add retry: `agent.WithEmbeddingRetry(emb, agent.RetryMaxAttempts(3))`.
- For multimodal embeddings (text + images), check `if mp, ok := emb.(oasis.MultimodalEmbeddingProvider); ok { ... }`.
