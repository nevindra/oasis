# Tools API

## Types

### `Tool[In, Out]`

```go
type Tool[In, Out any] interface {
    Definition() ToolMeta
    Execute(ctx context.Context, in In) (Out, error)
}
```

The primary interface for authoring tools. `In` is any JSON-marshalable struct; field tags (`json`, `describe`, `enum`) are read by `Erase` to build the JSON Schema. `Out` is any JSON-marshalable type. The output schema is derived from `Out` by reflection; implement `OutSchemaProvider` to override.

**Thread-safety:** `Execute` must be safe for concurrent calls. Oasis may dispatch the same registered tool instance in parallel.

**Error contract:**
- Return `(zero, nil)` on success. The framework marshals `Out` into `ToolResult.Content`.
- Return `(zero, err)` for business failures — "not found", "invalid input", "quota exceeded". `Erase` copies `err.Error()` into `ToolResult.Error` so the LLM sees the message, and also returns the Go error so `ToolPolicy` can inspect it for retryability.
- Return `(zero, core.RetryableError(err))` for transient failures you want the policy to retry automatically.
- Never return a Go `error` for a permanent domain failure — the LLM will treat it as an error it should adapt around, not abort on.

### `StreamingTool[In, Out]`

```go
type StreamingTool[In, Out any] interface {
    Tool[In, Out]
    ExecuteStream(ctx context.Context, in In, ch chan<- StreamEvent) (Out, error)
}
```

Extends `Tool[In, Out]` for tools that emit intermediate events. The `ch` channel accepts `StreamEvent` values that are forwarded to the agent's stream sink as they arrive. The final `Out` value is still returned normally. Use `core.EraseStreaming` instead of `core.Erase` to register.

**Important:** streaming tools bypass `ToolPolicy` entirely — retrying a partially-streamed call would duplicate events.

### `AnyTool`

```go
type AnyTool interface {
    Name() string
    Definition() ToolDefinition
    ExecuteRaw(ctx context.Context, args json.RawMessage) (ToolResult, error)
}
```

The type-erased form consumed by the execution loop. You produce this via `Erase` or `EraseStreaming`; you rarely implement it from scratch. The optional `StreamingAnyTool` interface adds `ExecuteStream` for streaming dispatch.

### `ToolMeta`

```go
type ToolMeta struct {
    Name        string
    Description string
}
```

Author-supplied metadata. `Name` is the function name the LLM will call — keep it short, lowercase, underscored (`get_weather`). `Description` tells the LLM when to use the tool; write it as instructions to the model.

### `ToolDefinition`

```go
type ToolDefinition struct {
    Name         string
    Description  string
    Parameters   json.RawMessage // JSON Schema for In, derived by Erase
    OutputSchema json.RawMessage // JSON Schema for Out, derived by Erase; omitempty
}
```

The full definition sent to the LLM. `Parameters` is the JSON Schema derived from `In` at `Erase` time. You never construct this by hand unless you are building a hand-rolled `AnyTool`.

### `ToolResult`

```go
type ToolResult struct {
    Content     string
    Error       string
    Attachments []Attachment
    UI          *UIComponent
}
```

The outcome of a tool call.

| Field | When populated |
|-------|---------------|
| `Content` | Successful result as a plain string. For human-readable text use `core.TextResult`; for structured data use `core.JSONResult` (marshals to a JSON string); for pre-encoded JSON bytes use `core.JSONContent(raw []byte) string`. |
| `Error` | Business failure message. Sent back to the LLM verbatim. Set by `Erase` when `Execute` returns a non-nil error, or by hand for `AnyTool` implementations. |
| `Attachments` | Multimodal content (images, PDFs) to include in the next LLM turn. |
| `UI` | Non-nil instructs consumers to render the result as the named frontend component. Set via `core.UIResult` or by returning a type that implements `core.UIRenderable`. |

`Content` and `Error` are mutually exclusive by convention: set one or the other, not both.

### `ToolPolicy`

```go
type ToolPolicy struct {
    Timeout       time.Duration    // per-attempt deadline; 0 = parent ctx only
    Retries       int              // additional attempts after the first
    RetryDelay    time.Duration    // base backoff; doubles each attempt
    MaxRetryDelay time.Duration    // caps the exponential growth; 0 = no cap
    RetryOn       func(error) bool // nil → DefaultRetryOn
}
```

Attached to a tool via `WithToolPolicy` or `WithToolPolicyMatch`. The policy wrapper sits outside user middleware so each retry is a real attempt through the full middleware chain. Streaming tools (`StreamingAnyTool`) bypass policy wrapping entirely.

### `ToolResultStore`

```go
type ToolResultStore interface {
    Put(ctx context.Context, content string) (id string, err error)
    Get(ctx context.Context, id string, offset, length int) (content string, total int, err error)
}
```

Holds oversized tool results when they exceed `Limits.MaxToolResultLen`. The LLM reads pages via the auto-registered `read_full_result` built-in tool. Implementations must be safe for concurrent use.

Error: `ErrToolResultNotFound` — the id is unknown or has expired.

### `ToolMiddleware`

```go
type ToolMiddleware func(AnyTool) AnyTool
```

A function that wraps one `AnyTool` in another. Applied to every tool at agent build time. The result must not be nil — returning nil panics at registration. Implementations should preserve `StreamingAnyTool` when the inner tool implements it.

---

## Constructors

### `Erase[In, Out]`

```go
func Erase[In, Out any](t Tool[In, Out]) AnyTool
```

Converts a typed `Tool[In, Out]` to `AnyTool`. Derives the JSON Schema for `In` by reflection at this call. Panics on unsupported types so schema errors surface at startup, not at LLM-call time. Called once per tool at agent construction.

### `EraseStreaming[In, Out]`

```go
func EraseStreaming[In, Out any](t StreamingTool[In, Out]) StreamingAnyTool
```

Same as `Erase` but preserves the `ExecuteStream` path.

### `NewInMemoryToolResultStore`

```go
func NewInMemoryToolResultStore(opts ...InMemoryToolResultStoreOption) ToolResultStore
```

Returns a bounded in-memory store. Default: 10 MiB total, 5-minute TTL per entry, FIFO eviction on overflow. Configure via `WithToolResultMaxBytes` and `WithToolResultTTL`.

---

## Methods

### `ApplyToolMiddleware`

```go
func ApplyToolMiddleware(t AnyTool, mws []ToolMiddleware) AnyTool
```

Applies a chain in order: first entry is innermost (closest to the tool), last is outermost. `nil` entries are skipped. Panics if any middleware returns nil.

---

## Options (agent construction)

### `WithTools`

```go
func WithTools(tools ...AnyTool) AgentOption
```

(`oasis.WithTools` / `agent.WithTools`) — Registers one or more erased tools with the agent. Cumulative — call multiple times to add more tools.

### `WithToolConfig`

```go
func WithToolConfig(tc agent.ToolConfig) AgentOption
```

(`oasis.WithToolConfig`) — Configures the tool subsystem in one option. Use this when you need middleware, policies, approval gates, or a custom result store. Fields on `agent.ToolConfig`:

```go
type ToolConfig struct {
    Tools          []core.AnyTool                // appended to any WithTools registrations
    Middleware      []core.ToolMiddleware          // innermost first; applied to every tool
    Policies        map[string]core.ToolPolicy     // keyed by exact tool name
    PolicyMatchers  []agent.ToolPolicyMatcher      // fallback predicate-based policies
    Approvals       []agent.ApprovalConfig         // use agent.Approval(...) helper
    ResultStore     core.ToolResultStore           // nil = use default in-memory store
    ResultStoreExplicit bool                       // set true when explicitly disabling the store
}
```

Wrapping order for middleware:

```
[tool] → [user middleware, innermost first] → [tool policy] → [approval] → dispatch
```

User middleware sits inside `ToolPolicy` so each retry invokes the middleware chain once — the middleware sees one full attempt.

`agent.Approval(toolName, opts...)` builds an `agent.ApprovalConfig`. Approval options:
- `agent.ApprovalPrompt(fn func(core.ToolCall) string)` — custom question shown to the human.
- `agent.OnDeny(action)` — `agent.DenyAskLLMToRevise` (default) puts an error in `ToolResult.Error`; `agent.DenyHalt` stops the run.

The agent must also configure `oasis.WithInputHandler` when approval gates are active — the approval gate sends the prompt through the `InputHandler`.

---

## Built-in tools

### `tools/http.Tool` (`http_fetch`)

Fetches a URL and returns its readable text content (up to 8,000 characters). Uses `go-readability` for article extraction with a plain HTML-strip fallback. Timeout: 15 seconds.

```go
import toolhttp "github.com/nevindra/oasis/tools/http"
tool := oasis.Erase[toolhttp.FetchInput, string](toolhttp.New())
```

### `tools/data` toolkit

Four atomic tools for CSV/JSON/JSONL processing without shelling out:

| Tool name | Input type | Output type | What it does |
|-----------|-----------|-------------|-------------|
| `data_parse` | `ParseInput` | `ParseOutput` | Parse raw CSV, JSON array, or JSONL into records |
| `data_filter` | `FilterInput` | `FilterOutput` | Filter records by column equality/range |
| `data_aggregate` | `AggregateInput` | `AggregateOutput` | Sum, avg, count, min, max over a column |
| `data_transform` | `TransformInput` | `TransformOutput` | Rename columns, reorder, drop fields |

```go
import "github.com/nevindra/oasis/tools/data"
tools := data.New() // returns []oasis.AnyTool, already erased
agent.New(provider, oasis.WithTools(tools...))
```

---

## Errors

| Error | Surface | Meaning |
|-------|---------|---------|
| `ErrToolResultNotFound` | `ToolResultStore.Get` | ID unknown or expired; caller should surface this gracefully |
| `ToolResult.Error` non-empty | `AnyTool.ExecuteRaw` | Business failure; returned to LLM verbatim |
| Go `error` from `ExecuteRaw` | `AnyTool.ExecuteRaw` | Infrastructure failure; inspected by `ToolPolicy.RetryOn` |
| panic from nil middleware | `ApplyToolMiddleware` | Programming error: a middleware returned nil |

---

## Helpers

All helpers below live in `github.com/nevindra/oasis/core`.

```go
// ToolResult constructors (core package)
func core.TextResult(s string) ToolResult          // ToolResult with Content set to s
func core.JSONResult[T any](v T) ToolResult        // marshals v to JSON string in Content; panics on marshal failure
func core.JSONContent(raw []byte) string           // converts pre-encoded JSON bytes to a string for ToolResult.Content
func core.UIResult[T any](name string, props T) ToolResult  // ToolResult with UI component descriptor set
```

```go
// Retry / error helpers (core package)
func core.RetryableError(err error) error          // marks err for automatic retry by ToolPolicy
func core.DefaultRetryOn(err error) bool           // default predicate: context deadline + net timeout + Retryable interface
func core.BackoffDelay(base, max time.Duration, attempt int) time.Duration  // delay = base << attempt, capped at max
```

```go
// Infrastructure-error propagation (core package)
func core.InfraError(err error) error   // wraps err to signal an infrastructure failure (distinct from business errors)
func core.IsInfraError(err error) bool  // reports whether err was wrapped with InfraError
```

`InfraError` and `IsInfraError` give tool authors a second error tier below `RetryableError`. An infra error signals that the failure is structural (storage down, network unreachable) rather than transient — the dispatch layer can inspect it to make skip-vs-abort decisions rather than retry decisions. `RetryableError` is the opt-in retry signal; `InfraError` is the opt-in abort signal; plain `fmt.Errorf` is treated as neither (goes to `ToolResult.Error` and the LLM adapts).

**Middleware helpers** live in `github.com/nevindra/oasis/agent`:

```go
func agent.LoggingMiddleware(logger *slog.Logger) core.ToolMiddleware
func agent.TimingMiddleware() core.ToolMiddleware
func agent.OTelSpanMiddleware(tracer core.Tracer) core.ToolMiddleware
```

**Payload transform types** live in `github.com/nevindra/oasis/core`:

```go
type ToolTransform struct {
    Model      *SinkTransform // what the LLM sees
    Display    *SinkTransform // what the UI streams
    Transcript *SinkTransform // what is persisted
}

type SinkTransform struct {
    Result func(name string, r ToolResult) ToolResult
    Args   func(name string, args json.RawMessage) json.RawMessage
}
```

Configure via `agent.ToolConfig.Transforms` (by exact tool name) or
`agent.ToolConfig.TransformMatchers` (by predicate). Human-facing sinks
(`Display`, `Transcript`) fail closed on transform panic — a safe placeholder
is shown rather than the raw payload. The `Model` sink fails open.
