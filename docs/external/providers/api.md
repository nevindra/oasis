# Providers API

## Types

### `core.Provider` (re-exported as `oasis.Provider`)

The interface every chat provider implements.

```go
type Provider interface {
    ChatStream(ctx context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error)
    Name() string
}
```

| Method | Contract |
|--------|----------|
| `ChatStream` | Writes `StreamEvent` values to `ch` as content arrives, then **closes `ch` before returning**. Returns the final assembled `ChatResponse` including tool calls, usage, and finish reason. Never returns a nil channel. |
| `Name` | Returns the provider's identifier string (e.g. `"gemini"`, `"openai"`, `"groq"`). Used in logs and error messages. |

Thread-safety: implementations are safe for concurrent `ChatStream` calls. The channel `ch` must not be shared between concurrent callers.

---

### `core.EmbeddingProvider` (re-exported as `oasis.EmbeddingProvider`)

```go
type EmbeddingProvider interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dimensions() int
    Name() string
}
```

`Embed` returns one `[]float32` vector per input text, in the same order. `Dimensions` returns the fixed vector size for this model.

---

### `core.MultimodalEmbeddingProvider` (re-exported as `oasis.MultimodalEmbeddingProvider`)

```go
type MultimodalEmbeddingProvider interface {
    EmbedMultimodal(ctx context.Context, inputs []MultimodalInput) ([][]float32, error)
}
```

Check for this interface via type assertion at runtime. Implementations that also support text-only embedding implement `EmbeddingProvider` as well.

---

### `core.ChatRequest`

```go
type ChatRequest struct {
    Messages         []ChatMessage
    Tools            []ToolDefinition
    ResponseSchema   *ResponseSchema   // nil = free-form text
    GenerationParams *GenerationParams // nil = use provider defaults
}
```

---

### `core.ChatResponse`

```go
type ChatResponse struct {
    Content      string
    Thinking     string          // populated when thinking mode is on
    Attachments  []Attachment    // images or other binary content from the model
    ToolCalls    []ToolCall
    Usage        Usage
    FinishReason FinishReason
    Warnings     []string        // non-fatal provider notes
    ProviderMeta json.RawMessage // provider-specific opaque metadata
}
```

`ProviderMeta` is documented per-provider. For Gemini, it carries `safety_ratings` when present.

---

### `core.GenerationParams`

All fields are pointers — `nil` means "use provider default".

```go
type GenerationParams struct {
    Temperature *float64
    TopP        *float64
    TopK        *int     // Gemini only; ignored with a warning on OpenAI-compat
    MaxTokens   *int
}
```

Pass via `ChatRequest.GenerationParams` for per-request overrides, or via provider-level options for per-provider defaults.

---

### `core.Usage`

```go
type Usage struct {
    InputTokens  int
    OutputTokens int
    CachedTokens int // tokens served from provider-side cache (e.g. Gemini cached content)
}
```

---

### Error types

| Type | When it occurs |
|------|---------------|
| `*core.ErrLLM` | Infrastructure errors (failed to marshal request, decode response, etc.) |
| `*core.ErrHTTP` | Non-2xx HTTP response. Has `Status int`, `Body string`, and `RetryAfter time.Duration` (parsed from `Retry-After` header). `WithRetry` uses this to detect 429/503. |

---

## Constructors

### `gemini.New(apiKey, model string, opts ...Option) *Gemini`

Creates a Gemini chat provider. Defaults: temperature 0.1, top-p 0.9, structured output enabled.

```go
g := gemini.New(apiKey, "gemini-2.0-flash", gemini.WithThinking(true))
```

### `gemini.NewEmbedding(apiKey, model string, dims int) *GeminiEmbedding`

Creates a Gemini embedding provider. `dims` sets the output dimensionality (e.g. 768 for `text-embedding-004`).

### `openaicompat.NewProvider(apiKey, model, baseURL string, opts ...ProviderOption) *Provider`

Creates an OpenAI-compatible chat provider. `baseURL` is the API base (e.g. `"https://api.openai.com/v1"`). The `/chat/completions` path is appended automatically.

```go
p := openaicompat.NewProvider(apiKey, "gpt-4o", "https://api.openai.com/v1")
```

### `openaicompat.NewEmbedding(apiKey, model, baseURL string, dims int, opts ...EmbeddingOption) *Embedding`

Creates an OpenAI-compatible embedding provider. The `/embeddings` path is appended automatically.

### `resolve.Provider(cfg resolve.Config) (oasis.Provider, error)`

Creates a provider from a provider-agnostic config. Known providers auto-fill `BaseURL`. Unknown providers require `BaseURL` (treated as OpenAI-compatible).

```go
p, err := resolve.Provider(resolve.Config{
    Provider: "groq",
    APIKey:   apiKey,
    Model:    "llama-3.3-70b-versatile",
})
```

Known provider strings: `"gemini"`, `"openai"`, `"groq"`, `"deepseek"`, `"together"`, `"mistral"`, `"ollama"`, `"qwen"`, `"qwen-cn"`.

### `resolve.EmbeddingProvider(cfg resolve.EmbeddingConfig) (oasis.EmbeddingProvider, error)`

Same pattern as `resolve.Provider` but for embeddings. Known embedding providers: `"gemini"`, `"openai"`, `"vllm"`, `"ollama"`, `"together"`, `"mistral"`, `"qwen"`, `"qwen-cn"`.

---

## Options

### Gemini options (`gemini.Option`)

| Option | Default | Notes |
|--------|---------|-------|
| `gemini.WithTemperature(t float64)` | 0.1 | |
| `gemini.WithTopP(p float64)` | 0.9 | |
| `gemini.WithThinking(enabled bool)` | false | Enables dynamic thinking budget (`-1`). |
| `gemini.WithGoogleSearch(enabled bool)` | false | Grounds responses with live web search. |
| `gemini.WithURLContext(enabled bool)` | false | Fetches and reads URLs mentioned in the prompt. |
| `gemini.WithCodeExecution(enabled bool)` | false | Enables Gemini's built-in code interpreter. |
| `gemini.WithStructuredOutput(enabled bool)` | true | Enforces JSON output when `ResponseSchema` is set. |
| `gemini.WithResponseModalities(m ...string)` | omitted | Required for image-generation models: `"TEXT"`, `"IMAGE"`. |
| `gemini.WithMediaResolution(r string)` | omitted | `"MEDIA_RESOLUTION_LOW"`, `"MEDIA_RESOLUTION_MEDIUM"`, `"MEDIA_RESOLUTION_HIGH"`. |
| `gemini.WithCachedContent(name string)` | `""` | Resource name of a previously created Gemini cached content. |
| `gemini.WithLogger(l *slog.Logger)` | nil | Emits warnings for unsupported `GenerationParams` fields. |

### OpenAI-compat provider-level options (`openaicompat.ProviderOption`)

Applied once at construction; affect every request.

| Option | Default | Notes |
|--------|---------|-------|
| `openaicompat.WithName(name string)` | `"openai"` | Sets `Provider.Name()`. Use to distinguish providers in logs. |
| `openaicompat.WithHTTPClient(c *http.Client)` | `&http.Client{}` | Custom client for timeouts, proxies. |
| `openaicompat.WithOptions(opts ...Option)` | none | Appends per-request defaults (temperature, top-p, etc.). |
| `openaicompat.WithLogger(l *slog.Logger)` | nil | Warns when `GenerationParams.TopK` is ignored. |

### OpenAI-compat per-request options (`openaicompat.Option`)

| Option | Notes |
|--------|-------|
| `openaicompat.WithTemperature(t float64)` | 0.0–2.0 |
| `openaicompat.WithTopP(p float64)` | 0.0–1.0 |
| `openaicompat.WithMaxTokens(n int)` | |
| `openaicompat.WithFrequencyPenalty(p float64)` | -2.0–2.0 |
| `openaicompat.WithPresencePenalty(p float64)` | -2.0–2.0 |
| `openaicompat.WithStop(s ...string)` | Stop sequences |
| `openaicompat.WithSeed(s int)` | Deterministic output |
| `openaicompat.WithToolChoice(choice any)` | `"none"`, `"auto"`, `"required"`, or a specific-tool object |
| `openaicompat.WithCacheControl(messageIndices ...int)` | Marks specified messages with `cache_control: {type: ephemeral}`. Supported by Anthropic, Qwen. |

---

## Decorators

### `agent.WithRetry(p Provider, opts ...RetryOption) Provider`

Wraps `p` with automatic retry on transient HTTP errors (HTTP 429 Too Many Requests and 503 Service Unavailable). Uses exponential backoff with jitter. If the error includes a `Retry-After` header, the retry delay is at least that long. Retries only happen before any stream tokens have been sent — once streaming starts, errors pass through immediately.

| Option | Default | Notes |
|--------|---------|-------|
| `agent.RetryMaxAttempts(n int)` | 3 | Total attempts including the first. |
| `agent.RetryBaseDelay(d time.Duration)` | 1s | Base delay; doubles each retry: 1s, 2s, 4s, … |
| `agent.RetryTimeout(d time.Duration)` | 0 (disabled) | Overall timeout across all attempts. |
| `agent.RetryLogger(l *slog.Logger)` | nop | Logs retries at WARN, final failures at ERROR. |

```go
llm := agent.WithRetry(raw, agent.RetryMaxAttempts(5), agent.RetryBaseDelay(500*time.Millisecond))
```

Also available for embedding providers: `agent.WithEmbeddingRetry(p EmbeddingProvider, opts ...RetryOption) EmbeddingProvider`.

### `ratelimit.WithRateLimit(p Provider, opts ...RateLimitOption) Provider`

Re-exported as `oasis.WithRateLimit`. Wraps `p` with proactive rate limiting using a sliding 1-minute window. Blocks the call until the budget allows it; respects context cancellation.

| Option | Notes |
|--------|-------|
| `ratelimit.RPM(n int)` / `oasis.RPM(n)` | Maximum requests per minute. |
| `ratelimit.TPM(n int)` / `oasis.TPM(n)` | Maximum tokens per minute (input + output combined). Soft limit — the request that tips over the budget completes; subsequent requests block. |

```go
llm := oasis.WithRateLimit(raw, oasis.RPM(60), oasis.TPM(100_000))
```

---

## Catalog

### `catalog.NewModelCatalog(opts ...CatalogOption) *ModelCatalog`

Creates a catalog that discovers models across multiple providers. Safe for concurrent use.

| Option | Default | Notes |
|--------|---------|-------|
| `catalog.WithCatalogTTL(d time.Duration)` | 1 hour | How long to cache live API results. |
| `catalog.WithMaxProviders(n int)` | 50 | Cap on registered providers. |
| `catalog.WithRefresh(s RefreshStrategy)` | `RefreshOnDemand` | `RefreshNone` = static data only; `RefreshOnDemand` = live API on first list, then cached per TTL. |

### Methods

| Method | Signature | Notes |
|--------|-----------|-------|
| `Add` | `Add(platform, apiKey string) error` | Register a known platform (Gemini, OpenAI, Groq, etc.) with credentials. Case-insensitive. |
| `AddCustom` | `AddCustom(identifier, baseURL, apiKey string) error` | Register a custom provider (Ollama, vLLM) not in the built-in list. Defaults to OpenAI-compatible protocol. |
| `RegisterPlatform` | `RegisterPlatform(p oasis.Platform) error` | Add a new platform definition (for non-OpenAI-compatible custom providers). |
| `List` | `List(ctx) ([]ModelInfo, error)` | Returns models from all registered providers, merging static metadata with live results. Degrades gracefully — one failing provider doesn't abort the others. |
| `ListProvider` | `ListProvider(ctx, identifier) ([]ModelInfo, error)` | Models from a single provider. |
| `Validate` | `Validate(ctx, modelID) error` | Checks that `"provider/model"` exists, is not deprecated, and is not unavailable. |
| `CreateProvider` | `CreateProvider(ctx, modelID) (oasis.Provider, error)` | Creates a ready-to-use provider for `"provider/model"`. Validates first. |
| `CreateProviderByID` | `CreateProviderByID(ctx, provider, model) (oasis.Provider, error)` | Same as `CreateProvider` but with separate strings. |
| `Platforms` | `Platforms() []oasis.Platform` | All known platforms. |
| `Remove` | `Remove(identifier)` | Unregisters a provider. |

---

## Errors

| Error | How to handle |
|-------|---------------|
| `*core.ErrHTTP` with `Status == 429` | `WithRetry` handles automatically. If you call providers directly, check `RetryAfter` and sleep. |
| `*core.ErrHTTP` with `Status == 503` | Same as 429. |
| `*core.ErrLLM` | Infrastructure failure (malformed request, unparseable response). Log and propagate; do not retry. |
| `context.DeadlineExceeded` / `context.Canceled` | Context cancelled during streaming or retry wait. Return to caller immediately. |

---

## Helpers

### `core.Chat(ctx, p Provider, req ChatRequest) (ChatResponse, error)`

Re-exported as `oasis.Chat`. Non-streaming convenience wrapper — discards stream events and returns the final response. Use for non-UI code paths.

### `core.ParseRetryAfter(value string) time.Duration`

Re-exported as `oasis.ParseRetryAfter`. Parses a `Retry-After` header value (delay-seconds or HTTP-date) into a `time.Duration`. Returns 0 on empty or unparseable input.
