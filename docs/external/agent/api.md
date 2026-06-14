# Agent API

All symbols below live in `github.com/nevindra/oasis/agent` unless noted otherwise.
The root umbrella package (`github.com/nevindra/oasis`) re-exports the most common
ones — see the table at the bottom of the Options section.

---

## Types

### `LLMAgent`

```go
type LLMAgent struct { /* unexported fields */ }
```

The standard LLM-driven agent. Implements `core.Agent`. Safe for concurrent calls
from multiple goroutines — each `Execute` call is independent.

### `AgentTask`

```go
// github.com/nevindra/oasis/core
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
// github.com/nevindra/oasis/core
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

One entry per tool call: `Name`, `Type`, `Input` (truncated to 200 chars),
`Output` (truncated to 500 chars), `RawArgs`, `RawOutput` (untruncated), `Usage`,
`Duration`. Agent delegations strip the `agent_` prefix from `Name`.

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
Partial-override semantics: only non-zero fields take effect.
Use `Unbounded` (= -1) for `MaxSteps` when you want no cap on step retention.

Default values: `MaxIter=25`, `MaxSteps=100`, `MaxPlanSteps=50`,
`MaxParallelDispatch=10`, `MaxAttachmentBytes=50MB`, `MaxToolResultLen=100_000 runes`.

### `Generation`

```go
// alias of core.GenerationParams
type Generation struct {
    Temperature *float64
    TopP        *float64
    TopK        *int
    MaxTokens   *int
}
```

LLM sampling parameters. All fields are pointers — nil means "use provider default".
Pass to `WithGeneration`. Use `oasis.Ptr(v)` to obtain a typed pointer from a literal
(e.g. `oasis.Ptr(0.7)` gives `*float64`).

### `Processors`

```go
type Processors struct {
    Pre      []core.PreProcessor
    Post     []core.PostProcessor
    PostTool []core.PostToolProcessor
}
```

Groups the processor-chain hooks fired by the run loop. Pass to `WithProcessors`.
Fields are additive: multiple `WithProcessors` calls append rather than replace.

### `Hooks`

```go
type Hooks struct {
    PrepareStep         PrepareStep         // func(ctx, iter int, ctrl *StepControl) error
    OnIterationComplete OnIterationComplete // func(ctx, iter int, snap *IterationSnapshot) (IterationDecision, error)
    OnError             OnError             // func(ctx, iter int, err error) (ErrorDecision, error)
}
```

Groups mid-iteration callbacks invoked by the run loop. Pass to `WithHooks`.
Nil fields leave the corresponding hook untouched, so multiple `WithHooks` calls
compose per-field rather than replacing the whole bundle.

### `ToolConfig`

```go
type ToolConfig struct {
    Tools               []core.AnyTool
    Middleware           []core.ToolMiddleware
    Policies            map[string]core.ToolPolicy
    PolicyMatchers      []ToolPolicyMatcher
    Approvals           []ApprovalConfig
    ResultStore         core.ToolResultStore
    ResultStoreExplicit bool
}
```

Groups the tool subsystem's knobs into one typed sub-config. Use `WithTools` for
simple tool registration; use `WithToolConfig` when you need middleware, policies,
approval gates, or a custom result store. Fields are additive: `Tools`,
`Middleware`, `Approvals`, and `PolicyMatchers` append; `Policies` merges;
`ResultStore` replaces.

### `RunOptions`

Per-call overrides passed to `Execute` via `agent.WithOverrides(opts)`.
`nil` and `&RunOptions{}` are both equivalent to "use all agent defaults."

Key fields: `Prompt *string`, `Generation *Generation`, `ResponseSchema *core.ResponseSchema`,
`Limits *Limits`, `Tools []core.AnyTool` (replaces full set when non-nil),
`ActiveSkills []skills.Skill`, `PreProcessors / PostProcessors / PostToolProcessors`
(replace when non-nil), `PrepareStep / OnIterationComplete / OnError` (replace when
non-nil), `Memory *memory.AgentMemory`, `InputHandler`, `Tracer`, `Logger *slog.Logger`,
`Metadata map[string]string` (shallow-merged; call-site wins on conflict),
`StreamReplayLimit int` (replay buffer cap for `Subscribe`).

`Validate()` returns `*RunOptionsError` when a field is out of range. Called
automatically by `WithOverrides` before anything else runs.

### `StreamEvent` / `StreamEventType`

Events emitted into the channel wired by `core.WithStream`. The `Type` field
selects the event kind. Key fields: `Content` (text delta or result),
`Name` (tool/agent name), `Args` (tool call args), `Usage` (token counts),
`FinishReason` (on `EventRunFinish`), `Object` (structured output),
`SuspendPayload` (on suspend events).

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
| `EventThinking` | LLM reasoning/chain-of-thought content |
| `EventReasoningDelta` | Incremental reasoning chunk (extended thinking) |

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

Multi-reader fan-out wrapper. Constructed via `Subscribe`. Safe for concurrent use.

Key methods: `Events() <-chan core.StreamEvent` (subscribe; late subscribers receive
replay), `OnTextDelta(fn)`, `OnToolCall(fn)`, `OnToolResult(fn)`, `OnEvent(fn)` (typed
callbacks), `Done() <-chan struct{}`, `Result() (AgentResult, error)`,
`Text() string`, `Usage()`, `FinishReason()`, `Suspended()`, `SuspendPayload()`.

---

## Constructors

### `New` / `oasis.NewAgent`

```go
func New(name, description string, provider core.Provider, opts ...AgentOption) *LLMAgent
```

Builds an `LLMAgent`. `name` and `description` are required (empty strings are
accepted but produce poor logs and network routing). `provider` must be non-nil.
Returns `*LLMAgent` which implements `core.Agent`. Construction is not goroutine-safe;
build agents once and share them after.

The umbrella package (`github.com/nevindra/oasis`) re-exports this as `oasis.NewAgent`.

### `Subscribe` / `oasis.Subscribe`

```go
func Subscribe(ctx context.Context, ag core.Agent, task AgentTask, opts ...core.RunOption) *Stream
```

Runs `ag.Execute` in a background goroutine with `core.WithStream` wired up, and
returns a `Stream` the caller may subscribe to or query for the final result. Pass
additional `core.RunOption` values to layer overrides or deadlines. The `Stream` is
ready to subscribe to before the goroutine produces its first event.

### `Spawn` / `oasis.Spawn`

```go
func Spawn(ctx context.Context, agent Agent, task AgentTask, opts ...SpawnOption) *AgentHandle
```

Launches `agent.Execute` in a background goroutine. Returns immediately with an
`AgentHandle` for tracking, awaiting, and cancelling. The parent `ctx` controls the
agent's lifetime.

---

## Methods

### `LLMAgent.Execute`

```go
func (a *LLMAgent) Execute(ctx context.Context, task AgentTask, opts ...core.RunOption) (AgentResult, error)
```

Runs the tool-calling loop and blocks until it finishes. Returns the final result or
an error. When a processor suspends execution, returns `(AgentResult{}, *ErrSuspended)`
— check with `errors.As`. Context cancellation propagates immediately; the in-progress
LLM call is aborted and an error is returned.

Thread-safe: multiple goroutines may call `Execute` concurrently on the same agent.

Pass `core.RunOption` values to customize per-call behavior:

| Run option | Effect |
|-----------|--------|
| `core.WithStream(ch)` | Attach a buffered `chan<- core.StreamEvent`; agent closes it before returning |
| `agent.WithOverrides(opts)` | Apply `*RunOptions` overrides (prompt, limits, tools, processors, etc.) |
| `core.WithDeadline(d)` | Add a per-call wall-clock cap |

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

### `ErrSuspended.ResumeStream`

```go
func (e *ErrSuspended) ResumeStream(ctx context.Context, data json.RawMessage, ch chan<- core.StreamEvent) (AgentResult, error)
```

Like `Resume` but emits `StreamEvent` values into `ch` throughout. `ch` must be
buffered. The agent closes it before returning.

### `ErrSuspended.WithSuspendTTL`

```go
func (e *ErrSuspended) WithSuspendTTL(d time.Duration)
```

Sets an automatic expiry. After `d` elapses the snapshot is freed and `Resume`
returns an error. Override the default 30-minute TTL here.

### `ServeSSE`

```go
func ServeSSE(ctx context.Context, w http.ResponseWriter, agent core.Agent, task AgentTask) (AgentResult, error)
```

Streams the agent over HTTP as Server-Sent Events. Validates that `w` implements
`http.Flusher`; returns an error immediately if it does not. Sets SSE headers, runs
the agent in a background goroutine, writes each `StreamEvent` as
`event: <type>\ndata: <json>\n\n`, then writes a final `done` event and returns.
Client disconnection via `ctx` cancellation propagates to the agent.

---

## Options

Construction-time options (all are `AgentOption`, pass to `New`):

**Prompt and model**
- `WithPrompt(s)` — static system prompt (default: none).
- `WithDynamicPrompt(fn PromptFunc)` — per-call prompt resolver; overrides `WithPrompt`.
- `WithDynamicModel(fn core.ModelFunc)` — per-call provider swap.
- `WithDynamicTools(fn ToolsFunc)` — per-call tool replacement (replaces, not appends).

**Tools and limits**
- `WithTools(tools...)` — registers tools the LLM can call.
- `WithToolConfig(tc ToolConfig)` — registers tools together with middleware, policies, approval gates, and result-store override in one call.
- `WithLimits(lim Limits)` — resource-budget knobs; see `Limits` type for defaults.
- `WithGeneration(g Generation)` — sampling params (temperature, top-p, top-k, max-tokens).
- `WithPlanExecution()` — enables built-in `execute_plan` parallel-batching tool.
- `WithSandbox(sb core.Sandbox, tools ...core.AnyTool)` — attaches a sandbox and auto-registers its tools.

**Memory and knowledge**
- `WithMemory(opts...)` — wires store, history, recall, compaction, compression.
- `WithEmbedding(e core.EmbeddingProvider)` — embedding provider for semantic recall.
- `WithActiveSkills(skills...)` — pre-activates skills appended to every system prompt.
- `WithSkills(p skills.SkillProvider)` — runtime skill discovery via `skill_discover`/`skill_activate` tools.

**Processors and hooks**
- `WithProcessors(p Processors)` — wire `Pre`, `Post`, and `PostTool` processor chains in one call.
- `WithHooks(h Hooks)` — wire `PrepareStep`, `OnIterationComplete`, and `OnError` callbacks in one call.

**Infrastructure**
- `WithInputHandler(h InputHandler)` — enables `ask_user` tool + HITL suspend/resume.
- `WithResponseSchema(s *core.ResponseSchema)` — structured JSON output enforcement.
- `WithTracer(t core.Tracer)` — OTEL-backed span emission; auto-wires `OTelSpanMiddleware`.
- `WithLogger(l *slog.Logger)` — structured logging; default is no-op.
- `WithMetadata(kv map[string]string)` — static metadata merged into traces, hooks, and logs.
- `WithMiddleware(mws ...Middleware)` — wraps the agent's `Execute` method.
- `WithoutPromptCaching()` — opts the agent out of automatic cache-breakpoint placement.

### Tool middleware constructors

| Function | What it does |
|----------|-------------|
| `LoggingMiddleware(logger)` | Logs `tool.start` / `tool.finish` at `slog.Info` |
| `TimingMiddleware()` | Logs duration at `slog.Debug` |
| `OTelSpanMiddleware(tracer)` | Emits a `tool.execute` span; auto-wired when `WithTracer` is set |
| `TransformMiddleware(fn)` | Applies `fn` to the `ToolResult` before it returns to the LLM |

### Provider retry decorator

```go
// Wrap any Provider with retry on HTTP 429/503:
p := provider.Chain(agent.RetryMiddleware(agent.RetryMaxAttempts(3)))(base)

// Wrap an EmbeddingProvider:
ep := agent.WithEmbeddingRetry(embedder, agent.RetryMaxAttempts(3))
```

`RetryOption` values: `RetryMaxAttempts(n)` (default 3), `RetryBaseDelay(d)` (default
1s), `RetryTimeout(d)` (total cap across all attempts; 0 = no cap),
`RetryLogger(l)`.

### Umbrella re-exports

The `github.com/nevindra/oasis` package re-exports these agent symbols:

| Umbrella name | Agent package source |
|--------------|---------------------|
| `oasis.NewAgent` | `agent.New` |
| `oasis.Subscribe` | `agent.Subscribe` |
| `oasis.Spawn` | `agent.Spawn` |
| `oasis.WithStream` | `core.WithStream` |
| `oasis.WithOverrides` | `agent.WithOverrides` |
| `oasis.WithDeadline` | `core.WithDeadline` |
| `oasis.Ptr[T](v)` | generic helper (not aliasable as var) |
| `oasis.WithProcessors` | `agent.WithProcessors` |
| `oasis.WithHooks` | `agent.WithHooks` |
| `oasis.WithToolConfig` | `agent.WithToolConfig` |
| `oasis.WithTools` | `agent.WithTools` |
| `oasis.WithPrompt` | `agent.WithPrompt` |
| `oasis.WithGeneration` | `agent.WithGeneration` |
| `oasis.WithLimits` | `agent.WithLimits` |
| `oasis.WithMemory` | `agent.WithMemory` |
| `oasis.RetryMiddleware` | `agent.RetryMiddleware` |

---

## Errors

| Error | How to handle |
|-------|--------------|
| `*ErrSuspended` | Detect with `errors.As`; call `Resume` or `Release` |
| `*RunOptionsError` | Field validation failed; log `err.Field` + `err.Message`, fix the value |
| `context.Canceled / context.DeadlineExceeded` | Caller cancelled or timed out; propagated as-is |
| `*core.ErrHalt` | Processor signalled a graceful halt; the run returns `AgentResult{Output: halt.Response}` with no error |

`ToolResult.Error` (a string field on `ToolResult`) is NOT a Go error — it is a
business failure returned to the LLM so it can adapt. `ExecuteRaw` always returns
nil Go error for tool-level outcomes; Go errors from tools signal infrastructure
failures only.
