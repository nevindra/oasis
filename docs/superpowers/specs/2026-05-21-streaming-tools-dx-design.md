# Streaming & Tools DX Layer — Design

> Date: 2026-05-21
> Status: Draft (awaiting user review)
> Owner: framework team
> Motivation: close the largest Streaming and Tool-System DX gaps from `docs/benchmarks/mastra-comparison.md` (Streaming: Mastra 11 / Oasis 3 / Tie 2; Tool System: Mastra 5 / Oasis 5 / Tie 10) while preserving the channel kernel's performance and respecting `docs/PHILOSOPHY.md`.

## 1. Problem

Today, Oasis streaming and tools have five production-pain DX gaps relative to Mastra:

1. **Single-reader streams.** `ExecuteStream(ctx, task, ch)` exposes one channel; multi-subscriber UIs need manual forwarding plumbing.
2. **Monolithic thinking events.** A single `EventThinking` is emitted at end-of-call with the full reasoning blob — UIs cannot render an incremental "thinking…" indicator.
3. **No convenience accessors on AgentResult or Stream.** Callers walk `Steps` by hand to pull tool calls / tool results / reasoning. Synchronous vs streaming code looks completely different.
4. **No tool middleware system.** Wrapping a tool with logging, tracing, transformation, or observability requires writing a wrapper struct and re-implementing `AnyTool` — or using `ToolPolicy`, which only covers retry/timeout.
5. **No framework-enforced approval gate.** `ask_user` covers LLM-initiated HITL, but there is no way to say "this specific tool always requires human approval before running, regardless of what the LLM decides."

These are addressed together because they share the same goal — making the everyday DX for agent UIs and tool composition match Mastra's polish — and because the streaming changes and tool changes both layer on the same channel kernel without modifying it.

## 2. Non-goals

- Replacing the channel-based `ExecuteStream` API. The channel stays the kernel; the helper is additive only.
- Promise-style fluent API (`stream.text` as a property). Go has no promises; we use blocking methods + `<-Done()`.
- Persistent / cross-process replay. Replay is in-process only, bounded by a ring buffer.
- New tool input/output schema mechanisms — solved by the [tool robustness layer](2026-05-21-tool-robustness-layer-design.md).
- Working memory primitive — separate spec (Tier 1 follow-up).
- Workflow-level streaming improvements — separate spec.
- WebSocket transport — `ServeSSE` over HTTP is the only built-in transport.

## 3. Constraints

- **PHILOSOPHY-aligned.** All four constraints (Fast, DX, Future-Ready, Safe & Recoverable) must hold simultaneously.
- **Kernel inviolate.** The single-reader `chan StreamEvent` API and `ExecuteStream` signature do not change.
- **Pre-v1.0.0.** Breaking changes are allowed with `CHANGELOG.md` entries, but this design is deliberately additive — no breaks.
- **Zero overhead when unused.** Tools without middleware pay no cost. Streams without a wrapper pay no cost. `Execute` callers pay no cost from the wrapper changes.
- **Memory bounded.** Every buffer in the new code has an explicit cap. No unbounded growth from late subscribers or replay history.
- **Leaf-package invariant.** `core/` cannot import anything from `oasis/...`. New code added to `core/` uses only stdlib.

## 4. Design summary

### 4.1 New stream event types (in `core/`)

Seven additions to the `StreamEventType` enum across this design:

- `EventReasoningStart` — emitted once at the start of a reasoning block.
- `EventReasoningDelta` — incremental reasoning text chunk (analogous to `EventTextDelta`).
- `EventReasoningEnd` — emitted once at the end of a reasoning block. `Content` may carry the full reassembled reasoning text for convenience.
- `EventHalt` — emitted when a processor returns `*ErrHalt`. `Name` carries the processor name; `Content` carries the canned response.
- `EventError` — emitted on terminal failure. `Content` carries the error message. Always followed by channel close.
- `EventStreamWarning` — emitted by the `Stream` wrapper (§4.2) on backpressure events. `Content` carries one of: `"replay-truncated"`, `"subscriber-dropped"`.
- `EventToolApprovalPending` — emitted by the approval middleware (§4.5) before a tool call requiring human approval. `ID` carries the tool call ID; `Name` carries the tool name; `Args` carries the proposed arguments.

`EventThinking` is kept as a deprecated alias and continues to be emitted for one minor release cycle. Providers that emit incremental reasoning (Claude extended thinking, OpenAI o1) start using the new triplet; providers that emit a single blob continue using `EventThinking` until ported. Deprecation removal goes in a future major.

Rejected alternatives:
- **Token-by-token tool args streaming** (`EventToolCallArgsDelta` separate from existing `EventToolCallDelta`): `EventToolCallDelta` already covers this. We do not need a second event type.
- **Subscriber-dropped / replay-truncated as separate types**: kept as sub-types on a single `EventStreamWarning` to avoid event-type proliferation.

### 4.2 Stream wrapper (`agent.Stream`)

A new opt-in type in `agent/stream_wrapper.go` (~300 LOC). Wraps the existing channel-based execution and exposes:

```go
// Construction
s := oasis.StartStream(ctx, ag, task)
s := oasis.StartStreamWith(ctx, ag, task, opts)  // RunOptions support

// Blocking accessors — wait for completion, return final values
s.Text() (string, error)
s.Result() (oasis.AgentResult, error)
s.ToolCalls() []oasis.ToolCall
s.ToolResults() []oasis.ToolResult
s.Reasoning() string
s.Usage() oasis.Usage

// Live subscription — multiple readers OK, each gets all events
s.Events() <-chan oasis.StreamEvent
s.OnTextDelta(func(chunk string))
s.OnReasoningDelta(func(chunk string))
s.OnToolCall(func(call oasis.ToolCall))
s.OnToolResult(func(result oasis.ToolResult))
s.OnEvent(func(ev oasis.StreamEvent))  // catch-all

// Lifecycle
s.Done() <-chan struct{}
s.Close() error  // optional early termination
```

**Implementation sketch:**

1. `StartStream` allocates an internal channel of size `defaultIterChBufSize` (64) and a `*Stream` with a mutex-protected subscriber list and a bounded ring buffer.
2. A reader goroutine ranges over the internal channel, appends each event to the ring buffer, and fans it out to every registered subscriber.
3. Each `Events()` call allocates a fresh per-subscriber buffered channel (default 32) and copies the current ring buffer contents into it before adding the subscriber to the broadcast list. New events arrive after.
4. Callback subscribers (`OnTextDelta` etc.) are filtered server-side — the dispatcher matches the event type and only invokes the callback if it matches, avoiding goroutine-per-callback overhead.
5. When the upstream agent finishes, the dispatcher closes every subscriber channel and stores the final `AgentResult` for the blocking accessors.

**Replay buffer:**

- Default cap: 256 events (configurable via `oasis.WithStreamReplayLimit(n)` in `RunOptions`).
- When the buffer is full, oldest events drop. A single `EventStreamWarning` with `Content="replay-truncated"` is emitted once into the live stream so late subscribers can detect history loss.
- Hard cap (configurable, default 4096) above which the wrapper returns an error from `StartStream` — protects against misconfiguration.

**Backpressure policy:**

- The upstream agent → wrapper channel is full-backpressure (current behavior preserved).
- The wrapper → subscriber channels are **lossy under pressure**: if a subscriber's channel is full when an event arrives, the wrapper writes a single `EventStreamWarning` with `Content="subscriber-dropped"` into that subscriber's channel and closes it. The slow subscriber is removed from the broadcast list. Other subscribers continue receiving.
- This is the one deliberate break from end-to-end backpressure. Rationale: a single broken UI tab must not stall the agent.

Rejected alternatives:
- **Per-subscriber backpressure** (block the wrapper if any subscriber is slow): violates "Safe and Recoverable" — one dead subscriber stalls all others.
- **Unbounded replay buffer**: violates "memory bounding non-negotiable."
- **No replay**: late subscribers (e.g. a UI that connects mid-task) see nothing. The ring buffer + truncated warning is the minimal correct answer.

### 4.3 AgentResult accessors

Pure-function helpers on `AgentResult` in a new file `agent/result_accessors.go` (~80 LOC). They derive from existing fields — no new storage:

```go
func (r AgentResult) Text() string                // alias for r.Output
func (r AgentResult) ToolCalls() []core.ToolCall  // extracted from r.Steps
func (r AgentResult) ToolResults() []core.ToolResult  // extracted from r.Steps
func (r AgentResult) Reasoning() string           // alias for r.Thinking
func (r AgentResult) LastStep() StepTrace         // last entry or zero
func (r AgentResult) StepByTool(name string) (StepTrace, bool)
```

**Goal:** `Stream.ToolCalls()` and `AgentResult.ToolCalls()` return the same type and shape, so the only difference between sync and streaming code is liveness.

No interface or field changes. Pure ergonomics.

### 4.4 Tool middleware chain

A new type and option in `agent/tool_middleware.go` (~200 LOC framework + ~50 LOC each for built-ins):

```go
// In core/ (leaf):
type ToolMiddleware func(AnyTool) AnyTool

// In agent/:
oasis.WithToolMiddleware(mw ...core.ToolMiddleware) Option
```

**Order:** middlewares apply innermost-first — the first in the slice is closest to the tool, the last is the outermost wrapper. Same convention as `net/http` middleware. The final wrap order from innermost to outermost is:

```
[tool] → [user middleware in order] → [tool policy: retry+timeout] → [approval] → dispatch
```

This order is deliberate:
- **User middleware inside policy:** a retried call gets one OTel span / log entry per attempt — correct, because each retry is a real attempt.
- **Approval outside policy:** retries do not re-prompt the human.

**Built-in middlewares:**

- `oasis.LoggingMiddleware(logger *slog.Logger)` — logs start, finish, error, duration per call.
- `oasis.TimingMiddleware()` — example/reference implementation; mostly redundant with `StepTrace`.
- `oasis.OTelSpanMiddleware()` — emits a per-tool span. **Auto-applied** when an agent has both `WithTracer(tracer)` configured and no explicit `OTelSpanMiddleware` already in the user's middleware list. The detection is "is there an existing middleware of this type in the chain"; users opting out call `WithToolMiddleware(...)` with their own list and skip it.
- `oasis.TransformMiddleware(fn func(name string, result core.ToolResult) core.ToolResult)` — modify the `ToolResult` before it returns to the LLM. Pure function.

**Interaction with streaming tools:** middleware wrappers preserve the `StreamingAnyTool` interface via type assertion + delegation. A middleware that wraps a streaming tool with a non-streaming wrapper bypasses streaming (with a logged warning).

**Cost:** zero allocation when the middleware list is empty (early return in `applyToolMiddleware`).

Rejected alternatives:
- **Per-tool middleware (`WithMiddlewareForTool(name, mw)`):** can be implemented as a user middleware that switches on `tool.Name()`. No need for a separate axis.
- **Pre-/post-/error-hook middleware structs (Mastra style):** function-based middleware is more composable and matches Go conventions.

### 4.5 Tool approval gate

A new option built on the middleware chain — `agent/tool_approval.go` (~150 LOC):

```go
oasis.WithToolApproval(toolName string, opts ...ApprovalOption) Option

// ApprovalOption is a functional option mutating an internal approvalConfig.
type ApprovalOption func(*approvalConfig)

// Helpers:
func ApprovalPrompt(fn func(call core.ToolCall) string) ApprovalOption
func OnDeny(action DenyAction) ApprovalOption

type DenyAction int
const (
    DenyAskLLMToRevise DenyAction = iota  // return error ToolResult to LLM (default)
    DenyHalt                               // halt the run with *ErrHalt
)
```

Defaults when no options are passed: prompt is a generic `"Approve call to <toolName>?"`; deny action is `DenyAskLLMToRevise`.

**Flow:**

1. LLM emits a tool call for `delete_user`.
2. Approval middleware (outermost layer of the chain) intercepts before the tool runs.
3. Stream emits a new `EventToolApprovalPending` event with the call ID, tool name, and args.
4. Middleware calls `InputHandler.RequestInput` with a structured approval payload (`{"kind":"approval","tool":"delete_user","args":{...}}`).
5. Human responds:
   - **approve** → tool runs normally, normal event sequence resumes.
   - **deny** → behavior depends on `OnDeny`:
     - `DenyAskLLMToRevise`: returns `ToolResult{Error: "user denied call to delete_user"}` to the LLM so it can adapt.
     - `DenyHalt`: middleware returns `*ErrHalt`, the loop halts cleanly.
   - **modify** (advanced): modified args replace the LLM's args before the tool runs.

**Why a middleware, not a special case:**
- Composes predictably with logging/tracing/policy.
- No new code path in the loop — the existing dispatch sees a wrapped tool and runs it normally.
- Auto-extensible: users can write their own approval logic if the default doesn't fit.

**Interaction with `ask_user`:** both coexist. `ask_user` is LLM-initiated ("I want to ask the user"). `WithToolApproval` is framework-enforced ("this tool requires human approval"). They use the same `InputHandler` for the channel back to humans.

**New event type:** `EventToolApprovalPending` (listed in §4.1).

Rejected alternatives:
- **Approval flag on `Tool` interface:** forces every tool implementation to think about approval. Better as a middleware concern.
- **Global approval policy (regex/matcher style):** can be added later as `WithToolApprovalMatch(matcher, opts)` if needed. Start with exact names.

## 5. Public API additions

All additions are non-breaking. Re-exported on `oasis.*` for the curated surface where appropriate.

```go
// core/ (leaf — no oasis imports)
type ToolMiddleware func(AnyTool) AnyTool

const (
    EventReasoningStart      StreamEventType = "reasoning-start"
    EventReasoningDelta      StreamEventType = "reasoning-delta"
    EventReasoningEnd        StreamEventType = "reasoning-end"
    EventHalt                StreamEventType = "halt"
    EventError               StreamEventType = "error"
    EventStreamWarning       StreamEventType = "stream-warning"
    EventToolApprovalPending StreamEventType = "tool-approval-pending"
)

// agent/
type Stream struct { ... }

func StartStream(ctx context.Context, agent StreamingAgent, task AgentTask) *Stream
func StartStreamWith(ctx context.Context, agent StreamingAgentWithOptions, task AgentTask, opts *RunOptions) *Stream

func (s *Stream) Text() (string, error)
func (s *Stream) Result() (AgentResult, error)
func (s *Stream) ToolCalls() []core.ToolCall
func (s *Stream) ToolResults() []core.ToolResult
func (s *Stream) Reasoning() string
func (s *Stream) Usage() core.Usage
func (s *Stream) Events() <-chan core.StreamEvent
func (s *Stream) OnTextDelta(fn func(string))
func (s *Stream) OnReasoningDelta(fn func(string))
func (s *Stream) OnToolCall(fn func(core.ToolCall))
func (s *Stream) OnToolResult(fn func(core.ToolResult))
func (s *Stream) OnEvent(fn func(core.StreamEvent))
func (s *Stream) Done() <-chan struct{}
func (s *Stream) Close() error

func (r AgentResult) Text() string
func (r AgentResult) ToolCalls() []core.ToolCall
func (r AgentResult) ToolResults() []core.ToolResult
func (r AgentResult) Reasoning() string
func (r AgentResult) LastStep() StepTrace
func (r AgentResult) StepByTool(name string) (StepTrace, bool)

func WithToolMiddleware(mw ...core.ToolMiddleware) Option
func WithToolApproval(toolName string, opts ...ApprovalOption) Option

// Stream-specific run-time tunables: new field on the existing RunOptions struct
// (see agent/runoptions.go). Zero value preserves the default of 256 events.
type RunOptions struct {
    // ... existing fields ...
    StreamReplayLimit int  // 0 = default (256); negative panics at StartStream
}

// Built-in middlewares
func LoggingMiddleware(logger *slog.Logger) core.ToolMiddleware
func TimingMiddleware() core.ToolMiddleware
func OTelSpanMiddleware() core.ToolMiddleware
func TransformMiddleware(fn func(name string, r core.ToolResult) core.ToolResult) core.ToolMiddleware
```

Re-exports added to `oasis.go`: `StartStream`, `StartStreamWith`, `Stream`, `WithToolMiddleware`, `WithToolApproval`, `LoggingMiddleware`, `OTelSpanMiddleware`, `TransformMiddleware`, `ToolMiddleware`, `ApprovalOption`, `ApprovalPrompt`, `OnDeny`, `DenyAction`, `DenyAskLLMToRevise`, `DenyHalt`.

## 6. Testing strategy

- **Stream unit tests:** subscriber fan-out (3 subscribers all see all events), late-subscriber replay (events emitted before subscription appear in order), bounded buffer (1000 events with cap=100 yields exactly 100 + truncation warning), slow-subscriber drop (one slow subscriber doesn't block others, drop warning emitted).
- **Stream race tests:** `go test -race` on subscribe/unsubscribe under concurrent event production.
- **Result accessor tests:** golden tests asserting `Stream.ToolCalls()` and `AgentResult.ToolCalls()` return identical slices for the same execution.
- **Middleware tests:** order verification (innermost-first), zero-allocation happy path (`testing.AllocsPerRun`), auto-OTel detection (with tracer → applied, without → not applied, with explicit user middleware → not auto-applied).
- **Approval tests:** approve / deny / deny-halt flows; approval event ordering relative to tool-call-start; interaction with retry policy (single approval, retry inside the approved scope).
- **Backwards-compat tests:** existing `ExecuteStream` channel tests continue to pass unchanged.
- **Benchmarks:** add `BenchmarkStreamWrapperFanout` (N subscribers, M events) to track wrapper overhead. Target: <500ns per event per subscriber on the dispatcher hot path.

## 7. Migration

- All changes additive. No required migration for existing users.
- `EventThinking` continues to be emitted alongside `EventReasoningStart`/`Delta`/`End` for one minor release. Deprecation noted in `CHANGELOG.md`; removal targeted for the next major.
- `ServeSSE` updated to forward the new event types — no caller change required (it already forwards all events).

## 8. Phases

**Phase 1 — Foundations (one PR):**
- Add new event types to `core/stream.go`.
- Add `AgentResult` convenience accessors.
- Update `ServeSSE` (no behavior change; just verify new events flow).

**Phase 2 — Stream wrapper (one PR):**
- Implement `agent.Stream` with subscriber fan-out, ring buffer, `On*` callbacks, blocking accessors.
- `StartStream` / `StartStreamWith`.
- Add `StreamReplayLimit int` field to existing `RunOptions` struct.

**Phase 3 — Tool middleware (one PR):**
- `ToolMiddleware` type in `core/`.
- `WithToolMiddleware` option in `agent/`.
- Built-in `LoggingMiddleware`, `TimingMiddleware`, `OTelSpanMiddleware`, `TransformMiddleware`.
- Auto-OTel wiring.

**Phase 4 — Tool approval (one PR):**
- `WithToolApproval` + `ApprovalOption` + `DenyAction`.
- `EventToolApprovalPending` integration.
- Stream subscriber sees approval pending event; existing `InputHandler` carries the human channel.

Each phase is independently shippable. Phase 4 depends on Phase 3 (middleware chain). Phase 2 depends on Phase 1 (event types). Phase 3 is independent.

## 9. PHILOSOPHY check

- **Fast:** kernel unchanged. Wrapper adds ~500ns per event per subscriber (target). Middleware path is zero-overhead when empty. No new allocations on the channel hot path.
- **Best-in-class DX:** beginners use `StartStream(ctx, ag, task).Text()` — 1 line. The 10-line agent stays 10 lines. Power users keep the raw channel for max control.
- **Future-Ready:** `ToolMiddleware` is composition-friendly — future capabilities (per-tool cache, per-tool rate limit, sensitivity tagging) drop in without framework changes. Replay buffer + event types support future UIs (Studio-style trace viewer, web playground) without API breaks.
- **Safe and Recoverable:** every buffer bounded, slow subscribers dropped not blocking, `EventError` makes terminal failure observable on the stream, halt is a first-class event, approval gate makes destructive tools controllable.
- **Codegen-friendly:** every new function has a consistent shape — `On<EventName>(func(...))` for subscription, blocking method named after the data type for synchronous read. `Stream.ToolCalls() []ToolCall` == `AgentResult.ToolCalls() []ToolCall` — LLMs generate correct code on the first try regardless of whether the caller streams or executes.
