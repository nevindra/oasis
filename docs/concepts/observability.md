# Observability

Deep tracing and structured logging built into the core framework. The root package defines minimal `Tracer`/`Span` interfaces (zero OTEL imports); the `observer` package provides the OTEL-backed implementation. When no tracer is configured, all span creation is skipped (nil check) — zero overhead.

## Architecture

```text
User Code
  oasis.NewLLMAgent("bot", provider,
      oasis.WithTracer(observer.NewTracer()),
      oasis.WithLogger(slog.Default()),
  )
         │
Core Framework (root package)
  tracer.go ──► Tracer/Span interface (pure Go, no OTEL)
  agent.go  ──► spans via tracer (nil = no-op)
  memory.go ──► spans + slog
  workflow.go, network.go, retriever.go, ingest/
         │
observer/ (OTEL implementation)
  NewTracer()     ──► oasis.Tracer backed by otel.Tracer()
  WrapProvider()  ──► metrics + cost (unchanged)
  WrapTool()      ──► metrics (unchanged)
  WrapEmbedding() ──► metrics (unchanged)
  Init()          ──► registers TracerProvider + MeterProvider
         │
OTEL SDK
  TracerProvider → Jaeger / Grafana Tempo
  MeterProvider  → Prometheus / OTLP
```

## Setup

### Tracing (core framework)

Pass a `Tracer` to any component that supports it:

```go
tracer := observer.NewTracer() // OTEL-backed

agent := oasis.NewLLMAgent("bot", provider,
    oasis.WithTracer(tracer),
    oasis.WithLogger(slog.Default()),
)

wf := oasis.NewWorkflow("pipeline",
    oasis.WithWorkflowTracer(tracer),
    oasis.WithWorkflowLogger(slog.Default()),
)

retriever := oasis.NewHybridRetriever(store, emb,
    oasis.WithRetrieverTracer(tracer),
)

ingestor := ingest.NewIngestor(store, emb,
    ingest.WithIngestorTracer(tracer),
)
```

### Metrics (observer wrappers)

Provider, tool, and embedding metrics still use `observer.Init`:

```go
inst, shutdown, err := observer.Init(ctx, pricingOverrides)
defer shutdown(ctx)

// Provider — emits llm.chat, llm.chat_with_tools, llm.chat_stream spans + metrics
observed := observer.WrapProvider(provider, modelName, inst)

// EmbeddingProvider — emits llm.embed spans + metrics
observed := observer.WrapEmbedding(embedding, modelName, inst)

// Tool — emits tool.execute spans + metrics
observed := observer.WrapTool(tool, inst)
```

## Tracer / Span Interface

**File:** `tracer.go`

```go
type Tracer interface {
    Start(ctx context.Context, name string, attrs ...SpanAttr) (context.Context, Span)
}

type Span interface {
    SetAttr(attrs ...SpanAttr)
    Event(name string, attrs ...SpanAttr)
    Error(err error)
    End()
}

type SpanAttr struct {
    Key   string
    Value any
}

// Helpers
func StringAttr(k, v string) SpanAttr
func IntAttr(k string, v int) SpanAttr
func BoolAttr(k string, v bool) SpanAttr
func Float64Attr(k string, v float64) SpanAttr
```

## Span Hierarchy

### Agent (LLMAgent / Network)

```text
agent.execute (agent.name, agent.type, agent.status, tokens)
│
├─ agent.memory.load (thread_id)
│
├─ agent.loop.iteration (iteration, tool_count)
│   ├─ llm.chat_with_tools          ← observer/WrapProvider
│   └─ tool.execute (per tool)       ← observer/WrapTool
│
├─ agent.loop.iteration (iteration=1)
│   └─ ...
│
└─ agent.memory.persist (thread_id)
```

Network adds `agent.delegate` spans for sub-agent routing.

### Workflow

```text
workflow.execute (workflow.name, step_count, workflow.status)
│
├─ workflow.step (step.name, step.status, step.duration_ms)
├─ workflow.step
├─ workflow.step (concurrent)
└─ workflow.step
```

### Retrieval

```text
retriever.retrieve (retriever.type="hybrid"|"graph", topK, result_count)
```

### Ingestion

```text
ingest.document (source, title, strategy, content_type, doc_id, chunk_count)
```

## Structured Logging

All core framework components use `slog` for structured logging. Pass a logger via `WithLogger` (or `WithWorkflowLogger`, `WithRetrieverLogger`, `WithIngestorLogger`). When not set, a no-op logger is used.

### Log Levels

| Level | Used For |
|-------|----------|
| Debug | Step skipped, loop iteration details |
| Info | Agent start/complete, step completed/suspended, subagent delegation |
| Warn | Max iterations reached |
| Error | Memory persist failure, step failed, callback panic |

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `llm.token.usage` | Counter | Tokens consumed (by model, provider, direction) |
| `llm.cost.total` | Counter | Cumulative cost in USD |
| `llm.requests` | Counter | Request count (by model, method, status) |
| `llm.duration` | Histogram | Call latency in ms |
| `tool.executions` | Counter | Tool call count (by name, status) |
| `tool.duration` | Histogram | Tool latency in ms |
| `embedding.requests` | Counter | Embedding call count |
| `embedding.duration` | Histogram | Embedding latency in ms |

## Cost Tracking

```go
calc := observer.NewCostCalculator(overrides)
cost := calc.Calculate("gemini-2.5-flash", inputTokens, outputTokens)
```

Built-in pricing for common Gemini, OpenAI, and Anthropic models. Unknown models return `0.0`. Override or extend via config:

```toml
[observer.pricing."gpt-4o"]
input = 2.50
output = 10.00

[observer.pricing."my-custom-model"]
input = 1.00
output = 3.00
```

## OTEL Configuration

Standard environment variables:

| Variable | Description |
|----------|-------------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Collector endpoint (e.g., `http://localhost:4318`) |
| `OTEL_SERVICE_NAME` | Service name (defaults to `"oasis"`) |
| `OTEL_TRACES_SAMPLER` | Trace sampling strategy |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` or `http/protobuf` |

Enable via config:

```toml
[observer]
enabled = true
```

Or environment variable: `OASIS_OBSERVER_ENABLED=true`

## Built-in Execution Traces (No OTEL Required)

Every `AgentResult` includes a `Steps` field — a chronological `[]StepTrace` of every tool call and agent delegation that occurred during execution. This works out of the box with no observer setup:

```go
result, _ := network.Execute(ctx, task)

for _, step := range result.Steps {
    fmt.Printf("%-6s %-20s %5dms  in=%-4d out=%d\n",
        step.Type, step.Name, step.Duration.Milliseconds(),
        step.Usage.InputTokens, step.Usage.OutputTokens)
}
// agent  researcher           1234ms  in=500  out=200
// tool   web_search            456ms  in=0    out=0
// agent  writer               2100ms  in=800  out=400
```

`StepTrace.Type` is `"tool"` for direct tool calls, `"agent"` for Network subagent delegations, and `"step"` for Workflow steps.

Streaming consumers get the same data via `Usage` and `Duration` fields on `EventToolCallResult` and `EventAgentFinish` events.

## Migration from `observer.WrapAgent`

```go
// Before
observed := observer.WrapAgent(agent, inst)
result, err := observed.Execute(ctx, task)

// After
agent := oasis.NewLLMAgent("bot", provider,
    oasis.WithTracer(observer.NewTracer()),
    oasis.WithLogger(slog.Default()),
)
result, err := agent.Execute(ctx, task)
```

## See Also

- [Configuration Reference](../configuration/reference.md) — observer config section
- [Provider](provider.md) — what gets observed
- [Streaming](../guides/streaming.md) — StreamEvent usage/duration fields
