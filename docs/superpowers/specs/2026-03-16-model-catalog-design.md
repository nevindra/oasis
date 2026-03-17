# Model Catalog — Design Spec

**Date:** 2026-03-16
**Status:** Draft
**Scope:** New framework-level primitive for dynamic model discovery, validation, and provider creation.

---

## Problem

Today, Oasis users must manually specify provider name and model name as strings in configuration. This has three problems:

1. **No validation** — a typo in the model name only surfaces when the first API call fails.
2. **No discovery** — users cannot browse available models without consulting external docs for each provider.
3. **No deprecation safety** — if a provider deprecates a model, users have no way to know until their app breaks in production.

## Goals

- **Developer UX** — help users discover valid models when configuring Oasis.
- **Runtime intelligence** — let the framework validate models and surface capabilities at runtime.
- **Observability/cost** — expose pricing and capability metadata for informed decisions and cost tracking.
- **Fully dynamic** — zero hardcoded providers in application code. End users add providers at runtime (pick platform, enter API key, browse models, select).

## Non-Goals

- Model benchmarking or quality scoring.
- Automatic model selection / routing (future work that can build on this).
- Replacing the existing `Provider` interface — this is additive.

---

## Design

### 1. Core Types

```go
// Protocol determines how to communicate with a provider's model listing API.
type Protocol int

const (
    // ProtocolOpenAICompat is the default — covers ~90% of providers.
    ProtocolOpenAICompat Protocol = iota
    // ProtocolGemini is for Google's unique Generative Language API.
    ProtocolGemini
)

// Platform represents a known LLM provider platform.
type Platform struct {
    Name     string   // "OpenAI", "Gemini", "Qwen", "Groq", etc.
    Protocol Protocol // determines how to list models and create providers
    BaseURL  string   // default API endpoint (hidden from end users)
}

// ModelInfo is the normalized metadata for a single model.
type ModelInfo struct {
    ID          string // "qwen-turbo", "gemini-2.5-flash"
    Provider    string // identifier used in catalog (e.g., "openai", "qwen")
    DisplayName string // human-friendly name (empty if API doesn't provide it)

    InputContext  int // max input tokens (0 = unknown)
    OutputContext int // max output tokens (0 = unknown)

    Capabilities ModelCapabilities

    Status         ModelStatus // availability from live API
    Deprecated     bool        // is this model deprecated?
    DeprecationMsg string      // "use X instead" (empty if not deprecated or unknown)

    Pricing *ModelPricing // nil if provider doesn't expose it
}

// ModelCapabilities describes what a model can do.
type ModelCapabilities struct {
    Chat      bool
    Vision    bool
    ToolUse   bool
    Embedding bool
}

// ModelPricing holds per-token cost information.
// Replaces observer.ModelPricing — observer/cost.go will be updated to use oasis.ModelPricing.
type ModelPricing struct {
    InputPerMillion  float64 // USD per 1M input tokens
    OutputPerMillion float64 // USD per 1M output tokens
}

// ModelStatus reflects the live availability of a model.
type ModelStatus int

const (
    ModelStatusUnknown     ModelStatus = iota // no live data (no API key, or offline)
    ModelStatusAvailable                       // confirmed available by live API
    ModelStatusUnavailable                     // in static data but NOT in live API
)
```

**Design decisions:**

- `0` means unknown, not zero — no fake data for fields the provider API doesn't expose.
- `Pricing` is a pointer — `nil` means the provider doesn't expose pricing, which is different from "free."
- `ModelPricing` consolidates with the existing `observer.ModelPricing` type. Field names `InputPerMillion` / `OutputPerMillion` match the existing observer convention.
- `ModelCapabilities` uses explicit bools — type-safe and IDE-friendly, not a string slice.
- `Protocol` has only 2 values — everything except Gemini speaks OpenAI-compatible (including Ollama's `/v1` endpoint).
- `ModelStatus` reflects live API state, separate from the `Deprecated` field which comes from static data.

### 2. Model Identifier Format

Models use the `"provider/model"` format as a canonical identifier:

```
openai/gpt-4o
gemini/gemini-2.5-flash
qwen/qwen-turbo
groq/llama-3.3-70b-versatile
```

This mirrors conventions from Docker (`registry/image:tag`), Go modules (`github.com/user/repo`), and OpenRouter (`openai/gpt-4o`). A single string encodes both provider and model, reducing misconfiguration.

The catalog accepts both formats:

```go
// Unified string
llm, _ := catalog.CreateProvider(ctx, "openai/gpt-4o")

// Also valid for backward compatibility and programmatic use
llm, _ := catalog.CreateProviderByID(ctx, "openai", "gpt-4o")
```

### 3. ModelCatalog API

```go
// ModelCatalog discovers and caches models across multiple providers.
// ModelCatalog is safe for concurrent use. Internal state is protected by sync.RWMutex.
type ModelCatalog struct { /* unexported fields */ }

// NewModelCatalog creates a catalog with optional configuration.
func NewModelCatalog(opts ...CatalogOption) *ModelCatalog

// --- Configuration options ---

// WithCatalogTTL sets the cache duration for live API results. Default: 1 hour.
func WithCatalogTTL(d time.Duration) CatalogOption

// WithMaxProviders sets the maximum number of providers that can be registered.
// Default: 50. Prevents unbounded growth.
func WithMaxProviders(n int) CatalogOption

// WithRefresh sets the refresh strategy for live model data.
// Accepts RefreshNone or RefreshOnDemand.
// Default: RefreshOnDemand (live call on List, cached with TTL).
func WithRefresh(strategy RefreshStrategy) CatalogOption

// WithRefreshInterval starts a background goroutine that refreshes
// periodically at the given interval. Requires Close() for cleanup.
func WithRefreshInterval(d time.Duration) CatalogOption

// RefreshStrategy controls when live API calls are made.
type RefreshStrategy int

const (
    // RefreshNone uses static data only. No external calls for discovery.
    // Suitable for air-gapped or offline environments.
    RefreshNone RefreshStrategy = iota

    // RefreshOnDemand makes a live API call on List/ListProvider,
    // cached with TTL. Default.
    RefreshOnDemand
)

// --- Platform discovery ---

// Platforms returns all available platforms (built-in + registered custom).
func (c *ModelCatalog) Platforms() []Platform

// RegisterPlatform adds a custom platform definition.
// Use for providers that Oasis doesn't ship with yet.
// Returns error if Name or BaseURL is empty.
func (c *ModelCatalog) RegisterPlatform(p Platform) error

// --- Provider management (runtime, from end-user input) ---

// Add registers a known platform with an API key.
// The platform name is case-insensitive ("Qwen", "qwen", "QWEN" all match).
// If the platform was already added, the credentials are updated (overwrite semantics).
// Returns error if platform is not found in the registry.
func (c *ModelCatalog) Add(platform, apiKey string) error

// AddCustom registers a custom provider with a base URL and identifier.
// Use for self-hosted (Ollama, vLLM) or providers not in the built-in list.
// Defaults to OpenAI-compatible protocol. Use RegisterPlatform for
// non-OpenAI-compatible custom providers (e.g., Gemini protocol proxies).
func (c *ModelCatalog) AddCustom(identifier, baseURL, apiKey string) error

// Remove removes a provider by identifier.
func (c *ModelCatalog) Remove(identifier string)

// --- Model discovery ---

// List returns models from all registered providers.
// Merges static metadata with live API results (if API key is available).
func (c *ModelCatalog) List(ctx context.Context) ([]ModelInfo, error)

// ListProvider returns models from a single registered provider.
func (c *ModelCatalog) ListProvider(ctx context.Context, identifier string) ([]ModelInfo, error)

// Validate checks if a model exists and is not deprecated.
// Returns an actionable error message if validation fails
// (e.g., "model gemini-1.0-pro is deprecated, use gemini-2.5-flash instead").
func (c *ModelCatalog) Validate(ctx context.Context, modelID string) error

// --- Provider creation ---

// CreateProvider creates a ready-to-use oasis.Provider for the given model.
// modelID uses the "provider/model" format (e.g., "openai/gpt-4o").
// Validates the model before creating the provider.
func (c *ModelCatalog) CreateProvider(ctx context.Context, modelID string) (oasis.Provider, error)

// CreateProviderByID creates a ready-to-use oasis.Provider for the given
// provider and model pair. Equivalent to CreateProvider("provider/model").
func (c *ModelCatalog) CreateProviderByID(ctx context.Context, provider, model string) (oasis.Provider, error)

// --- Lifecycle ---

// Close stops background refresh (if WithRefreshInterval is used) and
// releases resources. Safe to call multiple times.
func (c *ModelCatalog) Close() error
```

**Design decisions:**

- `Add()` vs `AddCustom()` — two paths, clear separation. Known platform = just API key. Custom = identifier + URL.
- `CreateProvider()` / `CreateProviderByID()` on the catalog itself — closes the loop. End user picks platform, picks model, gets a working provider. Named `Create*` to avoid shadowing the `oasis.Provider` interface.
- `Validate()` accepts the unified `"provider/model"` string format.
- `Close()` follows goroutine discipline (ENGINEERING.md) — `WithRefreshInterval` starts a background goroutine that requires explicit cleanup.
- `WithMaxProviders()` bounds the cache (ENGINEERING.md: "bound all caches, buffers, and queues").
- Case-insensitive platform names — end users shouldn't worry about casing.

### 4. Built-in Platform Registry

Oasis ships with a pre-configured list of known platforms. End users see these in the UI dropdown without any developer code.

```go
var builtinPlatforms = []Platform{
    {Name: "OpenAI",    Protocol: ProtocolOpenAICompat, BaseURL: "https://api.openai.com/v1"},
    {Name: "Gemini",    Protocol: ProtocolGemini,       BaseURL: "https://generativelanguage.googleapis.com/v1beta"},
    {Name: "Groq",      Protocol: ProtocolOpenAICompat, BaseURL: "https://api.groq.com/openai/v1"},
    {Name: "DeepSeek",  Protocol: ProtocolOpenAICompat, BaseURL: "https://api.deepseek.com"},
    {Name: "Qwen",      Protocol: ProtocolOpenAICompat, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"},
    {Name: "Together",  Protocol: ProtocolOpenAICompat, BaseURL: "https://api.together.xyz/v1"},
    {Name: "Mistral",   Protocol: ProtocolOpenAICompat, BaseURL: "https://api.mistral.ai/v1"},
    {Name: "Fireworks", Protocol: ProtocolOpenAICompat, BaseURL: "https://api.fireworks.ai/inference/v1"},
    {Name: "Cerebras",  Protocol: ProtocolOpenAICompat, BaseURL: "https://api.cerebras.ai/v1"},
    {Name: "Ollama",    Protocol: ProtocolOpenAICompat, BaseURL: "http://localhost:11434/v1"},
}
```

- Ollama has a default localhost URL for convenience; end users will typically use `AddCustom` with their actual host address.
- Only 2 protocols. Gemini is the sole outlier — everything else speaks OpenAI-compatible.
- New providers are added with each Oasis release. Between releases, developers use `RegisterPlatform()`.

### 5. Protocol-Specific Model Listing

```
catalog.ListProvider(ctx, "qwen")
    │
    ├─ Look up credentials + platform
    ├─ Check cache → if fresh, return cached
    │
    ├─ Protocol == ProtocolOpenAICompat?
    │   → GET {baseURL}/models
    │   → Authorization: Bearer {apiKey}
    │   → Parse response (handle both {"data":[...]} and raw array)
    │   → Normalize to []ModelInfo
    │   → Extract extended fields where available:
    │       Groq: context_window, max_completion_tokens, active
    │       Together: context_length, pricing, type
    │       Mistral: max_context_length, capabilities, deprecation
    │
    ├─ Protocol == ProtocolGemini?
    │   → GET {baseURL}/models?key={apiKey}
    │   → Paginate with pageToken
    │   → Normalize: inputTokenLimit → InputContext,
    │     outputTokenLimit → OutputContext,
    │     supportedGenerationMethods → Capabilities
    │
    └─ Cache result with TTL, merge with static data
```

**Provider metadata coverage from live APIs:**

| Provider  | ID  | Context | Capabilities | Pricing | Deprecation |
|-----------|-----|---------|-------------|---------|-------------|
| OpenAI    | `id` | -       | -           | -       | -           |
| Gemini    | `name` | input + output | `supportedGenerationMethods` | - | - |
| Groq      | `id` | `context_window` + `max_completion_tokens` | - | - | - |
| Together  | `id` | `context_length` | `type` enum | `pricing` obj | - |
| Mistral   | `id` | `max_context_length` | `capabilities` obj | - | `deprecation` + replacement |
| DeepSeek  | `id` | -       | -           | -       | -           |
| Others    | `id` | best effort | best effort | -   | -           |

Where live APIs are sparse (OpenAI, DeepSeek), static data fills the gaps.

### 6. Three-Layer Architecture

The catalog merges three data sources to produce the final model list:

```
Layer 1: Static Data (models_gen.go)
    ├─ Generated by CI script every 6 hours
    ├─ Rich metadata: pricing, capabilities, context windows
    ├─ Always available, zero latency, works offline
    ├─ Sources: OpenRouter API + models.dev (cross-referenced)

Layer 2: Live API Data (per-provider /v1/models)
    ├─ Called when user has added API key + requests model list
    ├─ Authoritative for availability (what the user's key can access)
    ├─ Catches new models not yet in static data
    ├─ Catches removed/deprecated models still in static data

Layer 3: Merge
    ├─ Static metadata + live availability = full picture
    ├─ Model in static AND live → full metadata + ModelStatusAvailable
    ├─ Model in static but NOT live → full metadata + ModelStatusUnavailable
    ├─ Model in live but NOT static → minimal metadata + ModelStatusAvailable
```

**Why not just one layer:**

- Static-only (Mastra's production mode) cannot detect deprecated models until redeploy.
- Live-only would fail offline, be slow, and miss pricing data (most providers don't expose it).
- The merge gives both stability (static always works) and freshness (live corrects it).

### 7. Static Data Generation

#### File structure

```
catalog.go                          ← vocabulary types in root oasis package
                                      (ModelInfo, ModelCapabilities, ModelPricing,
                                       ModelStatus, Platform, Protocol)
cmd/modelgen/main.go               ← generator tool
provider/catalog/
    models_gen.go                   ← generated static data (checked into repo)
    platforms.go                    ← built-in platform definitions
    catalog.go                      ← ModelCatalog struct + implementation
```

#### Generator behavior

The generator is invoked via `go generate`:

```go
//go:generate go run ../../cmd/modelgen
package catalog
```

Steps:

1. Fetch from **OpenRouter** (`/api/frontend/models`) — richest metadata (pricing, capabilities, context windows, deprecation).
2. Fetch from **models.dev** (`/api.json`) — cross-reference, catches providers OpenRouter might miss.
3. Merge by model ID. Prefer OpenRouter for pricing data.
4. Filter out deprecated models.
5. Write `models_gen.go` atomically (write to temp file, rename).

Output format:

```go
// Code generated by modelgen; DO NOT EDIT.
package catalog

var staticModels = []oasis.ModelInfo{
    {
        ID: "gpt-4o", Provider: "openai",
        DisplayName: "GPT-4o",
        InputContext: 128000, OutputContext: 16384,
        Capabilities: oasis.ModelCapabilities{Chat: true, Vision: true, ToolUse: true},
        Pricing: &oasis.ModelPricing{InputPerMillion: 2.50, OutputPerMillion: 10.0},
    },
    // ... hundreds more
}
```

#### CI pipeline

```yaml
# .github/workflows/update-models.yml
name: Update Model Registry
on:
  schedule:
    - cron: '0 */6 * * *'  # every 6 hours
  workflow_dispatch: {}      # manual trigger

jobs:
  update:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - run: go generate ./provider/catalog/...
      - name: Check for changes
        id: diff
        run: |
          git diff --exit-code provider/catalog/models_gen.go || echo "changed=true" >> $GITHUB_OUTPUT
      - name: Create PR
        if: steps.diff.outputs.changed == 'true'
        run: |
          git checkout -b update-model-registry-$(date +%Y%m%d%H%M)
          git add provider/catalog/models_gen.go
          git commit -m "chore: update model registry"
          gh pr create --title "chore: update model registry" --body "Automated model registry update from OpenRouter + models.dev"
```

### 8. End-to-End Usage

#### Chat app — end-user flow

```
App starts → empty catalog, no providers configured
    │
    ├─ End user opens settings
    │   → UI calls catalog.Platforms()
    │   → Shows: OpenAI, Gemini, Groq, Qwen, DeepSeek, Mistral, Together, ...
    │
    ├─ End user picks "Qwen", enters API key
    │   → App calls catalog.Add("qwen", apiKey)
    │
    ├─ End user opens model dropdown
    │   → App calls catalog.ListProvider(ctx, "qwen")
    │   → Returns models with metadata: context window, capabilities, pricing, status
    │
    ├─ End user picks "qwen-turbo"
    │   → App calls catalog.CreateProvider(ctx, "qwen/qwen-turbo")
    │   → Returns ready-to-use oasis.Provider
    │
    └─ Chat starts with the selected provider
```

For custom/self-hosted providers:

```
End user picks "Custom Provider"
    → Enters identifier: "my-ollama"
    → Enters base URL: "http://192.168.1.50:11434"
    → App calls catalog.AddCustom("my-ollama", "http://192.168.1.50:11434/v1", "")
    → Model listing works the same way
```

#### App developer code

```go
import (
    "github.com/theapemachine/oasis/provider/catalog"
)

func main() {
    cat := catalog.NewModelCatalog(
        catalog.WithCatalogTTL(1 * time.Hour),
    )

    // HTTP handlers — developer writes these once, never touches provider names
    http.HandleFunc("GET /api/platforms", func(w http.ResponseWriter, r *http.Request) {
        json.NewEncoder(w).Encode(cat.Platforms())
    })

    http.HandleFunc("POST /api/providers", func(w http.ResponseWriter, r *http.Request) {
        var req struct {
            Platform string `json:"platform"`
            APIKey   string `json:"api_key"`
        }
        json.NewDecoder(r.Body).Decode(&req)
        if err := cat.Add(req.Platform, req.APIKey); err != nil {
            http.Error(w, err.Error(), 400)
            return
        }
        w.WriteHeader(204)
    })

    http.HandleFunc("POST /api/providers/custom", func(w http.ResponseWriter, r *http.Request) {
        var req struct {
            Identifier string `json:"identifier"`
            BaseURL    string `json:"base_url"`
            APIKey     string `json:"api_key"`
        }
        json.NewDecoder(r.Body).Decode(&req)
        if err := cat.AddCustom(req.Identifier, req.BaseURL, req.APIKey); err != nil {
            http.Error(w, err.Error(), 400)
            return
        }
        w.WriteHeader(204)
    })

    http.HandleFunc("GET /api/providers/{id}/models", func(w http.ResponseWriter, r *http.Request) {
        models, err := cat.ListProvider(r.Context(), r.PathValue("id"))
        if err != nil {
            http.Error(w, err.Error(), 500)
            return
        }
        json.NewEncoder(w).Encode(models)
    })

    http.HandleFunc("POST /api/chat/start", func(w http.ResponseWriter, r *http.Request) {
        var req struct {
            Model string `json:"model"` // "qwen/qwen-turbo"
        }
        json.NewDecoder(r.Body).Decode(&req)

        llm, err := cat.CreateProvider(r.Context(), req.Model)
        if err != nil {
            http.Error(w, err.Error(), 400)
            return
        }

        agent := oasis.NewLLMAgent("chat", "You are a helpful assistant.", llm)
        // ... start chat session
    })
}
```

### 9. Impact on Existing Code

#### `resolve` package

The `resolve` package gets a small update to accept unknown providers with a `BaseURL`:

```go
// Current: rejects unknown providers
// New: falls through to openaicompat if BaseURL is provided
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
```

This is backward-compatible — existing code with known provider names works unchanged. The only change is that unknown providers with a `BaseURL` now succeed instead of erroring.

#### `Provider` interface

No changes. `ModelCatalog` is additive — it sits alongside the existing interface, not modifying it.

#### Breaking changes

None. This is a purely additive feature. The `"provider/model"` format is introduced for the catalog API but does not affect existing `resolve.Config` which retains its separate `Provider` and `Model` fields.

### 10. Alignment with ENGINEERING.md

| Principle | How this design complies |
|-----------|------------------------|
| Extend via composition, not modification | `ModelCatalog` is a new type; `Provider` interface untouched |
| Optional capabilities via interface assertion | Catalog is opt-in — existing code works without it |
| Zero values preserve existing behavior | `ModelInfo` uses 0/nil/empty for unknown fields |
| No SDK dependencies — raw HTTP only | Model listing uses raw HTTP + JSON parsing |
| Interfaces at natural boundaries | Catalog sits at the boundary between app and provider APIs |
| Explicit dependencies, constructor injection | `NewModelCatalog()` with options, no singletons or globals |
| Fail gracefully | Partial metadata instead of errors; live API failure degrades to static-only |
| Bound all caches, buffers, and queues | `WithCatalogTTL` (time), `WithMaxProviders` (size) |
| Context propagation | All I/O methods take `context.Context` |
| Goroutine discipline | `WithRefreshInterval` requires `Close()` for cleanup |
| Optimize for the First 15 Minutes | `catalog.Add("qwen", key)` — one line to get started |
| Every export is a commitment | Small surface: 6 types in root package, 1 struct with methods in `provider/catalog`, 4 option funcs |

### 11. Testing Strategy

- **Unit tests**: mock HTTP responses for each protocol (OpenAI-compat, Gemini). Test normalization of each provider's unique fields.
- **Merge logic tests**: static + live merge correctness for all three states (both, static-only, live-only).
- **Cache tests**: TTL expiry, max providers bound enforcement.
- **Integration tests**: hit real provider APIs (gated behind `OASIS_INTEGRATION_TEST` flag).
- **Generator tests**: verify `modelgen` produces valid Go code from sample API responses.
- **Refresh strategy tests**: verify `RefreshNone` makes no HTTP calls, `RefreshOnDemand` caches, `WithRefreshInterval` runs background goroutine with proper shutdown.

### 12. Future Extensions

These are explicitly **not** in scope but the design accommodates them:

- **Automatic model routing** — use `ModelCapabilities` and `ModelPricing` to route requests to the optimal model.
- **Cost tracking** — combine `Usage` from `ChatResponse` with `ModelPricing` for per-request cost calculation.
- **Model fallback chains** — if a model is `ModelStatusUnavailable`, automatically try an alternative.
- **Custom metadata enrichment** — let users attach their own metadata to models (e.g., internal quality scores).
