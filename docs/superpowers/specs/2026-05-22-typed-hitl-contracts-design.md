# Typed HITL Contracts — Design

> Date: 2026-05-22
> Status: Draft (awaiting user review)
> Owner: framework team
> Motivation: close the type-safety half of the Human-in-the-Loop gap from `docs/benchmarks/mastra-comparison.md` (Mastra 15 / Oasis 5 / Tie 1) for the long-lived Go server scenario, without introducing durable cross-process persistence.

## 1. Problem

Today every HITL pause in Oasis exchanges untyped `json.RawMessage` between the suspending site and the caller that resumes:

- `oasis.Suspend(payload json.RawMessage) error` — tool / step / processor pauses with raw bytes.
- `suspended.Resume(ctx, data json.RawMessage)` — caller hands raw bytes back.
- `suspended.Payload json.RawMessage` — caller reads context as raw bytes.
- Resume data is injected verbatim into the LLM history as `UserMessage("Human input: " + string(data))`.

Two real pain points come out of this:

1. **No compile-time contract between the suspend side and the resume side.** Tool author and caller must agree on payload shape by convention. A rename or refactor on one side silently breaks the other; the failure shows up at runtime, often deep inside an `errors.As` cast.
2. **The LLM sees raw JSON.** "Human input: `{\"approved\":true,\"reason\":\"ok\"}`" is what the model sees in the conversation — verbose, hostile to small models, and gives the protocol author no control over wording.

Mastra solves this with Zod `suspendSchema` / `resumeSchema` on each step/tool. The schemas validate at runtime and surface in Studio/MCP. For Oasis we want the same outcome — typed, contract-checked HITL — using a Go-idiomatic mechanism that:

- Stays consistent with the streaming overhaul (`oasis.StreamObjectAs[T]`, `oasis.ResultObjectAs[T]`) — free generics, no contagion of type parameters through `Agent` / `Workflow` / `Network`.
- Keeps `ErrSuspended` monomorphic so existing code does not break.
- Adds compile-time enforcement for the suspend side AND the resume side from a single declaration.

This is **spec #1 of a 5-spec HITL parity roadmap.** See §6 for the roadmap.

## 2. Non-goals

- **Durable cross-process suspend/resume persistence.** In-process closure state is acceptable; users opting into `Store`-backed persistence is a separate spec when the deployment scenario demands it.
- **Stream event parity for suspend.** `EventToolCallSuspended`, suspended-state envelope on `AgentResult`, suspended view in `IterationTrace` — separate spec (#2).
- **Multiple concurrent suspended paths surfaced per workflow `Execute()`.** Workflow today surfaces the first only. Fix is a separate spec (#3).
- **Reference approval UX surface.** `ServeApproval` HTTP/SSE handler, JS client snippet, optional React hook — separate spec (#4).
- **Cross-suspend tracing propagation.** Carrying span context through `Resume` — separate spec (#5).
- **Typed tool approval gate.** Today's `WithToolApproval` is *synchronous* — it blocks on `InputHandler.RequestInput` rather than using `Suspend`. A typed approval gate is a real DX win, but doing it right means redesigning the gate as suspend-based async (matching Mastra's `requireApproval`). That redesign deserves its own spec and is deferred to spec #6 (added to the roadmap below). Spec #1 leaves `WithToolApproval` untouched.
- **Typing `ask_user`.** `ask_user` stays free-form. Its purpose is mid-loop conversational clarification ("which user did you mean?"); typed structured input is what suspend protocols are for. The two primitives serve different needs and trying to unify them would make both worse.
- **Runtime Zod-like validation framework.** We do not introduce a generic schema engine. Type checks are compile-time at the protocol's call sites; the only runtime check is a single string tag mismatch.
- **JSON Schema auto-derivation on the protocol.** Could be added later for Studio/MCP discovery; not part of this spec.

## 3. Constraints

- **PHILOSOPHY-aligned.** All four constraints (Fast, DX, Future-Ready, Safe & Recoverable) must hold simultaneously.
- **`Agent` / `Workflow` / `Network` stay monomorphic.** No type parameter contagion through framework primitives — matches the streaming-world-class decision.
- **`ErrSuspended` stays a single struct.** No `ErrSuspended[Req, Resp]`. Internal payload bytes remain `json.RawMessage`.
- **Pre-v1.0.0 but additive.** No breaking changes. The untyped path (`Suspend`, `(*ErrSuspended).Resume`, `WithToolApproval`) keeps its current signature and semantics verbatim. Migration cost for existing HITL callers: zero.
- **Zero overhead when unused.** Tools / agents that never declare a protocol pay nothing.
- **Codegen-friendly.** An LLM coding assistant should produce correct typed HITL code with one declaration and reference. No `[Req, Resp]` type-args boilerplate at call sites (Go's type inference covers it).
- **Leaf-package invariant.** New protocol type lives in the `agent/` package (consistent with `ErrSuspended`). No `core/` changes required.

## 4. Design summary

### 4.1 The protocol value

A single new exported type in `agent/`, with a constructor and chainable configuration:

```go
// SuspendProtocol is a typed handle that pins the payload type (Req) sent
// to the human and the response type (Resp) sent back. Declare once,
// reference from both the suspending site and the caller that resumes.
//
// The zero value is not usable — construct with NewSuspendProtocol.
type SuspendProtocol[Req, Resp any] struct {
    name         string
    renderResume func(Resp) string
}

// NewSuspendProtocol declares a typed HITL contract. The name is a stable
// identifier used in error messages, the runtime tag check, and (later
// specs) for observability and Studio/MCP discovery. Names should be
// unique within a process; duplicates are not checked but a runtime tag
// mismatch on resume produces a clear error.
func NewSuspendProtocol[Req, Resp any](name string) SuspendProtocol[Req, Resp]

// WithRenderResume sets a formatter that converts the typed resume data
// into the natural-language message injected into the LLM's history.
// Default (when not set): JSON-marshal Resp and inject as
// "Human resumed `<protocol-name>`: <json>".
//
// Returns the protocol so calls can chain at declaration time.
func (p SuspendProtocol[Req, Resp]) WithRenderResume(fn func(Resp) string) SuspendProtocol[Req, Resp]

// Name returns the protocol's stable identifier.
func (p SuspendProtocol[Req, Resp]) Name() string
```

Protocols are declared as package-level vars next to the tool/agent that uses them, by convention:

```go
var ApproveTransfer = oasis.NewSuspendProtocol[TransferRequest, ApproveResponse]("approve_transfer").
    WithRenderResume(func(r ApproveResponse) string {
        if r.Approved {
            return fmt.Sprintf("Human approved the transfer. Reason: %s", r.Reason)
        }
        return fmt.Sprintf("Human declined the transfer. Reason: %s", r.Reason)
    })
```

### 4.2 Tool-side method: `Suspend`

```go
// Suspend returns an error that signals the engine to pause execution.
// The payload is JSON-marshaled with the protocol's tag attached, so
// callers using the same protocol value can read it typed via PayloadFrom.
//
// Semantic equivalence with oasis.Suspend(rawBytes): identical, except the
// resulting *ErrSuspended carries the protocol tag.
func (p SuspendProtocol[Req, Resp]) Suspend(payload Req) error
```

Usage in a workflow step:

```go
type ApprovalStep struct{}

func (s *ApprovalStep) Execute(ctx context.Context, wCtx *oasis.WorkflowContext) (any, error) {
    amount, _ := wCtx.Get("amount")
    if amount.(float64) > 1000 {
        return nil, ApproveTransfer.Suspend(TransferRequest{
            Amount: amount.(float64),
            To:     "external_account",
        })
    }
    // ... normal execution path
}
```

Usage in a processor (`PreProcessor` / `PostProcessor` / `PostToolProcessor`):

```go
func gateOnSensitiveAction(req *core.ChatRequest) error {
    if needsHumanReview(req) {
        return ApproveTransfer.Suspend(TransferRequest{ /* ... */ })
    }
    return nil
}
```

> Note: `Suspend` returns an `error` because that's the signaling primitive the engine catches today (`checkSuspendLoop`). The caller passing the wrong payload type triggers a compile error, not a runtime panic. Tools themselves do not call `Suspend` directly — per `PHILOSOPHY.md`, `Tool.Execute` always returns nil Go error. Suspend is invoked from workflow steps and processors that already use Go's error channel for control flow.

### 4.3 Caller-side methods: `PayloadFrom`, `Resume`, `ResumeStream`

```go
// PayloadFrom reads the suspended payload as the typed Req.
// Returns an error if the suspended err is nil, has a different protocol
// tag (mismatch), or the payload bytes don't unmarshal into Req.
func (p SuspendProtocol[Req, Resp]) PayloadFrom(e *ErrSuspended) (Req, error)

// Resume continues execution with the typed response data. Internally
// marshals data to JSON, runs RenderResume (or the default JSON formatter)
// to produce the LLM-visible message, and re-enters the engine loop.
//
// Returns an error on protocol tag mismatch, marshal failure, or any
// error the underlying (*ErrSuspended).Resume would return (released,
// expired, etc.).
func (p SuspendProtocol[Req, Resp]) Resume(e *ErrSuspended, ctx context.Context, data Resp) (AgentResult, error)

// ResumeStream is like Resume but emits StreamEvent values into ch.
// Semantic equivalence with (*ErrSuspended).ResumeStream, with the protocol
// tag check applied and the resume bytes coming from JSON-marshaled data.
func (p SuspendProtocol[Req, Resp]) ResumeStream(e *ErrSuspended, ctx context.Context, data Resp, ch chan<- StreamEvent) (AgentResult, error)
```

Usage in a server handler:

```go
result, err := ag.Execute(ctx, task)

var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    // Read payload typed — compiler enforces Req:
    payload, perr := ApproveTransfer.PayloadFrom(suspended)
    if perr != nil { /* handle mismatch / unmarshal error */ }

    // Present `payload` to the human via your UI of choice...

    // Resume typed — compiler enforces Resp:
    result, err = ApproveTransfer.Resume(suspended, ctx, ApproveResponse{
        Approved: true, Reason: "manager OK'd",
    })
}
```

### 4.4 Internal: the tag on `ErrSuspended`

`ErrSuspended` gets one new unexported field:

```go
type ErrSuspended struct {
    Step    string
    Payload json.RawMessage
    tag     string // NEW — empty when constructed via untyped Suspend; protocol Name when via protocol.Suspend
    // ... existing fields (resume, resumeStream, mu, ttlTimer, snapshotSize, onRelease)
}
```

The internal sentinel `errSuspend` gains the same field:

```go
type errSuspend struct {
    payload json.RawMessage
    tag     string // NEW
}
```

`SuspendProtocol[Req, Resp].Suspend(payload)` calls an internal helper:

```go
// agent/suspend.go (internal)
func suspendWithTag(tag string, payload json.RawMessage) error {
    return &errSuspend{payload: payload, tag: tag}
}
```

And `oasis.Suspend(json.RawMessage)` becomes a thin wrapper that calls `suspendWithTag("", payload)`.

`checkSuspendLoop` propagates the tag into the constructed `ErrSuspended`. Both the resume and resumeStream closures still take `json.RawMessage` — the protocol methods marshal `Resp` to bytes before calling them.

### 4.5 Render formatter

When the protocol-side `Resume` runs, it must produce the user-message text that gets appended to the conversation in place of today's `"Human input: " + string(data)`. The formatter selection is:

1. If the protocol has a `renderResume` function set, call it with the typed `Resp`.
2. Otherwise, JSON-marshal `Resp` and produce `"Human resumed `" + protocol.Name() + "`: " + string(jsonBytes)`.

The untyped path (`(*ErrSuspended).Resume(ctx, json.RawMessage)`) keeps the current verbatim format `"Human input: " + string(data)` for full backward compatibility.

Practical impact: a protocol with a formatter set gives the LLM a sentence; without one, it sees structured JSON tagged with the protocol name (already an improvement over today, since the protocol name lets the LLM disambiguate which approval came back).

### 4.6 What stays unchanged (back-compat)

- `oasis.Suspend(json.RawMessage) error` — same signature, same semantics.
- `(*ErrSuspended).Resume(ctx, json.RawMessage) (AgentResult, error)` — same.
- `(*ErrSuspended).ResumeStream(ctx, json.RawMessage, ch)` — same.
- `(*ErrSuspended).Release()`, `.WithSuspendTTL(d)` — same.
- `WithToolApproval(name, opts...)` — completely untouched in this spec. Typed approval gate is deferred to spec #6 (see roadmap).
- `WithInputHandler(h)` and `ask_user` — completely untouched. `ask_user` deliberately stays free-form (see Non-goals).
- All existing tests pass without modification. New tests cover the new surface only.

## 5. New exported surface

Two new exports in `agent/` (re-exported through `oasis.go`):

1. `type SuspendProtocol[Req, Resp any] struct{ ... }` (with methods: `Suspend`, `PayloadFrom`, `Resume`, `ResumeStream`, `WithRenderResume`, `Name`)
2. `func NewSuspendProtocol[Req, Resp any](name string) SuspendProtocol[Req, Resp]`

Re-exports in `oasis.go` follow the existing pattern (e.g., `oasis.SuspendProtocol`, `oasis.NewSuspendProtocol`) so users see the protocol surface from the umbrella package.

Internal additions (not exported):

- `errSuspend.tag string`
- `ErrSuspended.tag string`
- `suspendWithTag(tag string, payload json.RawMessage) error`

## 6. HITL roadmap context

This spec is #1 of 6. Sequencing is a recommendation; later specs may be reordered.

| # | Spec | One-line scope |
|---|------|----------------|
| **1** | **Typed HITL contracts** *(this spec)* | Protocol keys + methods (`Suspend` / `PayloadFrom` / `Resume` / `ResumeStream`). |
| 2 | Stream event parity | `EventToolCallSuspended`; `SuspendedPaths()` on result; suspended view in `IterationTrace`. |
| 3 | Concurrent suspends in workflows | Workflow `Execute()` surfaces all suspended steps at once. |
| 4 | Reference approval UX | `ServeApproval` HTTP/SSE handler + JS client snippet + (optional) React hook example. |
| 5 | Cross-suspend tracing | Propagate span context through `Resume` so spans link across pauses. |
| 6 | Typed async tool approval gate | Redesign `WithToolApproval` to use suspend-based async semantics with typed `SuspendProtocol`. Today's gate is synchronous via `InputHandler`; this spec moves it to `Suspend`/`Resume` so approval windows can be hours not seconds, matching Mastra's `requireApproval` model. |

Each subsequent spec gets its own brainstorm → design → plan cycle.

## 7. Implementation outline

Files touched / added:

- **New** `agent/suspend_protocol.go` (~120 LOC): `SuspendProtocol[Req, Resp]` type, constructor, methods, default formatter constant.
- **New** `agent/suspend_protocol_test.go` (~220 LOC): round-trip tests for each method, tag mismatch, render formatter override + default, streaming resume.
- **Edit** `agent/suspend.go`: add `tag string` to `errSuspend` and `ErrSuspended`; refactor `Suspend(payload)` to call `suspendWithTag("", payload)`; propagate tag through `checkSuspendLoop`. No public signature changes.
- **Edit** `oasis.go`: re-export `SuspendProtocol`, `NewSuspendProtocol`.
- **Edit** `CHANGELOG.md`: add entry under `[Unreleased]` describing the additive surface.
- **Edit** `docs/concepts/*` (likely `docs/concepts/hitl.md` or equivalent): document protocol keys, methods, render formatter, when to use untyped vs typed. Update existing HITL docs to point at the new path as recommended.
- **Edit** `docs/benchmarks/mastra-comparison.md`: flip the HITL "Resume data typing" and "Suspend payload typing" rows from Mastra → Tie. Update scorecard accordingly. Do NOT flip rows that depend on persistence, stream parity, or the async approval gate (those are later specs).

Estimated diff size: ~400–550 LOC added, ~30 LOC edited. Single PR.

## 8. Testing strategy

Test cases must cover, at minimum:

1. **Typed round-trip.** Declare protocol; suspend with `Req`; `PayloadFrom` returns the exact same value; `Resume` with `Resp` re-enters the loop and produces a result. Assert types are statically correct (compile-time) and values are byte-equal.
2. **Tag mismatch on `PayloadFrom`.** Suspend with protocol A, attempt `PayloadFrom` with protocol B. Assert error contains both names.
3. **Tag mismatch on `Resume`.** Suspend with protocol A, attempt `Resume` with protocol B. Assert error contains both names; the underlying `ErrSuspended` is *not* consumed (resume closure still callable via the correct path).
4. **Default render formatter.** Protocol without `WithRenderResume`; resume with a struct; assert injected user message matches ``"Human resumed `<name>`: " + json.Marshal(value)``.
5. **Custom render formatter.** Protocol with `WithRenderResume`; resume; assert injected user message matches the custom formatter output.
6. **Streaming resume.** Same as round-trip but via `ResumeStream`; assert events are received and the channel is closed.
7. **Untyped path unchanged.** Run an existing untyped Suspend/Resume test verbatim; assert no regression.
8. **Untyped `*ErrSuspended` interop.** Construct `ErrSuspended` via the legacy `oasis.Suspend(bytes)` path; attempt protocol `PayloadFrom`. Assert tag mismatch error (empty tag ≠ protocol name).
9. **TTL still works on typed Suspend.** Set a 50ms TTL via `(*ErrSuspended).WithSuspendTTL`; wait past it; assert `protocol.Resume` returns the expected "released/expired" error.
10. **Suspend budget interaction.** Confirm typed Suspend respects the agent's `WithSuspendBudget` like untyped does (count and bytes both increment correctly).
11. **Resume single-use semantics.** After `protocol.Resume` returns, a second call returns the "closure is nil" error — same as untyped today.

Coverage target: lines added in `agent/suspend_protocol.go` ≥ 90%.

## 9. Risk register

- **Generic method ergonomics.** Methods on generic struct types are well-supported (Go 1.18+; std uses them in `sync/atomic.Pointer`, `container/list`, `slices`), but error messages mentioning the type parameter pair may look noisier than method-less code. Mitigation: clear godoc examples, integration tests that exercise inference at call sites without explicit `[Req, Resp]`.
- **Tag collision.** Two protocols sharing a name string compile fine but cross-resume silently if the `Req`/`Resp` types also happen to align. Mitigation: documented convention to namespace protocol names (`"billing.approve_transfer"`); future spec could add `init()` registration with a one-process panic on duplicate names if needed.
- **`ask_user` users may want types too.** Documented decision (§2) is to leave it free-form. If demand emerges, a future spec can add a typed `ask_user` variant under a different name; no API in this spec forecloses it.
- **Users may expect typed approval gates from this spec.** This spec exposes `SuspendProtocol` but leaves `WithToolApproval` untouched. Mitigation: a clear note in the docs that the typed async approval gate ships in spec #6; until then, callers wanting typed approval can suspend manually from a `PreProcessor` keyed on tool name.

## 10. Open questions

None blocking. The following are deliberately deferred:

- Should the protocol carry a `Schema() *jsonschema.Schema` method for MCP/Studio introspection? → revisit when spec #4 (approval UX) lands and Studio integration is on the table.
- Should there be an `AnyProtocol` interface for cross-cutting middleware (e.g., a log-every-suspend wrapper)? → YAGNI today; add only when a real middleware needs it.
- Should `oasis.Suspend(json.RawMessage)` be marked `// Deprecated:` in godoc once typed lands? → No. The untyped path is a legitimate escape hatch (prototypes, scripts, dynamic payloads); leaving it un-deprecated matches `progressive disclosure` in `PHILOSOPHY.md`.

## 11. Acceptance criteria

- All existing HITL tests pass without modification.
- All new tests listed in §8 pass.
- `go build ./...` and `golangci-lint run ./...` clean.
- Existing untyped HITL example in `docs/concepts/` continues to compile and run.
- A new typed example exists alongside, ~20 LOC, demonstrating the full round-trip.
- The HITL "Resume data typing" and "Suspend payload typing" rows in `docs/benchmarks/mastra-comparison.md` flip from Mastra → Tie, with the scorecard updated accordingly. Other HITL rows stay unchanged (they depend on persistence, stream parity, or the async approval gate — all deferred to later specs).
- `CHANGELOG.md` `[Unreleased]` section names the new exports (`SuspendProtocol`, `NewSuspendProtocol`) and points at this spec.
