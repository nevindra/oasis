# Streaming World-Class — Design

> Date: 2026-05-21
> Status: Draft (awaiting user review)
> Owner: framework team
> Motivation: close the remaining Streaming gaps from `docs/benchmarks/mastra-comparison.md` (Streaming: Mastra 11 / Oasis 3 / Tie 2 — biggest single-category gap after Storage and HITL) while preserving the channel kernel's performance and respecting `docs/PHILOSOPHY.md`.

## 1. Problem

After the [streaming & tools DX layer](2026-05-21-streaming-tools-dx-design.md) and [agent primitives + RunOptions](2026-05-21-agent-primitives-hooks-and-run-options-design.md) shipped, Oasis streaming has four production-relevant gaps relative to Mastra:

1. **No lifecycle envelope.** Consumers cannot distinguish "the run started" from "first event arrived." There is no `run-finish` event carrying `finishReason`, `warnings`, or `providerMetadata`. Channel close is the only end-of-stream signal. UI clients fall back to ad-hoc state machines.
2. **No structured object streaming.** When `WithResponseSchema` is set, the framework emits text deltas only. Mastra ships `objectStream` (partial typed object snapshots) and `elementStream` (one event per completed array element). This blocks the entire class of "show the user the form filling itself in" UX.
3. **Incomplete result accessor surface.** `AgentResult` exposes `Output`, `Thinking`, `Usage`, `ToolCalls()`, `ToolResults()`, `LastStep()`, `StepByTool()`. Mastra exposes ~25 lazy accessors including `sources`, `files`, `warnings`, `finishReason`, `providerMetadata`, `suspendPayload`. Six of these are real gaps; the rest are language artifacts.
4. **Single root span per run.** `agent.execute` wraps everything. There is no per-iteration span, no per-LLM-call span with model/temperature/token attributes. Trace UIs (Jaeger, Tempo, Grafana) cannot tell whether latency came from one slow tool, one slow model call, or many fast iterations.

These are addressed in one cohesive overhaul because the four parts share data: the lifecycle envelope's `FinishReason` lives on both the stream event and the result accessor; per-iteration spans align with `iteration-start` / `iteration-finish` events; structured object streaming emits events that flow through the same wrapper as everything else.

## 2. Non-goals

- WebSocket transport. `ServeSSE` over HTTP remains the only built-in wire format. WebSocket (and OpenAI Realtime) lives in a future voice/realtime satellite, not core streaming.
- Background-task streaming (the 10 chunk types Mastra emits for its `BackgroundTaskManager`). Oasis has no `BackgroundTaskManager`; `Spawn` covers a different surface and emits subagent events inline already.
- Promise-style fluent API. Go has no promises. Blocking methods + `<-Done()` is the idiom.
- Persistent / cross-process replay. Replay stays in-process, bounded by the existing ring buffer.
- Generic `Stream[T]` propagating through `Agent` / `Network` / `Workflow` / `RunOptions`. Considered and rejected in §6.2 — the contagion cost outweighs the ergonomic gain. Typed access is provided via free-function adapters.
- Patch-style structured-object deltas (JSON Patch / diff). Snapshot deltas are the chosen emission style (§6.2).
- Sources tracking inside individual tools that don't opt in. Sources are an opt-in capability (`core.Sourced`).

## 3. Constraints

- **PHILOSOPHY-aligned.** All four constraints (Fast, DX, Future-Ready, Safe & Recoverable) must hold simultaneously.
- **Kernel inviolate.** The single-reader `chan StreamEvent` API and `ExecuteStream` signature do not change. The kernel emits new event types; existing fields stay.
- **Pre-v1.0.0.** Breaking changes are allowed with a `CHANGELOG.md` migration note. This design takes the "maximally aggressive — design the v1 surface now" stance: we change `StreamEvent` shape (adding four optional fields), deprecate four special-case events in favor of the lifecycle envelope, and rename `StepTrace` → `ToolCallTrace` for naming consistency with the new `IterationTrace` / `LLMCallTrace`.
- **Zero overhead when unused.** Consumers that ignore the new accessors pay nothing. Per-iteration spans cost one nil-pointer check per span when no tracer is configured.
- **No `any` at the boundary.** Where typing matters, we use generics (free functions). Where bytes are the right boundary type, we use `json.RawMessage` — the same pattern already in use for `ToolResult.Content`, `StreamEvent.Args`, and `Tool` inputs.
- **Leaf-package invariant.** `core/` cannot import anything from `oasis/...`. New code in `core/` uses only stdlib.
- **Memory bounded.** Snapshot-style object deltas accumulate. We cap at 256 KiB per snapshot; if a single response exceeds the cap, we emit one final `object-finish` and skip intermediate snapshots.

## 4. Design summary

### 4.1 Lifecycle envelope (new events in `core/stream.go`)

Four new event types frame every run and every iteration:

```go
EventRunStart         StreamEventType = "run-start"
EventRunFinish        StreamEventType = "run-finish"
EventIterationStart   StreamEventType = "iteration-start"
EventIterationFinish  StreamEventType = "iteration-finish"
```

Wire shape:

```
run-start
  iteration-start  (LLM call #1)
    text-delta, tool-call-start, tool-call-result, ...
  iteration-finish
  iteration-start  (LLM call #2)
    text-delta, ...
  iteration-finish
run-finish
[channel close]
```

`EventRunFinish` carries new fields populated only on the finish event. The `Object` field is shared with `EventObjectDelta` / `EventObjectFinish` (see §4.2):

```go
type StreamEvent struct {
    // existing fields unchanged ...

    // Populated on EventRunFinish only:
    FinishReason  FinishReason     `json:"finish_reason,omitempty"`
    Warnings      []string         `json:"warnings,omitempty"`
    ProviderMeta  json.RawMessage  `json:"provider_meta,omitempty"`

    // Populated on EventObjectDelta / EventObjectFinish (§4.2):
    Object        json.RawMessage  `json:"object,omitempty"`
}

type FinishReason string

const (
    FinishStop          FinishReason = "stop"
    FinishToolCalls     FinishReason = "tool-calls"
    FinishLength        FinishReason = "length"
    FinishContentFilter FinishReason = "content-filter"
    FinishHalted        FinishReason = "halted"
    FinishSuspended     FinishReason = "suspended"
    FinishMaxIter       FinishReason = "max-iterations"
    FinishError         FinishReason = "error"
)
```

**Deprecation:** `EventInputReceived`, `EventProcessingStart`, `EventMaxIterReached`, and `EventHalt` are collapsed into the envelope:

- `EventInputReceived` + `EventProcessingStart` → `EventRunStart` (single event; task input on `Content`).
- `EventMaxIterReached` → `EventRunFinish{FinishReason: FinishMaxIter}`.
- `EventHalt` → `EventRunFinish{FinishReason: FinishHalted}` (canned response on `Content`, processor name on `Name`).

The four deprecated events remain as exported constants for one minor release cycle (no emission after this design lands) and are removed in the next major. `CHANGELOG.md` carries a migration note showing the 1:1 mapping.

The channel still closes after `EventRunFinish` — back-compat for consumers that loop until close.

### 4.2 Structured object streaming (Option B — kernel events + free-function adapters)

When the agent has `WithResponseSchema(...)` configured, three new events stream the partial structured object alongside the existing `EventTextDelta`:

```go
EventObjectDelta   StreamEventType = "object-delta"
EventObjectFinish  StreamEventType = "object-finish"
EventElementDelta  StreamEventType = "element-delta"  // top-level array schemas only
```

Snapshot emission: each `EventObjectDelta` carries a complete, valid JSON snapshot of "everything parseable so far" in the new `Object json.RawMessage` field on `StreamEvent`. `EventObjectFinish` carries the final validated object. For top-level array schemas (e.g. `[]ToDoItem`), `EventElementDelta` fires once per completed element instead of growing-array snapshots.

Forgiving parser: a new `core/partial_json.go` parser accepts incomplete JSON input and returns the most-complete valid JSON snapshot it can produce (closing open strings, dropping incomplete tail values, terminating open objects/arrays). Pure stdlib, zero dependencies, unit-tested against captured fixtures from real provider streams (Gemini, Anthropic, OpenAI).

Typed adapters (free functions, package-level):

```go
// Stream typed partial-object snapshots into a typed channel.
func StreamObjectAs[T any](s *Stream) <-chan T

// Decode the final result into T. Returns error if schema validation fails.
func ResultObjectAs[T any](r AgentResult) (T, error)
```

`StreamObjectAs[T]` registers a subscriber on the existing `Stream` fan-out, runs a goroutine that decodes each `EventObjectDelta.Object` into `T`, and forwards on a typed channel. Multiple typed readers coexist with the raw `Events()` reader at zero coordination cost.

User code:

```go
type Report struct {
    Title    string   `json:"title"`
    Sections []string `json:"sections"`
}

ag := agent.NewLLMAgent(...,
    agent.WithResponseSchema(oasis.SchemaFor[Report]()),
)

stream := oasis.StartStream(ctx, ag, task)
for partial := range oasis.StreamObjectAs[Report](stream) {
    ui.Render(partial)
}
result, _ := stream.Result()
final, err := oasis.ResultObjectAs[Report](result)
```

Rejected alternatives — see §6.2.

### 4.3 Result accessor parity

`AgentResult` gains six fields, populated when applicable, zero-value otherwise:

```go
type AgentResult struct {
    // existing fields unchanged ...

    Sources         []Source          `json:"sources,omitempty"`
    Files           []Attachment      `json:"files,omitempty"`
    Warnings        []string          `json:"warnings,omitempty"`
    FinishReason    FinishReason      `json:"finish_reason"`
    ProviderMeta    json.RawMessage   `json:"provider_meta,omitempty"`
    SuspendPayload  json.RawMessage   `json:"suspend_payload,omitempty"`
    Object          json.RawMessage   `json:"object,omitempty"`
    Iterations      []IterationTrace  `json:"iterations,omitempty"`
}
```

`Source` is a new exported type with conservative shape:

```go
type Source struct {
    URL    string          `json:"url,omitempty"`
    Title  string          `json:"title,omitempty"`
    Quote  string          `json:"quote,omitempty"`
    Origin string          `json:"origin,omitempty"` // "rag", "tool:<name>", "model"
    Meta   json.RawMessage `json:"meta,omitempty"`
}
```

Population sources:

| Field | Filled by |
|---|---|
| `Sources` | RAG retrievers (`HybridRetriever`, `GraphRetriever`) declare cited chunks via a new `core.Sourced` opt-in interface. Tools opting into the same interface contribute too. |
| `Files` | The agent loop aggregates `EventFileAttachment` events into the slice during the run. |
| `Warnings` | Providers emit warnings via a new `core.Warner` opt-in interface (a method on `Provider` implementations that wish to). Decorator providers (`WithRetry`, `WithRateLimit`) append their own warnings. |
| `FinishReason` | Set at the same place that emits `EventRunFinish`. |
| `ProviderMeta` | Provider's `Complete` / `ChatStream` returns `ProviderMeta json.RawMessage` (new optional field on `ChatResponse`). The agent passes it through unmodified. |
| `SuspendPayload` | Copied from `ErrSuspended` onto the result. |
| `Iterations` | Filled by the loop (see §4.4). |

`Stream` gains matching blocking accessors that block until completion and return the same data:

```go
func (s *Stream) Sources() []Source
func (s *Stream) Files() []Attachment
func (s *Stream) Warnings() []string
func (s *Stream) FinishReason() FinishReason
func (s *Stream) ProviderMeta() json.RawMessage
func (s *Stream) SuspendPayload() json.RawMessage
func (s *Stream) Iterations() []IterationTrace
```

Synchronous and streaming code now use identical accessor names.

### 4.4 Per-stream observability

Two new span layers under the existing `agent.execute` root:

```
agent.execute                                    (existing)
├── agent.iteration[N]                           (NEW)
│   ├── llm.generate                             (NEW)
│   └── tool.execute                             (existing — wired via OTelSpanMiddleware)
```

Span attributes:

- `agent.iteration`: `iter` (0-indexed), `tool_calls_count`, `duration_ms`.
- `llm.generate`: `model`, `provider`, `temperature`, `top_p`, `max_tokens`, `input_tokens`, `output_tokens`, `finish_reason`.

Created in `agent/loop.go` around the existing iteration boundary and around the `provider.ChatStream` call. Zero overhead when `tracer == nil` — one nil check per span.

`IterationTrace` exposes the same data without OTel:

```go
type IterationTrace struct {
    Iter      int
    Model     string
    StartedAt time.Time
    Duration  time.Duration
    LLMCall   LLMCallTrace
    ToolCalls []ToolCallTrace
    Usage     Usage
}

type LLMCallTrace struct {
    Duration     time.Duration
    InputTokens  int
    OutputTokens int
    FinishReason FinishReason
}
```

`ToolCallTrace` is the existing `StepTrace` renamed for consistency with `IterationTrace` and `LLMCallTrace`. `StepTrace` remains as a deprecated alias for one minor release.

The event stream is not used for span data — `iteration-start` / `iteration-finish` already provide event-stream consumers with the boundaries. Span data lives in the tracer subsystem (OTel) and on `AgentResult.Iterations` (introspection). Clean separation.

### 4.5 Provider plumbing

Two small additions to `core.Provider`-adjacent types:

```go
// New optional fields on ChatResponse. Existing providers ignore.
type ChatResponse struct {
    // existing fields ...

    FinishReason FinishReason     `json:"finish_reason,omitempty"`
    ProviderMeta json.RawMessage  `json:"provider_meta,omitempty"`
    Warnings     []string         `json:"warnings,omitempty"`
}

// Opt-in interfaces — implementations may declare these to contribute.
type Sourced interface {
    Sources() []Source
}

type Warner interface {
    Warnings() []string
}
```

`Sourced` is checked via runtime type assertion on tool results and RAG retrievers. `Warner` is checked on provider responses and decorator providers. Implementations that don't opt in pay nothing.

Native Gemini and OpenAI-compat providers in `provider/` are updated to populate `FinishReason` and `ProviderMeta` where the underlying APIs return that data. Providers that don't return finish reason leave the field empty — honest, not a bug.

## 5. Affected packages and exports

| Package | New exports | Modified exports | Removed/deprecated |
|---|---|---|---|
| `core` | `EventRunStart`, `EventRunFinish`, `EventIterationStart`, `EventIterationFinish`, `EventObjectDelta`, `EventObjectFinish`, `EventElementDelta`, `FinishReason` (type + 8 constants), `Source`, `Sourced` (interface), `Warner` (interface), `IterationTrace`, `LLMCallTrace`, `ToolCallTrace`, new fields on `ChatResponse` | `StreamEvent` (4 new optional fields: `FinishReason`, `Warnings`, `ProviderMeta`, `Object`), `AgentResult` (8 new fields: `Sources`, `Files`, `Warnings`, `FinishReason`, `ProviderMeta`, `SuspendPayload`, `Object`, `Iterations`) | Deprecated (no longer emitted, exported constants kept for one minor): `EventInputReceived`, `EventProcessingStart`, `EventMaxIterReached`, `EventHalt`. Renamed: `StepTrace` → `ToolCallTrace` (alias kept; shape unchanged). |
| `core` (new file) | `partial_json.go` — forgiving JSON parser | — | — |
| `agent` | `StreamObjectAs[T]`, `ResultObjectAs[T]` (root re-exports), `Stream.Sources`, `Stream.Files`, `Stream.Warnings`, `Stream.FinishReason`, `Stream.ProviderMeta`, `Stream.SuspendPayload`, `Stream.Iterations` | `Stream` (new accessor methods) | — |
| `oasis` | Re-exports of the above where appropriate | — | — |
| `provider/gemini` | — | Populate `FinishReason`, `ProviderMeta` on `ChatResponse` | — |
| `provider/openaicompat` | — | Populate `FinishReason`, `ProviderMeta` on `ChatResponse` | — |
| `rag` | `HybridRetriever`, `GraphRetriever` implement `core.Sourced` | — | — |
| `observer` | — | Update OTel span helper to recognize the new span names | — |

## 6. Rejected alternatives

### 6.1 Span data on the event stream (`EventSpanFinished`)

Considered emitting span open/close as events so SSE consumers could see per-iteration timing live. Rejected: mixes telemetry with work-stream concerns. Section 4.1's `iteration-start` / `iteration-finish` already give event-stream consumers the boundaries; the rich span data belongs in the tracer subsystem and on `AgentResult.Iterations`. Two consumers, two clean channels.

### 6.2 Generic `Stream[T]` propagating through interfaces

Considered making `Stream[T any]`, `StreamingAgent[T any]`, `AgentResult[T any]`. Rejected:

- **Contagion.** Every consumer of `Agent` (`Network`, `Workflow`, `Spawn`, `RunOptions`, sub-agent spawning) becomes parameterized. The framework's deliberate monomorphic-agent stance — explicitly listed in the mastra comparison's "Generic type params" row where Mastra wins — would partly collapse.
- **Mismatch hazard.** Agents without `WithResponseSchema` need `Stream[any]`. Two parallel types (`Stream[Report]`, `Stream[any]`) split the API surface.
- **Eager parse cost.** Snapshot-style emission forces partial JSON parsing in the kernel even for consumers that only read SSE wire output.

The chosen Option B (untyped kernel, free-function generic adapters) achieves the same compile-time safety where users want it without any of the contagion. Adopted.

### 6.3 Patch-style object deltas (JSON Patch / diff)

Considered emitting only the JSON diff between successive partial states. Rejected: DX is materially worse (consumers must apply patches to a shadow state). Bytes saved are small after gzip compression. Snapshot wins on every axis except wire bytes, and wire bytes are not the binding constraint.

### 6.4 Per-element streaming for nested arrays (`object.sections[*]`)

Considered emitting `EventElementDelta` for arrays nested inside objects (e.g. each `Report.Section` as it completes). Rejected for v1: the cost is a JSONPath-like config ("which field path inside the schema is the streamed array?") plus a schema-aware partial parser that knows where in the document tree it is. Top-level array schemas only (`[]ToDoItem`) for now — these match against a top-level `[` and emit per-element. Nested-array streaming can be added later if real demand appears; the event type already exists and the API extension would be a new `WithStreamArrayPath("$.sections")` option, not a kernel change.

### 6.5 Token-by-token tool args streaming (separate event)

Considered adding `EventToolCallArgsDelta` distinct from existing `EventToolCallDelta`. Rejected as duplicative — `EventToolCallDelta` already streams tool args incrementally. We instead document the existing event more clearly in the godoc.

### 6.6 Stream `[N]` accessor versions of every result method

Considered exposing `Sources()`, `Files()`, etc. as both blocking (wait-until-done) and event-time (return what's accumulated so far) on `Stream`. Rejected: the event-time slice is non-deterministic and confuses semantics. Users who want event-time data subscribe via `Events()` or `OnEvent`. The blocking accessors deliberately match `AgentResult` semantics so the two paths are interchangeable.

## 7. Migration

This is a pre-v1.0.0 breaking change in spirit (event names move into the lifecycle envelope; `AgentResult` grows fields; new exported interfaces). The migration note in `CHANGELOG.md` lists:

1. `EventInputReceived`, `EventProcessingStart`, `EventMaxIterReached`, `EventHalt` are emitted only by deprecated paths. Replace with `EventRunStart` / `EventRunFinish` consumers.
2. `StepTrace` → `ToolCallTrace`. Alias kept for one minor; rename your variables.
3. `Stream` and `AgentResult` add fields. No existing field changes shape or name.
4. Providers that wish to populate `FinishReason` / `ProviderMeta` / `Warnings` need a one-time update; old providers continue to work with zero-value fields.

No code currently in the satellite repos breaks; the satellites that wire OTel (`observer`) see additional spans without changes to how they consume them.

## 8. Testing strategy

- **Lifecycle envelope:** unit tests assert event ordering (`run-start` first, `run-finish` last, iteration bookends around every LLM call). Table-driven test for each `FinishReason` (stop, tool-calls, length, content-filter, halted, suspended, max-iter, error).
- **Partial JSON parser:** fixtures captured from real Gemini / Anthropic / OpenAI streams across 20+ prompt/schema pairs. Property-based test: parser output is always valid JSON for any byte prefix of valid JSON.
- **`StreamObjectAs[T]`:** integration test against a captured-stream provider mock. Asserts monotone-growing partials, final equals all-accumulated, slow subscribers receive the standard `EventStreamWarning` drop.
- **Result accessors:** integration test exercising every population path (`Sources` from RAG, `Files` from sandbox tool, `Warnings` from rate-limited provider, `SuspendPayload` from `ask_user`, `Iterations` from a 3-iteration loop).
- **Spans:** OTel-recording fake tracer asserts `agent.iteration[0..N]` and `llm.generate` spans exist with expected attributes; total span count matches iteration count.
- **Zero-overhead regression:** benchmark `BenchmarkExecuteStream` confirms no regression when no `Tracer`, no `WithResponseSchema`, no `Stream` wrapper. Hot-path `for ev := range ch` of `EventTextDelta` events stays at current allocations/op.

## 9. Out of scope (future specs)

- Working memory primitive (Tier 1 follow-up — separate spec).
- WebSocket / realtime / voice transport (separate satellite).
- Cross-process suspend persistence (separate spec — needed for `SuspendPayload` to survive process restarts).
- Studio UI / dev playground (separate satellite — uses the new `Iterations` data).
- Persistent tracing context across suspend/resume.
- Sources auto-deduplication / ranking.

## 10. Open questions

None. All design choices are committed in this document. Implementation can begin against this spec.
