# Agent API

All symbols below live in `github.com/nevindra/oasis` (the root package umbrella).
Import that package to use them; you do not need to import `agent/` or `core/` directly
unless you are building framework extensions.

---

## Types

### `LLMAgent`

```go
type LLMAgent struct { /* unexported */ }
```

The standard LLM-driven agent. Implements `Agent`, `StreamingAgent`,
`AgentWithOptions`, and `StreamingAgentWithOptions`. Safe for concurrent calls from
multiple goroutines — each `Execute` call is independent.

### `AgentTask`

```go
type AgentTask struct {
    Input       string
    Attachments []Attachment
    ThreadID    string
    UserID      string
    ChatID      string
    Extra       map[string]any
}
```

Input to every agent. `Input` is the only required field. `ThreadID` scopes memory
history; omit it for stateless (single-turn) calls. `Extra` is pass-through metadata
the framework never reads — use it in dynamic resolvers and processors.

Builder methods return a copy (value receiver, safe to chain):
`task.WithThreadID(id)`, `task.WithUserID(id)`, `task.WithChatID(id)`.

### `AgentResult`

```go
type AgentResult struct {
    Output          string
    Thinking        string
    Attachments     []Attachment
    Usage           Usage
    Steps           []StepTrace
    FinishReason    FinishReason
    Sources         []Source
    Files           []Attachment
    Warnings        []string
    ProviderMeta    json.RawMessage
    SuspendPayload  json.RawMessage
    SuspendProtocol string
    Object          json.RawMessage
    Iterations      []IterationTrace
}
```

`Output` is the final model text. `Steps` records every tool call in chronological
order. `FinishReason` tells you why the loop ended (see `FinishReason` constants).
`Object` is populated when `WithResponseSchema` is set.

Convenience methods: `Text()` (= `Output`), `Reasoning()` (= `Thinking`),
`ToolCalls()`, `ToolResults()`, `LastStep()`, `StepByTool(name)`,
`Suspended()`, `SuspendedProtocol()`.

### `StepTrace`

One entry per tool call: `Name`, `Type` ("tool" or "agent"), `Input` (truncated to 200
chars), `Output` (truncated to 500 chars), `Usage`, `Duration`. Agent delegations strip
the `agent_` prefix from `Name`. Also aliased as `ToolCallTrace`.

### `Limits`

```go
type Limits struct {
    MaxIter             int
    MaxSteps            int
    MaxPlanSteps        int
    MaxParallelDispatch int
    MaxAttachmentBytes  int64
    MaxToolResultLen    int
    MaxSuspendSnapshots int
    MaxSuspendBytes     int64
}
```

Resource-budget knobs. Zero value = keep the agent's default. Pass to `WithLimits`.
Partial-override semantics: only non-zero fields take effect, so you can override
one knob without touching the rest. Use `Unbounded` (= -1) for `MaxSteps` when you
want no cap on step retention.

Default values: `MaxIter=25`, `MaxSteps=100`, `MaxPlanSteps=50`,
`MaxParallelDispatch=10`, `MaxAttachmentBytes=50MB`, `MaxToolResultLen=100_000 runes`.

### `Generation`

```go
type Generation struct {
    Temperature *float64
    TopP        *float64
    TopK        *int
    MaxTokens   *int
}
```

LLM sampling parameters. All fields are pointers — nil means "use provider default".
Pass to `WithGeneration`. Use `oasis.Ptr(v)` to get a pointer from a literal.

### `RunOptions`

Per-call overrides passed to `ExecuteWith` / `ExecuteStreamWith`. `nil` and
`&RunOptions{}` are both equivalent to "use all agent defaults."

Key fields: `Prompt *string`, `Generation *Generation`, `ResponseSchema *ResponseSchema`,
`Limits *Limits`, `Tools []AnyTool` (replaces full set when non-nil),
`PreProcessors / PostProcessors / PostToolProcessors []...` (replace when non-nil),
`PrepareStep / OnIterationComplete / OnError` (replace when non-nil),
`Memory *memory.AgentMemory`, `InputHandler`, `Tracer`, `Logger *slog.Logger`,
`Metadata map[string]any` (shallow-merged; call-site wins on conflict),
`StreamReplayLimit int` (replay buffer cap for `StartStream`).

`Validate()` returns `*RunOptionsError` when a field is out of range. Called
automatically by `ExecuteWith` before anything else runs.

### `StreamEvent` / `StreamEventType`

Events emitted by `ExecuteStream`. The `Type` field selects the event kind. Key fields:
`Content` (text delta or result), `Name` (tool/agent name), `Args` (tool call args),
`Usage` (token counts), `FinishReason` (on `EventRunFinish`), `Object` (structured
output), `SuspendPayload` (on suspend events).

Key event types:

| Constant | When emitted |
|----------|-------------|
| `EventRunStart` | First event on every stream |
| `EventTextDelta` | Incremental LLM text chunk |
| `EventToolCallStart` | Tool about to execute; `Name`+`Args` identify it |
| `EventToolCallResult` | Tool finished; `Content` carries the result |
| `EventUIComponent` | Tool produced a renderable component; `Name`/`Object` carry the component name + props JSON, `ID` correlates to the tool call |
| `EventIterationStart/Finish` | One LLM call iteration began/ended |
| `EventRunFinish` | Last event; `FinishReason` says why the run stopped |
| `EventToolCallSuspended` | Tool returned a `Suspend` error |
| `EventProcessorSuspended` | Processor returned a `Suspend` error |
| `EventObjectDelta/Finish` | Partial/final structured output (with `WithResponseSchema`) |

### `FinishReason`

| Constant | Meaning |
|----------|---------|
| `FinishStop` | Model produced a natural stop |
| `FinishMaxIter` | Hit `MaxIter` cap; loop forced synthesis |
| `FinishHalted` | Processor returned `*ErrHalt` |
| `FinishSuspended` | Run paused awaiting human input |
| `FinishError` | Run terminated with an error |
| `FinishLength` | Model hit `max_tokens` |
| `FinishContentFilter` | Provider safety filter blocked output |

### `ErrSuspended`

```go
type ErrSuspended struct {
    Step    string
    Payload json.RawMessage
    // unexported fields
}
```

Returned by `Execute` when a processor or tool calls `Suspend(payload)`. The struct
holds a captured snapshot of the conversation history. Call `Resume(ctx, data)` or
`ResumeStream(ctx, data, ch)` with the human's response to continue. Call `Release()`
if resuming will never happen (timeout, abandonment) to free the snapshot. Call
`WithSuspendTTL(d)` to set an automatic expiry (default: 30 minutes).

`Resume` and `ResumeStream` are single-use; calling either more than once is undefined
behavior.

### `Stream`

Multi-reader fan-out wrapper around `ExecuteStream`. Constructed via `StartStream` or
`StartStreamWith`. Safe for concurrent use.

Key methods: `Events() <-chan StreamEvent` (subscribe; late subscribers receive replay),
`OnTextDelta(fn)`, `OnToolCall(fn)`, `OnToolResult(fn)`, `OnEvent(fn)` (typed
callbacks), `Done() <-chan struct{}`, `Result() (AgentResult, error)`,
`Text() string`, `Usage()`, `FinishReason()`, `Suspended()`, `SuspendPayload()`.

---

## Constructors

### `NewLLMAgent`

```go
func NewLLMAgent(name, description string, provider Provider, opts ...AgentOption) *LLMAgent
```

Builds an `LLMAgent`. `name` and `description` are required (empty strings are
accepted but produce poor logs and network routing). `provider` must be non-nil.
Returns `*LLMAgent` which implements all four agent interfaces. Construction is not
goroutine-safe; build agents once and share them after.

### `StartStream`

```go
func StartStream(ctx context.Context, ag StreamingAgent, task AgentTask) *Stream
```

Runs `ag.ExecuteStream` in a background goroutine and returns a `Stream` immediately.
The `Stream` is ready to subscribe to before the goroutine produces its first event.

### `StartStreamWith`

```go
func StartStreamWith(ctx context.Context, ag StreamingAgentWithOptions, task AgentTask, opts *RunOptions) *Stream
```

Like `StartStream` but applies per-call `RunOptions`. Honors `opts.StreamReplayLimit`
(clamped to `[1, 4096]`; default 256 when unset).

---

## Methods

### `LLMAgent.Execute`

```go
func (a *LLMAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error)
```

Runs the tool-calling loop and blocks until it finishes. Returns the final result or
an error. When a processor suspends execution, returns `(AgentResult{}, *ErrSuspended)`
— check with `errors.As`. Context cancellation propagates immediately; the in-progress
LLM call is aborted and an error is returned.

Thread-safe: multiple goroutines may call `Execute` concurrently on the same agent.

### `LLMAgent.ExecuteStream`

```go
func (a *LLMAgent) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error)
```

Like `Execute` but emits `StreamEvent` values into `ch` throughout execution. `ch`
MUST be a buffered channel (recommended: 64+). The implementation always closes `ch`
before returning — do not close it yourself. Range over `ch` in a separate goroutine
or use `Stream` for ergonomic fan-out.

### `LLMAgent.ExecuteWith`

```go
func (a *LLMAgent) ExecuteWith(ctx context.Context, task AgentTask, opts *RunOptions) (AgentResult, error)
```

Like `Execute` with per-call overrides from `opts`. Validates `opts` first;
returns `*RunOptionsError` if any field is out of range. `nil` opts = plain `Execute`.

### `LLMAgent.ExecuteStreamWith`

```go
func (a *LLMAgent) ExecuteStreamWith(ctx context.Context, task AgentTask, ch chan<- StreamEvent, opts *RunOptions) (AgentResult, error)
```

Like `ExecuteStream` with per-call `RunOptions`. On validation failure the channel is
closed immediately and a `*RunOptionsError` is returned.

### `LLMAgent.Memory`

```go
func (a *LLMAgent) Memory() *memory.AgentMemory
```

Returns the agent's memory handle. Always non-nil; methods on a zero `AgentMemory`
(when `WithMemory` was not configured) safely no-op. Use this handle to call
`Remember`, `Recall`, `Forget`, `Pin` directly from application code.

### `ErrSuspended.Resume`

```go
func (e *ErrSuspended) Resume(ctx context.Context, data json.RawMessage) (AgentResult, error)
```

Continues the suspended run with the human's response. Single-use. Returns an error
if called after `Release()` or TTL expiry.

### `ErrSuspended.WithSuspendTTL`

```go
func (e *ErrSuspended) WithSuspendTTL(d time.Duration)
```

Sets an automatic expiry. After `d` elapses the snapshot is freed and `Resume` returns
an error. Override the default 30-minute TTL here.

### `ServeSSE`

```go
func ServeSSE(ctx context.Context, w http.ResponseWriter, agent StreamingAgent, task AgentTask) (AgentResult, error)
```

Streams the agent over HTTP as Server-Sent Events. Validates that `w` implements
`http.Flusher`; returns an error immediately if it does not. Sets SSE headers, runs
the agent in a background goroutine, writes each `StreamEvent` as
`event: <type>\ndata: <json>\n\n`, then writes a final `done` event and returns.
Client disconnection via `ctx` cancellation propagates to the agent.

---

## Options

Construction-time options (all are `AgentOption`, pass to `NewLLMAgent`):

**Prompt and model**
- `WithPrompt(s)` — static system prompt (default: none).
- `WithDynamicPrompt(fn PromptFunc)` — per-call prompt resolver; overrides `WithPrompt`.
- `WithDynamicModel(fn ModelFunc)` — per-call provider swap.
- `WithDynamicTools(fn ToolsFunc)` — per-call tool replacement (replaces, not appends).

**Tools and limits**
- `WithTools(tools...)` — registers tools the LLM can call.
- `WithLimits(lim Limits)` — resource-budget knobs; see `Limits` type for defaults.
- `WithGeneration(g Generation)` — sampling params (temperature, top-p, top-k, max-tokens).
- `WithToolPolicy(name, p)` — per-tool timeout + retry; exact name wins over matchers.
- `WithToolPolicyMatch(fn, p)` — predicate-matched policy (e.g. `mcp__*` prefix).
- `WithToolApproval(name, opts...)` — human-approval gate before a named tool runs.
- `WithToolResultStore(s)` — override paging store; `nil` disables result paging.
- `WithToolMiddleware(mws...)` — wrap all tools; first in list = innermost.
- `WithPlanExecution()` — enables built-in `execute_plan` parallel-batching tool.
- `WithSubAgentSpawning(opts...)` — enables built-in `spawn_agent` tool.
- `WithSandbox(sb, tools...)` — attaches a sandbox and auto-registers its tools.

**Memory and knowledge**
- `WithMemory(opts...)` — wires store, history, recall, compaction, compression.
- `WithEmbedding(e)` — embedding provider for semantic recall.
- `WithActiveSkills(skills...)` — pre-activates skills appended to every system prompt.
- `WithSkills(p)` — runtime skill discovery via `skill_discover`/`skill_activate` tools.

**Processors and hooks**
- `WithPreProcessors(...)` — run before each LLM call.
- `WithPostProcessors(...)` — run after each LLM response.
- `WithPostToolProcessors(...)` — run after each tool result.
- `WithPrepareStep(fn)` — mutate the per-iteration request, model, or tool set.
- `WithOnIterationComplete(fn)` — control loop continuation after each iteration.
- `WithOnError(fn)` — mid-loop error recovery decisions.

**Infrastructure**
- `WithInputHandler(h)` — enables `ask_user` tool + HITL suspend/resume.
- `WithResponseSchema(s)` — structured JSON output enforcement.
- `WithTracer(t)` — OTEL-backed span emission; auto-wires `OTelSpanMiddleware`.
- `WithLogger(l *slog.Logger)` — structured logging; default is no-op.
- `WithMetadata(kv)` — static metadata merged into traces, hooks, and logs.

### Tool middleware constructors

| Function | What it does |
|----------|-------------|
| `LoggingMiddleware(logger)` | Logs `tool.start` / `tool.finish` at `slog.Info` |
| `TimingMiddleware()` | Logs duration at `slog.Debug` |
| `OTelSpanMiddleware(tracer)` | Emits a `tool.execute` span; auto-wired when `WithTracer` is set |
| `TransformMiddleware(fn)` | Applies `fn` to the `ToolResult` before it returns to the LLM |

### Provider decorators

| Function | What it does |
|----------|-------------|
| `WithRetry(p, opts...)` | Wraps any `Provider` with retry on HTTP 429/503 |
| `WithEmbeddingRetry(p, opts...)` | Same for `EmbeddingProvider` |

`RetryOption` values: `RetryMaxAttempts(n)` (default 3), `RetryBaseDelay(d)` (default
1s), `RetryTimeout(d)` (total cap across all attempts; 0 = no cap),
`RetryLogger(l)`.

---

## Errors

| Error | How to handle |
|-------|--------------|
| `*ErrSuspended` | Detect with `errors.As`; call `Resume` or `Release` |
| `*RunOptionsError` | Field validation failed; log `err.Field` + `err.Message`, fix the value |
| `*ErrHTTP` | HTTP-level failure from a provider; `Status` and `Body` carry details |
| `*ErrLLM` | LLM-level failure; `Provider` and `Message` carry details |
| `context.Canceled / context.DeadlineExceeded` | Caller cancelled or timed out; propagated as-is |
| `*ErrHalt` | Processor signalled a graceful halt; the run returns `AgentResult{Output: halt.Response}` with no error |

`ToolResult.Error` (a string field on `ToolResult`) is NOT a Go error — it is a
business failure returned to the LLM so it can adapt. `Tool.Execute` (via `AnyTool`)
always returns nil Go error for tool-level outcomes; Go errors from tools signal
infrastructure failures only.
