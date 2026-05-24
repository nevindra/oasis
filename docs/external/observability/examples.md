# Observability Examples

## Recipe 1: Inspect step traces after a run (no OTEL required)

**Goal:** See which tools ran, how long each took, and how many tokens each consumed — using only the data already in `AgentResult`.

```go
package main

import (
    "context"
    "fmt"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
)

func main() {
    ctx := context.Background()
    a := agent.New("assistant", "You are a helpful assistant.", provider,
        agent.WithTools(searchTool, calcTool),
    )

    result, err := a.Execute(ctx, agent.AgentTask{Input: "What is the capital of France?"})
    if err != nil {
        panic(err)
    }

    fmt.Printf("total tokens: input=%d output=%d cached=%d\n",
        result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.CachedTokens)

    for i, s := range result.Steps {
        fmt.Printf("step %d: [%s] %s — %s — %d tokens\n",
            i, s.Type, s.Name, s.Duration, s.Usage.InputTokens+s.Usage.OutputTokens)
    }
}
```

**Plain-English walkthrough:**
- No OTEL satellite needed. `result.Steps` is populated after every `Execute` call when tools were invoked.
- `s.Type` is `"tool"` or `"agent"` — useful when you have sub-agent delegations mixed in.
- `s.Duration` is wall-clock time. Compare steps to find your slowest tool.
- `s.Usage` is the token budget consumed by this specific step. Aggregate across steps to audit per-tool token cost.

**Variations:**
- Use `result.StepByTool("search")` to pull a specific step by name instead of ranging.
- Use `result.ToolCalls()` to get the raw JSON args for each tool call (safe for `json.Unmarshal`, unlike `s.Input`).
- Log steps to your existing `slog` handler: `slog.Info("step", slog.Any("step", s))`.

---

## Recipe 2: Add structured logging to an agent

**Goal:** Route agent lifecycle events (start, finish, errors, subagent calls) to your existing log pipeline using standard `slog`.

```go
package main

import (
    "context"
    "log/slog"
    "os"

    "github.com/nevindra/oasis/agent"
)

func main() {
    ctx := context.Background()

    logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
        Level: slog.LevelDebug, // set to Info in production
    }))

    a := agent.New("researcher", systemPrompt, provider,
        agent.WithLogger(logger),
        agent.WithTools(webSearch, summarize),
    )

    result, err := a.Execute(ctx, agent.AgentTask{Input: "Summarise the top AI news today"})
    _ = result
    _ = err
}
```

**Plain-English walkthrough:**
- `slog.NewJSONHandler` writes structured JSON to stdout — one log line per event.
- `WithLogger` hands the logger to the agent. The agent logs `agent started`, `agent finished`, tool dispatch warnings, and subagent errors automatically.
- Setting `Level: slog.LevelDebug` reveals subagent execution details. Switch to `slog.LevelInfo` for production.
- Any `slog.Handler` works: cloud logging SDKs, `slog-multi`, tint for coloured terminal output, etc.

**Variations:**
- Add a `slog.With` group for request-level fields: `logger.With("request_id", reqID)`.
- Route to multiple handlers with `slog-multi`: log at Debug to a file, at Error to an alerting sink.
- For a no-op logger (suppress all output): omit `WithLogger` — the agent uses a discard handler by default.

---

## Recipe 3: Full OTEL setup (traces + metrics + cost)

**Goal:** Send distributed traces, LLM metrics, and cost data to an OTEL-compatible backend (Jaeger, Grafana Tempo, Honeycomb, etc.).

```go
package main

import (
    "context"
    "os"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/observer"
)

func main() {
    ctx := context.Background()

    // OTEL_EXPORTER_OTLP_ENDPOINT controls where telemetry goes.
    // Example: export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
    inst, shutdown, err := observer.Init(ctx, observer.DefaultPricing)
    if err != nil {
        panic(err)
    }
    defer shutdown(ctx)

    tracer := observer.NewTracer()

    a := agent.New("analyst", systemPrompt, provider,
        agent.WithTracer(tracer),
    )

    result, _ := a.Execute(ctx, agent.AgentTask{Input: "Analyse this dataset"})
    _ = result
}
```

**Plain-English walkthrough:**
- `observer.Init` reads `OTEL_EXPORTER_OTLP_ENDPOINT` (and other standard `OTEL_*` vars) to set up exporters. No endpoint in env? Spans go to the no-op provider silently.
- `shutdown` must be called before the process exits — it flushes in-flight spans and metrics. Defer it immediately after calling `Init`.
- `observer.NewTracer()` fetches the tracer from the global OTEL provider that `Init` just configured.
- After wiring, every `Execute` call automatically emits `agent.execute`, `agent.iteration`, and `llm.generate` spans to your backend.

**Variations:**
- Set `OTEL_SERVICE_NAME` to distinguish multiple agents in the same backend.
- Use `observer.DefaultPricing` as the base and override specific models: `map[string]core.ModelPricing{"my-model": {InputPerMillion: 1.0, OutputPerMillion: 4.0}}`.
- View cost in your backend by querying the `llm.cost.total` counter filtered by `llm.model`.

---

## Recipe 4: Wrap provider and tools for per-call OTEL spans

**Goal:** Get an individual span for every LLM call and every tool execution, with token counts and latency on each span.

```go
package main

import (
    "context"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/observer"
    gemini "github.com/nevindra/oasis/provider/gemini"
)

func main() {
    ctx := context.Background()

    inst, shutdown, err := observer.Init(ctx, observer.DefaultPricing)
    if err != nil {
        panic(err)
    }
    defer shutdown(ctx)

    // Wrap the provider — each ChatStream call gets its own span.
    rawProvider := gemini.New(os.Getenv("GEMINI_API_KEY"))
    obsProvider := observer.WrapProvider(rawProvider, "gemini-2.5-flash", inst)

    // Wrap individual tools — each Execute call gets its own span.
    obsSearch := observer.WrapTool(searchTool, inst)
    obsSummarise := observer.WrapTool(summariseTool, inst)

    a := agent.New("analyst", systemPrompt, obsProvider,
        agent.WithTracer(observer.NewTracer()),
        agent.WithTools(obsSearch, obsSummarise),
    )

    _, _ = a.Execute(ctx, agent.AgentTask{Input: "What happened in AI this week?"})
}
```

**Plain-English walkthrough:**
- `WrapProvider` returns an `*ObservedProvider` that satisfies `core.Provider`. It emits `llm.chat_stream` spans and records `llm.token.usage`, `llm.cost.total`, and `llm.requests` metrics for every call.
- `WrapTool` returns an `*ObservedTool` that satisfies `core.AnyTool`. It emits `tool.execute` spans and records `tool.executions` and `tool.duration` metrics per invocation.
- The agent-level `WithTracer` span (`agent.execute`) becomes the parent; the provider and tool spans nest underneath it automatically via the context.
- `tool.status` on a tool span is `"tool_error"` when `ToolResult.Error` is set, `"error"` for infrastructure failures, and `"ok"` otherwise — useful for alerting on business-level tool failures separately from infrastructure failures.

**Variations:**
- Wrap just the provider and not the tools if tool-level granularity is too noisy.
- Use `observer.WrapEmbedding` to instrument an `EmbeddingProvider` the same way.
- Inspect token usage and cost in Grafana by joining `llm.token.usage` and `llm.cost.total` on the `llm.model` attribute.

---

## Recipe 5: Custom Tracer (no OTEL, custom backend)

**Goal:** Implement `core.Tracer` yourself to send traces to a proprietary backend, a test recorder, or a simple in-memory log.

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
)

// printTracer writes span start/end lines to stdout.
type printTracer struct{}

func (printTracer) Start(ctx context.Context, name string, attrs ...core.SpanAttr) (context.Context, core.Span) {
    fmt.Printf("SPAN START  %s %v\n", name, attrs)
    return ctx, &printSpan{name: name, start: time.Now()}
}

type printSpan struct {
    name  string
    start time.Time
}

func (s *printSpan) SetAttr(attrs ...core.SpanAttr) {}
func (s *printSpan) Event(name string, attrs ...core.SpanAttr) {
    fmt.Printf("SPAN EVENT  %s.%s\n", s.name, name)
}
func (s *printSpan) Error(err error) {
    fmt.Printf("SPAN ERROR  %s: %v\n", s.name, err)
}
func (s *printSpan) End() {
    fmt.Printf("SPAN END    %s (%s)\n", s.name, time.Since(s.start))
}

func main() {
    ctx := context.Background()
    a := agent.New("bot", "You are helpful.", provider,
        agent.WithTracer(printTracer{}),
    )
    _, _ = a.Execute(ctx, agent.AgentTask{Input: "Hello"})
}
```

**Plain-English walkthrough:**
- `core.Tracer` and `core.Span` are plain Go interfaces — implement them in ~30 lines.
- `printTracer.Start` returns the same context it received. That is correct when you don't need span propagation across goroutine/service boundaries. If you need real propagation, store a trace ID in the context and retrieve it in child spans.
- This pattern is also exactly how the agent test suite works (see `agent/span_test.go`): a `recordingTracer` captures span names for assertions.

**Variations:**
- Add a trace-ID to the context and log it on every slog call for correlation across logs and traces.
- Use the in-memory recorder pattern in unit tests to assert that specific spans are created.
- Forward events to any backend by adding an HTTP POST in `End()` — the interface imposes no transport constraint.
