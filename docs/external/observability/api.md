# Observability API

## Types

### `core.Tracer` (interface)

```go
type Tracer interface {
    Start(ctx context.Context, name string, attrs ...SpanAttr) (context.Context, Span)
}
```

The only interface you implement (or inject) for tracing. `Start` creates a new span, attaches it to the context, and returns both. The returned `context.Context` carries the span so nested calls automatically become child spans. The returned `Span` must have `End()` called when the operation completes — always defer it.

When no `Tracer` is configured on an agent (`WithTracer` not called), all tracing is skipped. No nil-check is required in application code; the agent handles the nil guard internally.

### `core.Span` (interface)

```go
type Span interface {
    SetAttr(attrs ...SpanAttr)
    Event(name string, attrs ...SpanAttr)
    Error(err error)
    End()
}
```

| Method | What it does |
|--------|-------------|
| `SetAttr` | Adds key-value attributes to the span. Call any time before `End`. |
| `Event` | Records a timestamped annotation on the span timeline (e.g., a retry event). |
| `Error` | Marks the span as failed and attaches the error. Does not call `End`. |
| `End` | Flushes the span to the exporter. Must be called exactly once. |

Thread-safety: implementations from `observer.NewTracer()` are safe for concurrent attribute writes. Custom implementations should document their own guarantees.

### `core.SpanAttr`

```go
type SpanAttr struct {
    Key   string
    Value any // string | int | int64 | float64 | bool
}
```

A key-value pair attached to a span or event. Construct with the typed helpers below rather than the struct literal, so the OTEL backend receives the correct attribute type.

**Attribute constructors:**

| Function | Type |
|----------|------|
| `core.StringAttr(k, v string) SpanAttr` | string |
| `core.IntAttr(k string, v int) SpanAttr` | int |
| `core.BoolAttr(k string, v bool) SpanAttr` | bool |
| `core.Float64Attr(k string, v float64) SpanAttr` | float64 |

### `core.StepTrace`

Recorded automatically by the agent run loop. One entry per tool call or agent delegation. Collected into `AgentResult.Steps`.

```go
type StepTrace struct {
    Name      string          // tool or sub-agent name (agent_ prefix stripped)
    Type      StepTraceType   // "tool" | "agent" | "step"
    Input     string          // args truncated to 200 chars (display only)
    Output    string          // result truncated to 500 chars (display only)
    RawArgs   json.RawMessage // untruncated original args bytes; nil for external traces
    RawOutput json.RawMessage // untruncated original result bytes; nil for external traces
    Usage     core.Usage      // per-step token counts
    Duration  time.Duration   // wall-clock time for this step
}
```

`Input`/`Output` are safe for display and logging but are NOT safe to `json.Unmarshal` — truncation may land inside a JSON value. Use `RawArgs`/`RawOutput` (or `AgentResult.ToolCalls()`/`AgentResult.ToolResults()`) when you need round-trip JSON access.

### `core.StepTraceType`

```go
type StepTraceType string

const (
    StepTypeTool  StepTraceType = "tool"   // a tool execution
    StepTypeAgent StepTraceType = "agent"  // a sub-agent delegation
    StepTypeStep  StepTraceType = "step"   // a workflow node
)
```

### `core.Usage`

```go
type Usage struct {
    InputTokens  int
    OutputTokens int
    CachedTokens int // input tokens served from provider cache
}
```

Attached to both `AgentResult.Usage` (aggregate for the whole run) and each `StepTrace.Usage` (per step). `CachedTokens` is populated when the provider supports prompt caching; otherwise zero.

### `core.IterationTrace`

One entry per LLM call, collected into `AgentResult.Iterations`.

```go
type IterationTrace struct {
    Iter      int       // 0-indexed iteration number
    Model     string    // e.g. "gpt-4o"
    StartedAt time.Time
    // ...additional timing/usage fields
}
```

---

## Constructors

### `observer.Init`

```go
func Init(
    ctx context.Context,
    pricing map[string]core.ModelPricing,
) (*Instruments, func(context.Context) error, error)
```

Sets up the OTEL trace, metric, and log providers using OTLP HTTP exporters. Configuration is entirely through standard OTEL environment variables — no code changes between environments.

Returns:
- `*Instruments` — holds all OTEL instruments (tracer, meter, logger, counters, histograms).
- `func(context.Context) error` — shutdown function. Call it with a deadline context on application exit. Internally calls `Shutdown` on all three providers and joins errors.
- `error` — non-nil if any exporter or instrument fails to initialize.

Pass `observer.DefaultPricing` as the pricing map for built-in cost models, or merge in your own overrides (see `observer.NewCostCalculator`).

### `observer.NewTracer`

```go
func NewTracer() core.Tracer
```

Returns a `core.Tracer` backed by the global OTEL `TracerProvider`. Call `observer.Init` first; if called before `Init`, spans go to the no-op provider (no error, no data).

### `observer.WrapProvider`

```go
func WrapProvider(inner core.Provider, model string, inst *Instruments) *ObservedProvider
```

Returns an instrumented `core.Provider` that emits one `llm.chat_stream` span per LLM call, records `llm.token.usage`, `llm.cost.total`, `llm.requests`, and `llm.duration` metrics, and emits a structured OTEL log record. The wrapped provider satisfies `core.Provider` — pass it anywhere a provider is accepted.

### `observer.WrapTool`

```go
func WrapTool(inner core.AnyTool, inst *Instruments) *ObservedTool
```

Returns an instrumented `core.AnyTool` that emits one `tool.execute` span per execution, records `tool.executions` and `tool.duration` metrics, and emits a structured OTEL log record. Status is `"ok"`, `"tool_error"` (business failure in `ToolResult.Error`), or `"error"` (infrastructure failure).

### `observer.WrapEmbedding`

```go
func WrapEmbedding(inner core.EmbeddingProvider, model string, inst *Instruments) *ObservedEmbedding
```

Returns an instrumented `core.EmbeddingProvider` that emits one `llm.embed` span per `Embed` call and records `embedding.requests` and `embedding.duration` metrics.

### `observer.NewCostCalculator`

```go
func NewCostCalculator(overrides map[string]core.ModelPricing) *CostCalculator
```

Merges `observer.DefaultPricing` with any caller-supplied overrides. Overrides take precedence. Pass the result to `observer.Init` to get cost tracking.

---

## Methods

### `(*CostCalculator).Calculate`

```go
func (c *CostCalculator) Calculate(model string, inputTokens, outputTokens, cachedTokens int) float64
```

Returns cost in USD. When `cachedTokens > 0` and the model has `CacheReadPerMillion` pricing, cached tokens are billed at the lower cache rate. Returns `0.0` for unknown models (no error).

---

## Options

### Agent options (package `agent`)

| Option | Default | What it does |
|--------|---------|-------------|
| `agent.WithTracer(t core.Tracer)` | nil (no tracing) | Wires the tracer into the agent run loop. Spans are created for `agent.execute`, `agent.iteration`, `llm.generate`, `agent.loop.synthesis`, and `agent.loop.compress`. |
| `agent.WithLogger(l *slog.Logger)` | discard logger | Sets the structured logger. The agent calls `logger.Info`, `logger.Warn`, `logger.Error`, and `logger.Debug` at key lifecycle points. |

Both are re-exported on the `oasis` root package:

```go
var WithTracer = agent.WithTracer
var WithLogger = agent.WithLogger
```

---

## Span names emitted by the agent run loop

| Span name | When emitted |
|-----------|-------------|
| `agent.execute` | Wraps the full run (once per `Execute` call). Attributes: `agent.name`, `agent.type`, `tokens.input`, `tokens.output`. |
| `agent.iteration` | Wraps one LLM loop pass. |
| `llm.generate` | Wraps the LLM call within an iteration. |
| `agent.loop.synthesis` | Wraps the final response assembly step. |
| `agent.loop.compress` | Emitted when context compression runs. |

## Span names emitted by observer wrappers

| Span name | Emitted by |
|-----------|-----------|
| `llm.chat_stream` | `ObservedProvider.ChatStream` |
| `tool.execute` | `ObservedTool.ExecuteRaw` |
| `llm.embed` | `ObservedEmbedding.Embed` |

## OTEL attribute keys (`observer` package)

| Variable | Key string | Description |
|----------|-----------|-------------|
| `AttrLLMModel` | `llm.model` | Model ID |
| `AttrLLMProvider` | `llm.provider` | Provider name |
| `AttrLLMMethod` | `llm.method` | `"chat_stream"` etc. |
| `AttrTokensInput` | `llm.tokens.input` | Input token count |
| `AttrTokensOutput` | `llm.tokens.output` | Output token count |
| `AttrTokensCached` | `llm.tokens.cached` | Cached token count |
| `AttrCostUSD` | `llm.cost_usd` | Cost in USD |
| `AttrToolName` | `tool.name` | Tool name |
| `AttrToolStatus` | `tool.status` | `"ok"` / `"tool_error"` / `"error"` |
| `AttrToolResultLength` | `tool.result_length` | Result content length |
| `AttrStreamChunks` | `llm.stream_chunks` | Number of SSE chunks received |

## OTEL metrics emitted

| Metric name | Unit | Description |
|-------------|------|-------------|
| `llm.token.usage` | `{token}` | Cumulative tokens per model/provider/direction |
| `llm.cost.total` | `USD` | Cumulative cost |
| `llm.requests` | `{request}` | LLM call count by model/method/status |
| `tool.executions` | `{execution}` | Tool execution count by name/status |
| `embedding.requests` | `{request}` | Embedding request count |
| `llm.duration` | `ms` | LLM call duration histogram |
| `tool.duration` | `ms` | Tool execution duration histogram |
| `embedding.duration` | `ms` | Embedding duration histogram |

---

## Errors

`observer.Init` propagates errors from the OTEL SDK exporters and instrument construction. The most common cause is a misconfigured `OTEL_EXPORTER_OTLP_ENDPOINT`. If `Init` fails, call the returned shutdown function only if it is non-nil (it will be nil when `Init` errors).

`NewTracer` never errors. If called before `Init`, spans silently go to the no-op provider.

`WrapProvider`, `WrapTool`, and `WrapEmbedding` never error. They pass through all errors from the wrapped implementation unchanged.
