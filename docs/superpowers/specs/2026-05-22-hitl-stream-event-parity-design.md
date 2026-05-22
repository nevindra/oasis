# HITL Stream Event Parity — Design

> Date: 2026-05-22
> Status: Draft (awaiting user review)
> Owner: framework team
> Motivation: close the "Stream chunk integration" and "Observability of suspended state" rows from `docs/benchmarks/mastra-comparison.md` (HITL section, both currently Mastra wins) by adding explicit per-source mid-stream suspend events and small accessor improvements on `IterationTrace`, `AgentResult`, and `Stream`. Spec #2 of the 6-spec HITL parity roadmap.

## 1. Problem

After spec #1 (typed contracts) shipped, the typed protocol value pins `Req`/`Resp` at compile time, the resume formatter shapes the LLM-visible message, and the runtime tag check catches mismatches. But the **stream side of suspend** is still threadbare:

1. **No mid-stream suspend event.** When a suspend fires from a tool dispatch, processor hook, or workflow step, the only signal a UI receives is the very last event on the stream: `EventRunFinish` with `FinishReason = FinishSuspended`. By then the channel is already closing. The UI cannot render a "Human, please decide" card in real time — it has to wait for the run to wind down and then introspect `AgentResult`.
2. **`IterationTrace` doesn't record per-iteration finish reason.** Consumers walking `result.Iterations` can see what model ran and how long each iteration took, but not why each iteration ended. Suspended iterations look the same as iterations that proceeded to a normal tool-call follow-up — caller has no way to identify the suspending iteration without external bookkeeping.
3. **Protocol name isn't on stream events.** Typed protocols (spec #1) carry a name/tag that travels with each suspension; today nothing in the stream carries that name forward. UIs cannot route the suspension to the right form/handler by protocol name without separately reading the typed `*ErrSuspended` after the run ends.
4. **No `Suspended()` predicate on `AgentResult` or `Stream`.** Callers write `result.FinishReason == oasis.FinishSuspended` everywhere — verbose, easy to miss, and inconsistent with the streaming overhaul that ships `SuspendPayload()` as a one-call accessor on both sync and streaming paths.

Mastra's parity points:
- `tool-call-approval` stream chunk → already covered by Oasis's existing `EventToolApprovalPending`.
- `tool-call-suspended` stream chunk → MISSING in Oasis. This spec adds it (plus the step / processor analogs).
- `output.status = 'suspended'` and `finishReason = 'suspended'` → already covered by Oasis's `FinishSuspended` enum value and `AgentResult.FinishReason` field.
- "Studio shows suspended steps" / `listWorkflowRuns({status: 'suspended'})` → outside scope (Studio UI is a separate problem; this spec gives Studio-equivalent code the data it needs).

## 2. Non-goals

- **Multiple concurrent suspended paths surfaced per workflow `Execute()`.** Today workflow surfaces only the first suspended step. This spec fires *events* for each suspending step but does NOT change the `Execute()` return to surface multiple `*ErrSuspended` values. That's spec #3 (concurrent suspends in workflows).
- **Cross-suspend tracing.** Propagating OTel span context through a `Resume` so spans link across pauses is spec #5.
- **Durable cross-process snapshot persistence.** Spec #2 is in-process only, same as #1.
- **Reference approval UX surface.** `ServeApproval` HTTP/SSE handler is spec #4.
- **Async tool approval gate.** Today's synchronous `WithToolApproval` is unchanged here; spec #6 redesigns it.
- **Studio / playground UI.** This spec gives the data; rendering it is downstream.
- **Backward-incompatible event renames.** All new event types and fields are additive. No existing event type or field is removed or renamed.

## 3. Constraints

- **PHILOSOPHY-aligned.** All four constraints (Fast, DX, Future-Ready, Safe & Recoverable) must hold simultaneously.
- **Additive only.** No existing stream consumer breaks. Every new field uses `omitempty` so JSON shape stays clean.
- **Zero overhead when not suspending.** The new fields cost ~40 bytes per `StreamEvent` instance (one empty string header + one nil slice header) regardless of payload size when empty. The new events fire only on actual suspends — rare by definition.
- **Mirror the existing event-family pattern.** Three new events follow the convention of `EventToolCall*` / `EventStep*`: by source, predictably named, easy to switch on.
- **Names track the streaming overhaul's lifecycle envelope.** The new events fit alongside `EventRunStart`/`EventRunFinish`/`EventIterationStart`/`EventIterationFinish` and the typed `FinishReason` enum without redundancy.
- **Leaf-package invariant.** New event-type constants and `StreamEvent` fields live in `core/`. New accessors live in `agent/`. No new `core/` imports.

## 4. Design summary

### 4.1 Three new `StreamEventType` constants

```go
// core/stream.go — appended to the existing event-type block

// EventToolCallSuspended is emitted when a tool dispatch (the tool's
// ExecuteRaw call, a tool middleware, or a PostToolProcessor) returns a
// Suspend-class error. ID carries the tool call ID; Name carries the tool
// name; Args carries the tool's input arguments (same as the preceding
// EventToolCallStart); Protocol carries the typed protocol tag (empty for
// untyped Suspend); SuspendPayload carries the bytes from Suspend().
// No EventToolCallResult follows.
EventToolCallSuspended StreamEventType = "tool-call-suspended"

// EventStepSuspended is emitted when a workflow step's Execute returns
// a Suspend-class error. Name carries the step name; Protocol carries
// the typed protocol tag (empty for untyped Suspend); SuspendPayload
// carries the bytes from Suspend().
EventStepSuspended StreamEventType = "step-suspended"

// EventProcessorSuspended is emitted when a PreLLM, PostLLM, or PostTool
// processor returns a Suspend-class error. Name carries the processor's
// reflect.Type name; Protocol carries the typed protocol tag (empty for
// untyped Suspend); SuspendPayload carries the bytes from Suspend().
// Content carries the processor kind: "pre", "post", or "post-tool".
EventProcessorSuspended StreamEventType = "processor-suspended"
```

Event ordering on a suspending run:

```
EventRunStart
  EventIterationStart (iter=0)
    EventTextDelta / EventToolCallStart / ...
    EventToolCallSuspended   ← OR EventStepSuspended OR EventProcessorSuspended
  EventIterationFinish (iter=0, FinishReason=FinishSuspended)
EventRunFinish (FinishReason=FinishSuspended, SuspendPayload=<bytes>, Protocol=<tag>)
[channel close]
```

The mid-stream event fires *before* the iteration's finish event. The iteration finish carries the same `FinishReason=FinishSuspended` (consistent with the lifecycle envelope established by the streaming overhaul). `EventRunFinish` carries the final `SuspendPayload` and `Protocol` so consumers that ignore mid-stream events still get the data.

### 4.2 Two new fields on `StreamEvent`

```go
// core/stream.go — appended to StreamEvent struct fields

// Protocol carries the typed SuspendProtocol's tag on EventToolCallSuspended,
// EventStepSuspended, EventProcessorSuspended, EventToolApprovalPending,
// and EventRunFinish (when FinishReason=FinishSuspended). Empty for events
// not related to a suspend, and for suspends made via the untyped
// Suspend(json.RawMessage) escape hatch.
Protocol string `json:"protocol,omitempty"`

// SuspendPayload carries the suspend payload bytes on the same set of
// events listed under Protocol. Nil for non-suspend events. Distinct from
// Args (which carries tool call arguments) to avoid semantic overload —
// EventToolCallSuspended carries both: Args is the proposed tool input,
// SuspendPayload is the human-facing context.
SuspendPayload json.RawMessage `json:"suspend_payload,omitempty"`
```

Both fields use `omitempty` so existing JSON consumers see no shape change for non-suspend events.

### 4.3 `IterationTrace.FinishReason`

```go
// core/agent.go — appended to IterationTrace struct fields

// FinishReason is the reason this iteration ended. Carries
// FinishSuspended when a Suspend-class error fired during the iteration,
// FinishToolCalls when the iteration completed with tool calls pending,
// FinishStop when the model returned a natural end, etc. Empty only
// when the iteration is mid-run (during stream events). Mirrors the
// FinishReason emitted on EventIterationFinish.
FinishReason FinishReason `json:"finish_reason,omitempty"`
```

This is general-purpose: useful for finding the suspended iteration, but also for content-filter / length / halt diagnostics. The field is already computed by `agent/iteration.go:endIter` — currently set on the per-iteration `EventIterationFinish` event but not surfaced on the trace value passed to consumers.

### 4.4 `AgentResult.SuspendProtocol`

```go
// core/agent.go — appended to AgentResult struct fields (next to SuspendPayload)

// SuspendProtocol is set when FinishReason == FinishSuspended. Carries
// the typed protocol's tag from *ErrSuspended.tag (see SuspendProtocol[Req, Resp]).
// Empty for suspends made via the untyped Suspend(json.RawMessage) escape hatch.
SuspendProtocol string `json:"suspend_protocol,omitempty"`
```

Populated alongside the existing `SuspendPayload` field in `agent/iteration.go` wherever a `suspResult` is constructed (six call sites today).

### 4.5 Convenience predicates on `AgentResult` and `Stream`

```go
// agent/result.go — new methods on AgentResult

// Suspended reports whether the run paused awaiting human input.
// Shorthand for r.FinishReason == FinishSuspended.
func (r AgentResult) Suspended() bool { return r.FinishReason == FinishSuspended }

// SuspendedProtocol returns the typed protocol tag for a suspended run.
// Empty for untyped suspends or runs that did not suspend.
func (r AgentResult) SuspendedProtocol() string { return r.SuspendProtocol }
```

```go
// agent/stream.go (or wherever Stream blocking accessors live) — new methods on *Stream

// Suspended reports whether the run paused awaiting human input.
// Blocks on completion of the run. Identical shape to AgentResult.Suspended.
func (s *Stream) Suspended() bool {
    res, err := s.Result()
    if err != nil {
        return false
    }
    return res.Suspended()
}

// SuspendedProtocol returns the typed protocol tag for a suspended run.
// Blocks on completion of the run. Identical shape to AgentResult.SuspendedProtocol.
func (s *Stream) SuspendedProtocol() string {
    res, err := s.Result()
    if err != nil {
        return ""
    }
    return res.SuspendedProtocol()
}
```

### 4.6 Event emission points

The new events fire from existing suspend-detection sites in the engine. No new control-flow paths.

**`EventToolCallSuspended`**: emitted inside `agent/dispatch.go` when the tool dispatch returns a Suspend-class error. Lives next to the existing `EventToolCallResult` emission. Carries the tool call's ID + Name + Args from the dispatch context.

**`EventStepSuspended`**: emitted inside `workflow/exec.go` when a step's `Execute()` returns a Suspend-class error. Lives next to wherever the step's error path produces today's `EventStepFinish`. Carries the step name.

**`EventProcessorSuspended`**: emitted inside the processor chain (`agent/processors.go` or wherever the chain unwraps `errSuspend`). Carries the processor's `reflect.TypeOf(processor).Elem().Name()` as Name and the kind (`"pre"` / `"post"` / `"post-tool"`) as Content.

All three sites already detect suspension via `checkSuspendLoop` (or its workflow equivalent) and already pass through `errSuspend.tag` and `errSuspend.payload`. The emission is a one-line `select` on the stream channel.

### 4.7 What stays unchanged (back-compat)

- `EventToolApprovalPending` — unchanged signature today. The new `Protocol` field is on the struct (so future events can populate it) but `EventToolApprovalPending` keeps `Protocol == ""` until spec #6 introduces typed approval protocols. Consumers reading `Protocol` on approval events today must treat empty as "no protocol".
- `EventRunFinish` — unchanged signature; already carries `FinishReason`; now also carries `Protocol` and `SuspendPayload` when `FinishReason == FinishSuspended`. Consumers checking only `FinishReason` keep working.
- All existing `StreamEvent` fields and their JSON shapes — unchanged.
- All existing `AgentResult` fields and `IterationTrace` fields — additive only; no removed/renamed fields.
- `EventThinking`, `EventInputReceived`, `EventProcessingStart`, `EventMaxIterReached`, `EventHalt` — already deprecated by the streaming overhaul; no change here.
- No new exported types beyond the constants and accessors named above.
- No behavioral change to the untyped Suspend / Resume path. No behavioral change to typed Suspend / Resume from spec #1.

## 5. New exported surface

In `core/`:
- `StreamEventType` constant `EventToolCallSuspended = "tool-call-suspended"`
- `StreamEventType` constant `EventStepSuspended = "step-suspended"`
- `StreamEventType` constant `EventProcessorSuspended = "processor-suspended"`
- `StreamEvent` field `Protocol string`
- `StreamEvent` field `SuspendPayload json.RawMessage`
- `IterationTrace` field `FinishReason FinishReason`
- `AgentResult` field `SuspendProtocol string`

In `agent/`:
- `func (r AgentResult) Suspended() bool`
- `func (r AgentResult) SuspendedProtocol() string`
- `func (s *Stream) Suspended() bool`
- `func (s *Stream) SuspendedProtocol() string`

Re-exports in `oasis.go`: the event-type constants follow the existing pattern of being re-exported (e.g., `EventToolCallStart` is already there); the new ones go alongside. `AgentResult` and `Stream` methods don't need re-export — they're on aliased types.

## 6. HITL roadmap context

This spec is #2 of 6.

| # | Spec | Status |
|---|------|--------|
| 1 | Typed HITL contracts | ✅ Shipped (commit `9c870e6`) |
| **2** | **HITL stream event parity** *(this spec)* | Draft |
| 3 | Concurrent suspends in workflows | Pending |
| 4 | Reference approval UX | Pending |
| 5 | Cross-suspend tracing | Pending |
| 6 | Typed async tool approval gate | Pending |

## 7. Implementation outline

Files touched / added:

- **Edit** `core/stream.go`: add three event-type constants; add two `StreamEvent` fields.
- **Edit** `core/agent.go`: add `IterationTrace.FinishReason` field; add `AgentResult.SuspendProtocol` field.
- **Edit** `agent/iteration.go`: populate `IterationTrace.FinishReason` at every iteration-end site (grep `suspResult|errResult|FinishReason:` for the full list — currently around 8–12 sites). Populate `AgentResult.SuspendProtocol` at the same sites where `SuspendPayload` is already set.
- **Edit** `agent/dispatch.go`: emit `EventToolCallSuspended` when a tool dispatch returns a Suspend-class error. Pull the protocol tag and payload from the unwrapped `errSuspend`.
- **Edit** `agent/processors.go` (or wherever the processor chain handles errSuspend): emit `EventProcessorSuspended` from each of the three processor hook chains (PreLLM, PostLLM, PostTool) when one returns a Suspend-class error.
- **Edit** `workflow/exec.go`: emit `EventStepSuspended` when a step's `Execute()` returns a Suspend-class error. The workflow's stream channel is already plumbed.
- **Edit** `agent/result.go` (or wherever `AgentResult` methods live): add `Suspended()` and `SuspendedProtocol()` methods.
- **Edit** `agent/stream.go` (or wherever `Stream` blocking accessors live): add `Suspended()` and `SuspendedProtocol()` methods.
- **Edit** `agent/suspend.go`: at the `ErrSuspended` construction site in `checkSuspendLoop`, make sure the tag is propagated. (Spec #1 already does this; spot-check.)
- **New** test file(s) — `core/stream_test.go` if it exists (add JSON omitempty assertions), and `agent/suspend_event_test.go` (new) for the three event emission paths and the new accessors.
- **Edit** `oasis.go`: re-export the three new event-type constants.
- **Edit** `CHANGELOG.md`: add entry under `[Unreleased]`.
- **Edit** `docs/benchmarks/mastra-comparison.md`: flip "Stream chunk integration" and "Observability of suspended state" HITL rows from Mastra → Tie. Update HITL subtotal and overall scorecard.
- **Edit** `docs/concepts/hitl.md`: add a "Streaming suspends" section showing the event flow and the new accessors.

Estimated diff size: ~250–400 LOC added, ~40 LOC edited. Single PR.

## 8. Testing strategy

Test cases must cover at minimum:

1. **`EventToolCallSuspended` fires from a tool dispatch.** Configure a tool that returns a Suspend-class error via `core.ToolResult.Error` injection or a PostToolProcessor; assert the event appears in the channel with correct `ID`, `Name`, `Args`, `Protocol`, `SuspendPayload`.
2. **`EventStepSuspended` fires from a workflow step.** Configure a workflow step whose `Execute()` returns `Suspend(...)` or `protocol.Suspend(...)`; assert the event appears.
3. **`EventProcessorSuspended` fires from each processor kind.** Three sub-tests: PreLLM, PostLLM, PostTool. Each asserts the right `Content` discriminator and processor name.
4. **Event ordering.** `EventToolCallSuspended` appears *before* `EventIterationFinish` (which carries `FinishReason=FinishSuspended`), which appears *before* `EventRunFinish` (also `FinishSuspended`).
5. **Typed protocol propagation.** Use spec #1's `SuspendProtocol[Req, Resp]`; assert `ev.Protocol == protocol.Name()` on the mid-stream event AND on `EventRunFinish` AND on `AgentResult.SuspendProtocol`.
6. **Untyped suspend produces empty `Protocol`.** Use `oasis.Suspend(json.RawMessage(...))`; assert `ev.Protocol == ""` everywhere.
7. **`IterationTrace.FinishReason` records `FinishSuspended`.** Trigger a suspend on iteration 2; assert `result.Iterations[2].FinishReason == FinishSuspended` and earlier iterations have their natural finish reason (`FinishToolCalls` etc.).
8. **`AgentResult.Suspended()` returns true on suspend.** Trivial round-trip.
9. **`AgentResult.SuspendedProtocol()` returns the tag.** Same round-trip with a typed protocol.
10. **`Stream.Suspended()` blocks then returns true.** Verify the streaming-path accessor matches the sync-path accessor.
11. **`Stream.SuspendedProtocol()` blocks then returns the tag.** Same.
12. **JSON shape: omitempty.** Marshal a non-suspend `StreamEvent` and assert `protocol` and `suspend_payload` keys are absent. Marshal a suspending `StreamEvent` and assert both keys present with correct values.
13. **Channel closes cleanly after `EventRunFinish`.** Subscribe to the channel, exhaust it, assert close (`_, ok := <-ch; !ok`).

Coverage target: lines added in `agent/dispatch.go`, `agent/processors.go`, `workflow/exec.go` for event emission ≥ 90%.

## 9. Risk register

- **Event proliferation.** Three new event types now, more later. Mitigation: the family pattern is well-established (`EventToolCall*`, `EventStep*`); UI code switching on `Type` scales fine; the alternative (one unified event with a discriminator) was considered and rejected upstream.
- **`StreamEvent` struct size growth.** Two new fields = ~40 bytes per instance regardless of population. Mitigation: events are short-lived; the per-run count is bounded; benchmarks from the streaming overhaul show channel throughput is bottlenecked on goroutine scheduling, not struct copy.
- **Workflow event emission relies on stream channel availability.** If the workflow is invoked via `Execute()` (non-stream), `EventStepSuspended` has nowhere to go. Mitigation: guard the emission with the same `streamSinkFromContext` pattern used elsewhere — emit only when a sink exists.
- **`EventProcessorSuspended.Name` uses reflection.** Compute via `t := reflect.TypeOf(processor); if t.Kind() == reflect.Ptr { t = t.Elem() }; name := t.Name()` to handle both value and pointer receivers safely. Reflective but fast — processors suspend rarely; reflection-on-suspend is < 1µs and not on a hot path. Fall back to `"anonymous"` if the type has no name (rare — closure-wrapped processors).
- **`IterationTrace.FinishReason` populated at six sites.** Easy to miss one. Mitigation: code-review checklist; integration test that asserts every documented exit path sets `FinishReason` non-empty.
- **Existing UI consumers may not handle the new events.** Mitigation: all three events are skippable (UIs that filter to known types ignore unknowns); `EventRunFinish` still carries the data. The new events are purely additive UX.

## 10. Open questions

None blocking. Deferred:

- Should `EventProcessorSuspended` carry the processor kind as a separate field instead of squatting on `Content`? Considered — rejected because `Content` is already used by other events as a free-form short string and the three values are stable. If a 4th processor kind ever lands, revisit.
- Should `IterationTrace.FinishReason` be required (always populated) vs optional? Keeping it `omitempty` because for in-progress iterations during streaming it's legitimately empty. Setting a sentinel value adds nothing.
- Should `Stream.Suspended()` be non-blocking? No — same semantics as the existing `SuspendPayload()` blocking accessor; consistency wins over a niche async case.

## 11. Acceptance criteria

- All existing tests pass without modification.
- All new tests listed in §8 pass.
- `go build ./...` and `golangci-lint run ./...` clean.
- Three new event-type constants exist in `core/stream.go` and are re-exported from `oasis.go`.
- `StreamEvent` carries `Protocol` and `SuspendPayload` fields with `omitempty` JSON tags.
- `IterationTrace.FinishReason` is populated by every existing iteration-end site in `agent/iteration.go`.
- `AgentResult.SuspendProtocol` is populated wherever `SuspendPayload` is populated today.
- `AgentResult.Suspended()` / `.SuspendedProtocol()` exist and match `Stream.Suspended()` / `.SuspendedProtocol()` by name and return type.
- `docs/benchmarks/mastra-comparison.md` HITL "Stream chunk integration" and "Observability of suspended state" rows flip from Mastra → Tie. HITL subtotal and overall scorecard updated.
- `docs/concepts/hitl.md` gains a "Streaming suspends" section.
- `CHANGELOG.md` `[Unreleased]` entry names the new exports.
