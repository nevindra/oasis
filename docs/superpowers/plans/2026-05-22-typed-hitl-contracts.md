# Typed HITL Contracts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a typed `SuspendProtocol[Req, Resp]` to `agent/` so suspend/resume contracts are compile-time enforced, without changing any existing untyped HITL API or making `Agent`/`Workflow`/`Network` generic.

**Architecture:** A new generic value type `SuspendProtocol[Req, Resp]` declared once and referenced by both the suspending site and the caller that resumes. Internally, two new fields on the existing `errSuspend` sentinel — `tag string` (the protocol's name) and `format func(json.RawMessage) string` (the type-erased LLM-message formatter) — propagate through `checkSuspendLoop` into the resume closure. The protocol's methods (`Suspend`, `PayloadFrom`, `Resume`, `ResumeStream`) marshal/unmarshal at the boundary and forward to the existing untyped engine entry points. `ErrSuspended` stays a single concrete struct.

**Tech Stack:** Go 1.24, generics, stdlib only (`encoding/json`, `errors`, `fmt`). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-22-typed-hitl-contracts-design.md`

---

## Project conventions for this plan

- **No auto-commits.** Per `~/.claude/RULES.md` and the user's standing instruction, do NOT run `git commit` between tasks. After every implementation task there is a `Show the user the diff` step instead of a commit step. The user batches commits and signs off at the end.
- **TDD strict.** Test first, run to fail, implement, run to pass.
- **Plain Go.** No new dependencies. No code generation. No reflection — type erasure is done via closure capture (same pattern as `oasis.StreamObjectAs[T]`).
- **Leaf-package invariant.** All new code in `agent/`. No `core/` edits.
- **Test pattern.** New tests mirror `agent/suspend_test.go` (uses `mockProvider`, direct `runLoop(ctx, cfg, task, nil)` invocation, `errors.As(err, &suspended)` for unwrap).
- **Test commands:**
  - Single test: `go test ./agent/ -run TestName -v`
  - All agent tests: `go test ./agent/...`
  - Whole module: `go test ./...`
  - Lint: `golangci-lint run ./...`

---

## File map

| File | Action | Lines | Responsibility |
|------|--------|-------|----------------|
| `agent/suspend_protocol.go` | **Create** | ~140 | `SuspendProtocol[Req, Resp]` type, constructor, methods, default formatter |
| `agent/suspend_protocol_test.go` | **Create** | ~280 | Unit + integration tests for typed contracts |
| `agent/suspend.go` | Modify | +20 -2 | Add `tag` + `format` to `errSuspend` and `ErrSuspended`; thread through `checkSuspendLoop` |
| `oasis.go` | Modify | +4 | Re-export `SuspendProtocol`, `NewSuspendProtocol`, `Suspend`, `ErrSuspended` |
| `CHANGELOG.md` | Modify | +6 | `[Unreleased]` entry naming the new surface |
| `docs/benchmarks/mastra-comparison.md` | Modify | ~10 | Flip "Resume data typing" + "Suspend payload typing" rows from Mastra → Tie; update HITL section subtotal and overall scorecard |
| `docs/concepts/hitl.md` | Modify or create | ~60 | Add typed-protocol section with example, mark untyped as "escape hatch" |

---

## Task 1: Add `tag` field to `errSuspend` and `ErrSuspended`

**What this does at runtime:** No behavior change. Just plumbs a protocol-name string from suspend site to `ErrSuspended` so future tasks can use it. The untyped `Suspend(json.RawMessage)` produces an empty tag.

**Files:**
- Modify: `agent/suspend.go:30-36` (errSuspend struct), `agent/suspend.go:41-43` (Suspend func), `agent/suspend.go:61-81` (ErrSuspended struct), `agent/suspend.go:311-345` (checkSuspendLoop)
- Test: `agent/suspend_protocol_test.go` (create)

- [ ] **Step 1.1: Create test file and write the first failing test**

Create `agent/suspend_protocol_test.go` with this content:

```go
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nevindra/oasis/memory"
)

// TestUntypedSuspendHasEmptyTag verifies the existing untyped Suspend path
// produces an ErrSuspended whose internal tag is empty. This guards against
// future protocol additions accidentally tagging the untyped path.
func TestUntypedSuspendHasEmptyTag(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{{Content: "done"}},
	}

	chain := NewProcessorChain()
	chain.AddPre(&suspendingPreProcessor{
		payload: json.RawMessage(`{"prompt": "ok?"}`),
	})

	cfg := LoopConfig{
		name:       "test",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}

	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}
	if suspended.tag != "" {
		t.Errorf("untyped suspend produced tag = %q, want empty string", suspended.tag)
	}
}
```

- [ ] **Step 1.2: Run the test to confirm it fails (tag field does not exist yet)**

Run: `go test ./agent/ -run TestUntypedSuspendHasEmptyTag -v`
Expected: build failure with `suspended.tag undefined` (or similar). This confirms the field is missing.

- [ ] **Step 1.3: Add `tag` field to `errSuspend` sentinel**

In `agent/suspend.go`, change the `errSuspend` struct (around line 32):

```go
// errSuspend is the internal sentinel returned by step functions to signal
// that execution should pause for external input. The workflow/network engine
// catches it and converts to ErrSuspended with resume capabilities.
type errSuspend struct {
	payload json.RawMessage
	tag     string // empty for untyped Suspend; protocol name for typed SuspendProtocol.Suspend
}

func (e *errSuspend) Error() string { return "suspend" }
```

- [ ] **Step 1.4: Update `Suspend` to pass empty tag**

In `agent/suspend.go`, change the `Suspend` function (around line 41):

```go
// Suspend returns an error that signals the workflow or network engine to
// pause execution. The payload provides context for the human (what they
// need to decide, what data to show).
//
// For typed payloads use SuspendProtocol.Suspend; this untyped form
// remains as an escape hatch for prototypes and dynamic payloads.
func Suspend(payload json.RawMessage) error {
	return &errSuspend{payload: payload}
}
```

> The struct literal omits `tag` deliberately — Go zero-values it to `""`. No code change needed beyond the existing call site.

- [ ] **Step 1.5: Add `tag` field to `ErrSuspended` struct**

In `agent/suspend.go`, change the `ErrSuspended` struct (around line 61):

```go
type ErrSuspended struct {
	// Step is the name of the step or processor hook that suspended.
	Step string
	// Payload carries context for the human (what to show, what to decide).
	Payload json.RawMessage
	// tag identifies the SuspendProtocol used to construct this suspension,
	// or "" if constructed via the untyped Suspend(json.RawMessage) path.
	// Protocol methods (PayloadFrom, Resume, ResumeStream) check tag for
	// mismatch and return a clear error.
	tag string
	// resume is the closure that continues execution with human input.
	// (existing fields below — unchanged)
	resume func(ctx context.Context, data json.RawMessage) (AgentResult, error)
	resumeStream func(ctx context.Context, data json.RawMessage, ch chan<- StreamEvent) (AgentResult, error)
	mu sync.Mutex
	ttlTimer *time.Timer
	snapshotSize int64
	onRelease func(size int64)
}
```

- [ ] **Step 1.6: Propagate `tag` through `checkSuspendLoop`**

In `agent/suspend.go`, in `checkSuspendLoop` (around line 311), change the `ErrSuspended` literal to copy the tag:

```go
suspended := &ErrSuspended{
	Step:         cfg.name,
	Payload:      suspend.payload,
	tag:          suspend.tag, // ← new line: propagate from sentinel
	snapshotSize: snapSize,
	resume: func(ctx context.Context, data json.RawMessage) (AgentResult, error) {
		resumed := make([]ChatMessage, len(snapshot)+1)
		copy(resumed, snapshot)
		resumed[len(snapshot)] = UserMessage("Human input: " + string(data))
		resumeCfg := cfg
		resumeCfg.resumeMessages = resumed
		return runLoop(ctx, resumeCfg, task, nil)
	},
	resumeStream: func(ctx context.Context, data json.RawMessage, ch chan<- StreamEvent) (AgentResult, error) {
		resumed := make([]ChatMessage, len(snapshot)+1)
		copy(resumed, snapshot)
		resumed[len(snapshot)] = UserMessage("Human input: " + string(data))
		resumeCfg := cfg
		resumeCfg.resumeMessages = resumed
		return runLoop(ctx, resumeCfg, task, ch)
	},
}
```

- [ ] **Step 1.7: Run the new test — should pass now**

Run: `go test ./agent/ -run TestUntypedSuspendHasEmptyTag -v`
Expected: PASS.

- [ ] **Step 1.8: Run the full agent suspend test suite to confirm no regressions**

Run: `go test ./agent/ -run TestRunLoop -v`
Expected: every existing `TestRunLoop*` test still passes.

- [ ] **Step 1.9: Show the user the diff and pause for review**

Run: `git diff agent/suspend.go agent/suspend_protocol_test.go`
Tell the user: "Task 1 done — `tag` field plumbed end-to-end, untyped path unchanged. Please review the diff before I continue."

Do NOT commit. Wait for the user's go-ahead.

---

## Task 2: Add `format` field to `errSuspend` and use it in resume closures

**What this does at runtime:** Adds a per-suspension formatter that turns resume bytes into the user-visible message in the LLM history. Untyped path passes `nil`, which preserves today's `"Human input: " + string(data)` output exactly.

**Files:**
- Modify: `agent/suspend.go:32-36` (errSuspend struct again), `agent/suspend.go:311-345` (checkSuspendLoop resume closures)
- Test: `agent/suspend_protocol_test.go` (add test)

- [ ] **Step 2.1: Write a failing test that exercises a custom format**

Append to `agent/suspend_protocol_test.go`:

```go
// formattingProcessor is a PostProcessor that suspends with a custom format function.
type formattingProcessor struct {
	tag    string
	format func(json.RawMessage) string
}

func (p *formattingProcessor) PostLLM(_ context.Context, _ *ChatResponse) error {
	return &errSuspend{
		payload: json.RawMessage(`{"q":"approve?"}`),
		tag:     p.tag,
		format:  p.format,
	}
}

// TestSuspendFormatFnInjectsCustomMessage verifies that when errSuspend.format
// is set, the resume closure uses it instead of the default "Human input: <bytes>".
func TestSuspendFormatFnInjectsCustomMessage(t *testing.T) {
	var captured []ChatMessage
	provider := &mockProvider{
		responses: []ChatResponse{
			{Content: "first"},  // before suspend
			{Content: "second"}, // after resume — capture happens before this call
		},
		onChat: func(req *ChatRequest) { captured = append([]ChatMessage(nil), req.Messages...) },
	}

	chain := NewProcessorChain()
	chain.AddPost(&formattingProcessor{
		tag: "approve_v1",
		format: func(data json.RawMessage) string {
			return "CUSTOM(" + string(data) + ")"
		},
	})

	cfg := LoopConfig{
		name:       "test",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}

	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	// Resume and look at the last message that hit the provider.
	_, err = suspended.Resume(context.Background(), json.RawMessage(`{"ok":true}`))
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	if len(captured) == 0 {
		t.Fatalf("provider never re-invoked after resume")
	}
	last := captured[len(captured)-1]
	want := `CUSTOM({"ok":true})`
	if last.Content != want {
		t.Errorf("resume message = %q, want %q", last.Content, want)
	}
}
```

> If `mockProvider` doesn't have an `onChat` hook in the existing test helpers, add it. Check `agent/agent_test.go` for the type definition and add an `onChat func(*ChatRequest)` field that runs at the top of `Chat`.

- [ ] **Step 2.2: Run the test to confirm it fails (format field does not exist)**

Run: `go test ./agent/ -run TestSuspendFormatFnInjectsCustomMessage -v`
Expected: build failure with `unknown field format in struct literal`.

- [ ] **Step 2.3: Add `format` field to `errSuspend`**

In `agent/suspend.go`, change `errSuspend`:

```go
type errSuspend struct {
	payload json.RawMessage
	tag     string
	// format produces the user-visible message injected into the LLM history
	// from the resume bytes. When nil, the default formatter (defaultResumeFormat)
	// is used, preserving today's "Human input: <data>" output.
	format func(data json.RawMessage) string
}
```

- [ ] **Step 2.4: Add a default formatter and wire it into `checkSuspendLoop`**

Add this small helper to `agent/suspend.go` (near the top, just under the imports):

```go
// defaultResumeFormat is the formatter used by untyped Suspend (and as a
// fallback inside protocol suspends when their formatter is nil for any
// reason). It preserves the exact byte-for-byte output Oasis has emitted
// since v0.1 — callers reading transcripts can rely on it.
func defaultResumeFormat(data json.RawMessage) string {
	return "Human input: " + string(data)
}
```

In `checkSuspendLoop`, before the `ErrSuspended` literal, capture the formatter:

```go
formatFn := suspend.format
if formatFn == nil {
	formatFn = defaultResumeFormat
}
```

Then change BOTH resume closures (sync and stream) so they use the captured `formatFn`:

```go
resume: func(ctx context.Context, data json.RawMessage) (AgentResult, error) {
	resumed := make([]ChatMessage, len(snapshot)+1)
	copy(resumed, snapshot)
	resumed[len(snapshot)] = UserMessage(formatFn(data)) // ← changed
	resumeCfg := cfg
	resumeCfg.resumeMessages = resumed
	return runLoop(ctx, resumeCfg, task, nil)
},
resumeStream: func(ctx context.Context, data json.RawMessage, ch chan<- StreamEvent) (AgentResult, error) {
	resumed := make([]ChatMessage, len(snapshot)+1)
	copy(resumed, snapshot)
	resumed[len(snapshot)] = UserMessage(formatFn(data)) // ← changed
	resumeCfg := cfg
	resumeCfg.resumeMessages = resumed
	return runLoop(ctx, resumeCfg, task, ch)
},
```

- [ ] **Step 2.5: Run the custom-format test — should pass now**

Run: `go test ./agent/ -run TestSuspendFormatFnInjectsCustomMessage -v`
Expected: PASS.

- [ ] **Step 2.6: Run the empty-tag test from Task 1 — must still pass**

Run: `go test ./agent/ -run TestUntypedSuspendHasEmptyTag -v`
Expected: PASS.

- [ ] **Step 2.7: Run the entire suspend test suite — must still pass (default fallback works)**

Run: `go test ./agent/ -run "TestRunLoop|TestSuspend" -v`
Expected: every existing suspend test continues to pass — they all hit the `nil format → defaultResumeFormat → "Human input: ..."` fallback path.

- [ ] **Step 2.8: Show the user the diff and pause**

Run: `git diff agent/suspend.go agent/suspend_protocol_test.go`
Tell the user: "Task 2 done — formatter plumbed; untyped path unchanged. Diff ready for review."

Do NOT commit.

---

## Task 3: Create the `SuspendProtocol[Req, Resp]` type skeleton

**What this does at runtime:** Adds a new exported generic value type with constructor, `Name()`, and `WithRenderResume`. No engine integration yet — this is pure type/value plumbing.

**Files:**
- Create: `agent/suspend_protocol.go`
- Test: `agent/suspend_protocol_test.go` (append)

- [ ] **Step 3.1: Write the failing test for construction + Name() + WithRenderResume**

Append to `agent/suspend_protocol_test.go`:

```go
type apReq struct{ Amount float64 }
type apResp struct{ Approved bool }

func TestSuspendProtocolConstruction(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")
	if p.Name() != "approve_transfer" {
		t.Errorf("Name() = %q, want %q", p.Name(), "approve_transfer")
	}
}

func TestSuspendProtocolWithRenderResume(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer").
		WithRenderResume(func(r apResp) string {
			if r.Approved {
				return "approved"
			}
			return "denied"
		})

	// formatBytes is an unexported method — see Step 3.3.
	if got := p.formatBytes(json.RawMessage(`{"Approved":true}`)); got != "approved" {
		t.Errorf("formatBytes(approved) = %q, want %q", got, "approved")
	}
	if got := p.formatBytes(json.RawMessage(`{"Approved":false}`)); got != "denied" {
		t.Errorf("formatBytes(denied) = %q, want %q", got, "denied")
	}
}

func TestSuspendProtocolDefaultFormat(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")
	// No WithRenderResume → default formatter produces tagged JSON.
	got := p.formatBytes(json.RawMessage(`{"Approved":true}`))
	want := "Human resumed `approve_transfer`: {\"Approved\":true}"
	if got != want {
		t.Errorf("default formatBytes = %q, want %q", got, want)
	}
}
```

- [ ] **Step 3.2: Run the test — confirm build failure (type does not exist)**

Run: `go test ./agent/ -run TestSuspendProtocol -v`
Expected: build failure `NewSuspendProtocol undefined`.

- [ ] **Step 3.3: Create `agent/suspend_protocol.go` with the type, constructor, Name, WithRenderResume, formatBytes**

```go
// Package agent — typed suspend/resume contracts.
//
// SuspendProtocol[Req, Resp] is a typed handle that pins the payload type
// (Req) sent to the human and the response type (Resp) sent back, declared
// once and referenced from both the suspending site and the caller that
// resumes. The framework primitives ErrSuspended, Agent, Workflow, and
// Network stay monomorphic — the generic parameters live only on the
// protocol value and its methods, captured into closures at registration.
//
// See docs/superpowers/specs/2026-05-22-typed-hitl-contracts-design.md
// for the full design.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// SuspendProtocol is a typed HITL contract. Declare once with
// NewSuspendProtocol, then use its methods to suspend (from a workflow
// step or processor) and resume (from the caller that receives
// *ErrSuspended). The zero value is not usable.
type SuspendProtocol[Req, Resp any] struct {
	name         string
	renderResume func(Resp) string
}

// NewSuspendProtocol declares a typed HITL contract. The name is a stable
// identifier used in error messages and the runtime tag check that
// catches "wrong protocol used to resume" mistakes. Names should be
// unique within a process; the framework does not enforce uniqueness.
//
// By convention, namespace protocol names with a domain prefix to avoid
// collisions across packages, e.g. "billing.approve_transfer".
func NewSuspendProtocol[Req, Resp any](name string) SuspendProtocol[Req, Resp] {
	return SuspendProtocol[Req, Resp]{name: name}
}

// Name returns the protocol's stable identifier.
func (p SuspendProtocol[Req, Resp]) Name() string { return p.name }

// WithRenderResume sets a formatter that converts the typed resume data
// into the natural-language message injected into the LLM's conversation
// history. When not set, the default formatter produces
// "Human resumed `<name>`: <json>".
//
// Returns the protocol so calls can chain at declaration time:
//
//	var ApproveTransfer = oasis.NewSuspendProtocol[Req, Resp]("name").
//	    WithRenderResume(func(r Resp) string { ... })
func (p SuspendProtocol[Req, Resp]) WithRenderResume(fn func(Resp) string) SuspendProtocol[Req, Resp] {
	p.renderResume = fn
	return p
}

// formatBytes is the type-erased formatter used by the resume closure.
// It unmarshals into Resp and applies renderResume if set, otherwise
// returns the tagged-JSON default. On unmarshal failure it falls back
// to the tagged-JSON default so the LLM still gets readable context
// instead of a crash.
func (p SuspendProtocol[Req, Resp]) formatBytes(data json.RawMessage) string {
	if p.renderResume != nil {
		var resp Resp
		if err := json.Unmarshal(data, &resp); err == nil {
			return p.renderResume(resp)
		}
		// fall through to tagged-JSON default on unmarshal failure
	}
	return fmt.Sprintf("Human resumed `%s`: %s", p.name, string(data))
}
```

> The remaining methods (`Suspend`, `PayloadFrom`, `Resume`, `ResumeStream`) are added in Tasks 4 and 5. Keep this file open.

- [ ] **Step 3.4: Run the construction tests — should pass**

Run: `go test ./agent/ -run TestSuspendProtocol -v`
Expected: `TestSuspendProtocolConstruction`, `TestSuspendProtocolWithRenderResume`, `TestSuspendProtocolDefaultFormat` all PASS.

- [ ] **Step 3.5: Show diff and pause**

Run: `git diff agent/suspend_protocol.go agent/suspend_protocol_test.go`
Tell the user: "Task 3 done — protocol skeleton + formatter in place, no engine wiring yet."

Do NOT commit.

---

## Task 4: Add `SuspendProtocol.Suspend` method

**What this does at runtime:** Tool/step/processor calls `MyProtocol.Suspend(typedPayload)` → marshals to JSON → returns an `errSuspend` carrying the protocol's name as `tag` and `formatBytes` as `format`. The engine's existing `checkSuspendLoop` picks it up unchanged.

**Files:**
- Modify: `agent/suspend_protocol.go`
- Test: `agent/suspend_protocol_test.go` (append)

- [ ] **Step 4.1: Write the failing tests**

Append to `agent/suspend_protocol_test.go`:

```go
func TestSuspendProtocolSuspendReturnsTaggedSentinel(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	err := p.Suspend(apReq{Amount: 5000})
	if err == nil {
		t.Fatal("Suspend returned nil error")
	}

	var sus *errSuspend
	if !errors.As(err, &sus) {
		t.Fatalf("Suspend returned %T, want *errSuspend", err)
	}
	if sus.tag != "approve_transfer" {
		t.Errorf("tag = %q, want %q", sus.tag, "approve_transfer")
	}
	if sus.format == nil {
		t.Error("format func is nil; want non-nil from protocol")
	}

	// Payload bytes should be valid JSON of apReq.
	var got apReq
	if err := json.Unmarshal(sus.payload, &got); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if got.Amount != 5000 {
		t.Errorf("payload Amount = %v, want 5000", got.Amount)
	}
}

func TestSuspendProtocolSuspendMarshalFailure(t *testing.T) {
	// Use a Req that json.Marshal cannot encode (chan can't be marshaled).
	type bad struct{ Ch chan int }
	p := NewSuspendProtocol[bad, apResp]("bad")

	err := p.Suspend(bad{Ch: make(chan int)})
	if err == nil {
		t.Fatal("expected marshal error, got nil")
	}
	// Must NOT be an *errSuspend — marshal failed before construction.
	var sus *errSuspend
	if errors.As(err, &sus) {
		t.Error("got *errSuspend on marshal failure; expected a plain error")
	}
}
```

- [ ] **Step 4.2: Run — confirm build failure (Suspend method missing)**

Run: `go test ./agent/ -run TestSuspendProtocolSuspend -v`
Expected: build failure `p.Suspend undefined`.

- [ ] **Step 4.3: Add `Suspend` method to `agent/suspend_protocol.go`**

Append to `agent/suspend_protocol.go`:

```go
// Suspend returns an error that signals the engine to pause execution.
// The payload is JSON-marshaled with the protocol's tag and formatter
// attached, so any caller using the same protocol value can read the
// payload typed (via PayloadFrom) and resume typed (via Resume).
//
// Returns a non-suspend error if marshaling fails — propagate it as
// normal. Tools should not invoke Suspend directly; suspend from a
// workflow step or processor whose return type is error.
func (p SuspendProtocol[Req, Resp]) Suspend(payload Req) error {
	bytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("SuspendProtocol[%s].Suspend: marshal payload: %w", p.name, err)
	}
	return &errSuspend{
		payload: bytes,
		tag:     p.name,
		format:  p.formatBytes,
	}
}
```

- [ ] **Step 4.4: Run the Suspend tests — should pass**

Run: `go test ./agent/ -run TestSuspendProtocolSuspend -v`
Expected: both tests PASS.

- [ ] **Step 4.5: Show diff and pause**

Run: `git diff agent/suspend_protocol.go agent/suspend_protocol_test.go`
Tell the user: "Task 4 done — Suspend marshals + tags."

Do NOT commit.

---

## Task 5: Add `PayloadFrom`, `Resume`, and `ResumeStream` methods

**What this does at runtime:** The caller side of the protocol. `PayloadFrom` reads typed `Req` out of `*ErrSuspended` (with tag-mismatch check). `Resume` and `ResumeStream` marshal a typed `Resp`, do the same tag check, and delegate to the existing `(*ErrSuspended).Resume` / `ResumeStream` methods. The formatter set in Task 4 then turns the bytes into the LLM-visible message.

**Files:**
- Modify: `agent/suspend_protocol.go`
- Test: `agent/suspend_protocol_test.go` (append)

- [ ] **Step 5.1: Write the failing tests**

Append to `agent/suspend_protocol_test.go`:

```go
func TestPayloadFromReturnsTypedReq(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	// Construct an ErrSuspended directly via the engine path.
	provider := &mockProvider{responses: []ChatResponse{{Content: ""}}}
	chain := NewProcessorChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 7500}})

	cfg := LoopConfig{
		name:       "test",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	got, err := p.PayloadFrom(suspended)
	if err != nil {
		t.Fatalf("PayloadFrom error = %v", err)
	}
	if got.Amount != 7500 {
		t.Errorf("Amount = %v, want 7500", got.Amount)
	}
}

func TestPayloadFromNilErr(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")
	_, err := p.PayloadFrom(nil)
	if err == nil {
		t.Fatal("expected error on nil suspension")
	}
}

func TestPayloadFromTagMismatch(t *testing.T) {
	pA := NewSuspendProtocol[apReq, apResp]("protocol_A")
	pB := NewSuspendProtocol[apReq, apResp]("protocol_B")

	// Suspend via A, query via B.
	provider := &mockProvider{responses: []ChatResponse{{Content: ""}}}
	chain := NewProcessorChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: pA, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		name:       "test",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	_, err = pB.PayloadFrom(suspended)
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "protocol_A") || !strings.Contains(msg, "protocol_B") {
		t.Errorf("error %q does not contain both protocol names", msg)
	}
}

func TestResumeAppliesRenderResume(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer").
		WithRenderResume(func(r apResp) string {
			if r.Approved {
				return "Human approved the transfer."
			}
			return "Human declined the transfer."
		})

	var captured []ChatMessage
	provider := &mockProvider{
		responses: []ChatResponse{{Content: "first"}, {Content: "second"}},
		onChat:    func(req *ChatRequest) { captured = append([]ChatMessage(nil), req.Messages...) },
	}
	chain := NewProcessorChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 100}})

	cfg := LoopConfig{
		name:       "test",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	_, err = p.Resume(suspended, context.Background(), apResp{Approved: true})
	if err != nil {
		t.Fatalf("Resume error = %v", err)
	}
	if len(captured) == 0 {
		t.Fatal("provider never re-invoked after Resume")
	}
	last := captured[len(captured)-1]
	if last.Content != "Human approved the transfer." {
		t.Errorf("resume message = %q, want %q", last.Content, "Human approved the transfer.")
	}
}

func TestResumeTagMismatch(t *testing.T) {
	pA := NewSuspendProtocol[apReq, apResp]("protocol_A")
	pB := NewSuspendProtocol[apReq, apResp]("protocol_B")

	provider := &mockProvider{responses: []ChatResponse{{Content: ""}}}
	chain := NewProcessorChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: pA, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		name:       "test",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	_, err = pB.Resume(suspended, context.Background(), apResp{Approved: true})
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "protocol_A") || !strings.Contains(err.Error(), "protocol_B") {
		t.Errorf("error %q does not contain both protocol names", err.Error())
	}

	// The original suspended should still be resumable via the correct protocol.
	_, err = pA.Resume(suspended, context.Background(), apResp{Approved: true})
	if err != nil {
		t.Errorf("Resume via correct protocol failed: %v", err)
	}
}

func TestResumeStreamDeliversEvents(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	provider := &mockProvider{responses: []ChatResponse{{Content: ""}, {Content: "done"}}}
	chain := NewProcessorChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		name:       "test",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}
	ch := make(chan StreamEvent, 16)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)
	close(ch) // drain
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	resumeCh := make(chan StreamEvent, 16)
	_, err = p.ResumeStream(suspended, context.Background(), apResp{Approved: true}, resumeCh)
	if err != nil {
		t.Fatalf("ResumeStream error = %v", err)
	}
	// Channel must be closed by the engine after ResumeStream returns.
	select {
	case _, ok := <-resumeCh:
		if ok {
			// Drain until close.
			for range resumeCh {
			}
		}
	default:
		// Already closed.
	}
}

// typedSuspendingPreProcessor uses a typed protocol to suspend.
// Mirrors suspendingPreProcessor but with protocol typing.
type typedSuspendingPreProcessor[Req, Resp any] struct {
	protocol SuspendProtocol[Req, Resp]
	payload  Req
}

func (p *typedSuspendingPreProcessor[Req, Resp]) PreLLM(_ context.Context, _ *ChatRequest) error {
	return p.protocol.Suspend(p.payload)
}
```

> Add `"strings"` to the import list at the top of `agent/suspend_protocol_test.go` if not already there. If the existing `mockProvider` in `agent/agent_test.go` doesn't have an `onChat` field, add it:
>
> ```go
> type mockProvider struct {
>     // ... existing fields
>     onChat func(*ChatRequest)
> }
>
> func (m *mockProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
>     if m.onChat != nil { m.onChat(req) }
>     // ... existing body
> }
> ```

- [ ] **Step 5.2: Run — confirm build failure (PayloadFrom/Resume/ResumeStream missing)**

Run: `go test ./agent/ -run "TestPayloadFrom|TestResume" -v`
Expected: build failure naming the missing methods.

- [ ] **Step 5.3: Add the three methods to `agent/suspend_protocol.go`**

Append to `agent/suspend_protocol.go`:

```go
// PayloadFrom reads the suspended payload as the typed Req.
// Returns an error if e is nil, has a different protocol tag than this
// protocol, or the payload bytes don't unmarshal as Req.
func (p SuspendProtocol[Req, Resp]) PayloadFrom(e *ErrSuspended) (Req, error) {
	var zero Req
	if e == nil {
		return zero, errors.New("PayloadFrom: nil suspended err")
	}
	if e.tag != p.name {
		return zero, fmt.Errorf(
			"PayloadFrom: protocol mismatch: suspended with %q, queried with %q",
			tagDescriptor(e.tag), p.name,
		)
	}
	var out Req
	if err := json.Unmarshal(e.Payload, &out); err != nil {
		return zero, fmt.Errorf("PayloadFrom: unmarshal payload as %T: %w", out, err)
	}
	return out, nil
}

// Resume continues execution with the typed response data. The data is
// JSON-marshaled and handed to the engine's existing untyped resume path;
// the protocol's formatter (set via WithRenderResume, or the default
// tagged-JSON formatter) shapes the message the LLM sees.
//
// Returns an error if e is nil, has a different protocol tag, or any
// error the underlying (*ErrSuspended).Resume would return (released,
// expired, marshal failure on data, etc.).
func (p SuspendProtocol[Req, Resp]) Resume(e *ErrSuspended, ctx context.Context, data Resp) (AgentResult, error) {
	if e == nil {
		return AgentResult{}, errors.New("Resume: nil suspended err")
	}
	if e.tag != p.name {
		return AgentResult{}, fmt.Errorf(
			"Resume: protocol mismatch: suspended with %q, attempted via %q",
			tagDescriptor(e.tag), p.name,
		)
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return AgentResult{}, fmt.Errorf("Resume: marshal data: %w", err)
	}
	return e.Resume(ctx, bytes)
}

// ResumeStream is the streaming form of Resume. Same tag check, same
// JSON marshaling; events are emitted on ch by the engine throughout
// the post-resume loop. The engine closes ch when streaming completes.
func (p SuspendProtocol[Req, Resp]) ResumeStream(e *ErrSuspended, ctx context.Context, data Resp, ch chan<- StreamEvent) (AgentResult, error) {
	if e == nil {
		return AgentResult{}, errors.New("ResumeStream: nil suspended err")
	}
	if e.tag != p.name {
		return AgentResult{}, fmt.Errorf(
			"ResumeStream: protocol mismatch: suspended with %q, attempted via %q",
			tagDescriptor(e.tag), p.name,
		)
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return AgentResult{}, fmt.Errorf("ResumeStream: marshal data: %w", err)
	}
	return e.ResumeStream(ctx, bytes, ch)
}

// tagDescriptor returns a human-readable label for an ErrSuspended's
// internal tag, mapping the empty tag to the literal token "<untyped>"
// so mismatch errors are unambiguous when one side used the untyped path.
func tagDescriptor(tag string) string {
	if tag == "" {
		return "<untyped>"
	}
	return tag
}
```

- [ ] **Step 5.4: Run the caller-side tests — should pass**

Run: `go test ./agent/ -run "TestPayloadFrom|TestResume" -v`
Expected: every new test PASSes.

- [ ] **Step 5.5: Run the whole agent test suite — no regressions**

Run: `go test ./agent/...`
Expected: PASS.

- [ ] **Step 5.6: Show diff and pause**

Run: `git diff agent/suspend_protocol.go agent/suspend_protocol_test.go agent/agent_test.go`
Tell the user: "Task 5 done — typed PayloadFrom / Resume / ResumeStream complete. Tag mismatch produces a clear error."

Do NOT commit.

---

## Task 6: Re-export to `oasis.go`

**What this does at runtime:** Nothing. Just makes the new types visible from the umbrella package so users can write `oasis.NewSuspendProtocol[…]` instead of `agent.NewSuspendProtocol[…]`. Also fills a long-standing gap by re-exporting `Suspend` and `ErrSuspended` to match the godoc-promised `*oasis.ErrSuspended` usage.

**Files:**
- Modify: `oasis.go` (add 4 lines near the existing `WithSuspendBudget` re-export)

- [ ] **Step 6.1: Write a thin verification test in `oasis_test.go`**

Add (or append to) `oasis_test.go` at the repo root:

```go
package oasis_test

import (
	"testing"

	"github.com/nevindra/oasis"
)

func TestUmbrellaReExportsSuspendProtocol(t *testing.T) {
	// Compilation alone is the assertion — if any of these references
	// fail to resolve, the umbrella is missing the re-export.
	type req struct{ X int }
	type resp struct{ Y int }

	var p oasis.SuspendProtocol[req, resp] = oasis.NewSuspendProtocol[req, resp]("test")
	if p.Name() != "test" {
		t.Errorf("Name() = %q, want %q", p.Name(), "test")
	}
	_ = oasis.Suspend
	var _ *oasis.ErrSuspended
}
```

> If `oasis_test.go` doesn't exist yet, create it with the file content above. If it does, append the function.

- [ ] **Step 6.2: Run — confirm build failure on the missing re-exports**

Run: `go test ./ -run TestUmbrellaReExportsSuspendProtocol -v`
Expected: build failure naming `oasis.SuspendProtocol`, `oasis.NewSuspendProtocol`, `oasis.Suspend`, or `oasis.ErrSuspended` as undefined.

- [ ] **Step 6.3: Add the re-exports to `oasis.go`**

In `oasis.go`, just below the existing `var WithSuspendBudget = agent.WithSuspendBudget` line (around line 57), add:

```go
// Typed HITL contracts — see docs/superpowers/specs/2026-05-22-typed-hitl-contracts-design.md
type SuspendProtocol[Req, Resp any] = agent.SuspendProtocol[Req, Resp]
type ErrSuspended = agent.ErrSuspended
var NewSuspendProtocol = agent.NewSuspendProtocol[any, any] // placeholder — see Step 6.4
var Suspend = agent.Suspend
```

> **Step 6.4 fixes the generic function alias.** Go does not let you assign a generic function to a `var` without instantiating it. We'll work around this in the next step.

- [ ] **Step 6.4: Replace the generic-function placeholder with a thin wrapper**

Generic functions cannot be aliased directly via `var`. Replace the placeholder line with a proper generic wrapper:

```go
// NewSuspendProtocol declares a typed HITL contract. See agent.NewSuspendProtocol.
func NewSuspendProtocol[Req, Resp any](name string) SuspendProtocol[Req, Resp] {
	return agent.NewSuspendProtocol[Req, Resp](name)
}
```

> Other generic re-exports in the repo (e.g., `StreamObjectAs[T]`) use this same wrapper pattern. Follow the existing convention.

- [ ] **Step 6.5: Run the re-export test — should pass**

Run: `go test ./ -run TestUmbrellaReExportsSuspendProtocol -v`
Expected: PASS.

- [ ] **Step 6.6: Build the whole module to make sure nothing else broke**

Run: `go build ./...`
Expected: succeeds with no output.

- [ ] **Step 6.7: Show diff and pause**

Run: `git diff oasis.go oasis_test.go`
Tell the user: "Task 6 done — umbrella re-exports added (also filled the `Suspend` / `ErrSuspended` gap)."

Do NOT commit.

---

## Task 7: TTL and budget still work on typed Suspend

**What this does at runtime:** Confirms typed suspensions are governed by the same `WithSuspendTTL` and `WithSuspendBudget` controls as untyped — no special path inside the engine, just a different formatter and tag.

**Files:**
- Test: `agent/suspend_protocol_test.go` (append)

- [ ] **Step 7.1: Write the TTL test**

Append to `agent/suspend_protocol_test.go`:

```go
func TestTypedSuspendRespectsTTL(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	provider := &mockProvider{responses: []ChatResponse{{Content: ""}}}
	chain := NewProcessorChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		name:       "test",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	suspended.WithSuspendTTL(20 * time.Millisecond)
	time.Sleep(60 * time.Millisecond)

	_, err = p.Resume(suspended, context.Background(), apResp{Approved: true})
	if err == nil {
		t.Fatal("expected resume to fail after TTL, got nil")
	}
	if !strings.Contains(err.Error(), "closure is nil") {
		t.Errorf("error %q does not look like a released-after-TTL error", err.Error())
	}
}
```

> Add `"time"` to the import list at the top of `agent/suspend_protocol_test.go` if not already there.

- [ ] **Step 7.2: Run — expect PASS (no code change needed, TTL plumbing is upstream)**

Run: `go test ./agent/ -run TestTypedSuspendRespectsTTL -v`
Expected: PASS.

- [ ] **Step 7.3: Write the budget test**

Append:

```go
func TestTypedSuspendRespectsBudget(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	// Budget = 1 snapshot only.
	var count, bytesUsed int64
	var mu sync.Mutex

	provider := &mockProvider{responses: []ChatResponse{{Content: ""}, {Content: ""}, {Content: ""}}}
	chain := NewProcessorChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		name:                "test",
		provider:            provider,
		processors:          chain,
		maxIter:             5,
		mem:                 &memory.AgentMemory{},
		dispatch:            func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
		suspendCount:        &count,
		suspendBytes:        &bytesUsed,
		suspendMu:           &mu,
		maxSuspendSnapshots: 1,
	}

	// First suspend lands inside the budget.
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}
	if count != 1 {
		t.Errorf("count after first suspend = %d, want 1", count)
	}

	// Second suspend should be over budget — checkSuspendLoop returns nil and
	// the original processor error propagates. Behavior should match the
	// existing untyped budget test.
	_, err = runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	if err == nil {
		t.Fatal("expected over-budget error, got nil")
	}
	var second *ErrSuspended
	if errors.As(err, &second) {
		t.Errorf("expected non-ErrSuspended over-budget error, got ErrSuspended")
	}
}
```

> Add `"sync"` to the import list if it isn't already there.

- [ ] **Step 7.4: Run — expect PASS**

Run: `go test ./agent/ -run TestTypedSuspendRespectsBudget -v`
Expected: PASS.

- [ ] **Step 7.5: Show diff and pause**

Run: `git diff agent/suspend_protocol_test.go`
Tell the user: "Task 7 done — TTL and budget both verified for typed suspend."

Do NOT commit.

---

## Task 8: Untyped/typed interop tests

**What this does at runtime:** Exercises the edge cases between untyped and typed paths so the runtime tag check is exercised in both directions.

**Files:**
- Test: `agent/suspend_protocol_test.go` (append)

- [ ] **Step 8.1: Write the interop tests**

Append:

```go
// TestPayloadFromOnUntypedSuspend ensures the runtime tag check rejects a
// query via a protocol when the suspension came from the untyped path.
func TestPayloadFromOnUntypedSuspend(t *testing.T) {
	provider := &mockProvider{responses: []ChatResponse{{Content: ""}}}
	chain := NewProcessorChain()
	chain.AddPre(&suspendingPreProcessor{payload: json.RawMessage(`{"Amount": 1}`)})

	cfg := LoopConfig{
		name:       "test",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")
	_, err = p.PayloadFrom(suspended)
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	// Untyped tag is "<untyped>" in the error message.
	if !strings.Contains(err.Error(), "<untyped>") || !strings.Contains(err.Error(), "approve_transfer") {
		t.Errorf("error %q missing expected descriptors", err.Error())
	}
}

// TestUntypedResumeOnTypedSuspendStillFormats verifies that bypassing the
// protocol and calling (*ErrSuspended).Resume directly on a typed-protocol
// suspension still runs the protocol's formatter (the formatter is captured
// in the closure at suspend time, not at resume time).
func TestUntypedResumeOnTypedSuspendStillFormats(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer").
		WithRenderResume(func(r apResp) string { return "FORMATTED" })

	var captured []ChatMessage
	provider := &mockProvider{
		responses: []ChatResponse{{Content: ""}, {Content: ""}},
		onChat:    func(req *ChatRequest) { captured = append([]ChatMessage(nil), req.Messages...) },
	}
	chain := NewProcessorChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		name:       "test",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	// Call the untyped method directly — protocol formatter should still apply.
	_, err = suspended.Resume(context.Background(), json.RawMessage(`{"Approved":true}`))
	if err != nil {
		t.Fatalf("Resume error = %v", err)
	}
	if len(captured) == 0 {
		t.Fatal("provider never re-invoked after Resume")
	}
	last := captured[len(captured)-1]
	if last.Content != "FORMATTED" {
		t.Errorf("resume message = %q, want %q", last.Content, "FORMATTED")
	}
}

// TestResumeIsSingleUse mirrors the existing single-use guarantee for the
// untyped path: a second Resume call after a successful first returns the
// "closure is nil" error.
func TestResumeIsSingleUse(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	provider := &mockProvider{responses: []ChatResponse{{Content: ""}, {Content: "done"}}}
	chain := NewProcessorChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		name:       "test",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	_, err = p.Resume(suspended, context.Background(), apResp{Approved: true})
	if err != nil {
		t.Fatalf("first Resume failed: %v", err)
	}
	_, err = p.Resume(suspended, context.Background(), apResp{Approved: true})
	if err == nil {
		t.Fatal("second Resume should fail with closure-is-nil")
	}
	if !strings.Contains(err.Error(), "closure is nil") {
		t.Errorf("second Resume error = %q, expected 'closure is nil'", err.Error())
	}
}
```

- [ ] **Step 8.2: Run — should pass with no code change**

Run: `go test ./agent/ -run "TestPayloadFromOnUntypedSuspend|TestUntypedResumeOnTypedSuspendStillFormats|TestResumeIsSingleUse" -v`
Expected: PASS.

- [ ] **Step 8.3: Run the full `./...` once to confirm zero regressions across the module**

Run: `go test ./...`
Expected: PASS across root + all root tests. Satellites have their own `go.mod` and are not exercised by this PR.

- [ ] **Step 8.4: Run the linter**

Run: `golangci-lint run ./...`
Expected: no new findings. If new findings appear, fix them before continuing.

- [ ] **Step 8.5: Show diff and pause**

Run: `git diff agent/suspend_protocol_test.go`
Tell the user: "Task 8 done — interop edge cases covered, full module green, lint clean."

Do NOT commit.

---

## Task 9: Update `CHANGELOG.md`

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 9.1: Add a new bullet under `[Unreleased]` → `Added`**

Open `CHANGELOG.md` and locate the `## [Unreleased]` section. Under its `### Added` subsection (create it if missing), add:

```markdown
- **Typed HITL contracts.** New `agent.SuspendProtocol[Req, Resp]` value (re-exported as `oasis.SuspendProtocol`) with constructor `NewSuspendProtocol[Req, Resp](name)` and methods `Suspend(Req)`, `PayloadFrom(*ErrSuspended) (Req, error)`, `Resume(*ErrSuspended, ctx, Resp)`, `ResumeStream(*ErrSuspended, ctx, Resp, ch)`, `WithRenderResume(func(Resp) string)`, and `Name()`. Compile-time contract between the suspending site and the caller that resumes — wrong payload or response type fails the build. Untyped `Suspend(json.RawMessage)` and `(*ErrSuspended).Resume` remain as the escape hatch. Also re-exports `Suspend` and `ErrSuspended` on the umbrella package (long-standing gap fixed). Spec: [`docs/superpowers/specs/2026-05-22-typed-hitl-contracts-design.md`](docs/superpowers/specs/2026-05-22-typed-hitl-contracts-design.md).
```

- [ ] **Step 9.2: Show diff**

Run: `git diff CHANGELOG.md`
Tell the user: "Task 9 done — CHANGELOG entry added."

Do NOT commit.

---

## Task 10: Flip the two row outcomes in `docs/benchmarks/mastra-comparison.md`

**Files:**
- Modify: `docs/benchmarks/mastra-comparison.md` (HITL section table; HITL subtotal; Overall Scorecard total)

- [ ] **Step 10.1: Flip the "Suspend payload typing" row**

Open `docs/benchmarks/mastra-comparison.md`. Find the HITL table row:

```markdown
| **Suspend payload typing**           | Zod `suspendSchema` (optional)                                                                                                      | Untyped `json.RawMessage`                                                              | Mastra |
```

Replace the Oasis cell and Winner cell:

```markdown
| **Suspend payload typing**           | Zod `suspendSchema` (optional)                                                                                                      | Typed via `SuspendProtocol[Req, Resp].Suspend(payload)` — compile-time `Req` check; untyped `Suspend(json.RawMessage)` remains as escape hatch | Tie    |
```

- [ ] **Step 10.2: Flip the "Resume data typing" row**

Find:

```markdown
| **Resume data typing**               | Zod `resumeSchema` per step/tool; validated at `run.resume()` via `_validateResumeData` (`workflow.ts:3241`); serialized to JSON Schema in `tool-call-suspended` chunk | Untyped `json.RawMessage`; caller does own `json.Unmarshal`                            | Mastra |
```

Replace:

```markdown
| **Resume data typing**               | Zod `resumeSchema` per step/tool; validated at `run.resume()` via `_validateResumeData` (`workflow.ts:3241`); serialized to JSON Schema in `tool-call-suspended` chunk | Typed via `SuspendProtocol[Req, Resp].Resume(suspended, ctx, data)` — compile-time `Resp` check; `WithRenderResume(func(Resp) string)` shapes the LLM-visible message; untyped `(*ErrSuspended).Resume` remains as escape hatch | Tie |
```

- [ ] **Step 10.3: Update the HITL subtotal line**

Find the line `**Score: Mastra 15 — Oasis 5 — Tie 1**` immediately after the HITL table. Replace with:

```markdown
**Score: Mastra 13 — Oasis 5 — Tie 3**
```

> Rationale: two rows moved Mastra → Tie. Mastra column: 15 - 2 = 13. Tie column: 1 + 2 = 3. Oasis column unchanged.

- [ ] **Step 10.4: Add a note paragraph below the HITL subtotal explaining the flips**

Below the new subtotal, add a paragraph (insert after the existing block-quoted note, separated by a blank line):

```markdown
> **2026-05-22 (post typed HITL contracts):** the Suspend/Resume payload-typing rows flipped Mastra → Tie via [`docs/superpowers/specs/2026-05-22-typed-hitl-contracts-design.md`](../superpowers/specs/2026-05-22-typed-hitl-contracts-design.md): `SuspendProtocol[Req, Resp]` declared once and referenced by both the suspending site and the caller that resumes; compile-time `Req`/`Resp` enforcement on `Suspend`, `PayloadFrom`, `Resume`, `ResumeStream`; runtime string-tag check as a safety net; per-protocol `WithRenderResume` formatter for the LLM-visible resume message. `Agent`/`Workflow`/`Network` stay monomorphic — generics live only on the protocol value and its methods. Mastra still leads on durable cross-process snapshot persistence, multiple concurrent suspended paths surfaced per workflow run, the synchronous-to-async tool approval gate, Studio UI for suspended runs, and cross-suspend tracing context propagation. Those gaps are the targets of HITL specs #2–#6 on the same date.
```

- [ ] **Step 10.5: Update the Overall Scorecard**

Find the Overall Scorecard table near the bottom of the file. The HITL row currently reads:

```markdown
| Human-in-the-Loop             | 15      | 5     | 1   |
```

Replace with:

```markdown
| Human-in-the-Loop             | 13      | 5     | 3   |
```

Find the totals line:

```markdown
| **Total**                     | **93**  | **70**| **56** |
```

Compute the new totals: Mastra `93 - 2 = 91`, Oasis `70`, Tie `56 + 2 = 58`. Replace with:

```markdown
| **Total**                     | **91**  | **70**| **58** |
```

- [ ] **Step 10.6: Append a scorecard-history line**

Below the existing scorecard-history bullets (the `> Scorecard history:` block), add:

```markdown
> - **2026-05-22 (post typed HITL contracts)**: Mastra 91 / Oasis 70 / Tie 58 — HITL category dropped from Mastra 15/5/1 to Mastra 13/5/3 across two rows (Suspend payload typing, Resume data typing) via the typed `SuspendProtocol[Req, Resp]` shipped in spec #1 of the 6-spec HITL parity roadmap. The protocol value pins `Req`/`Resp` in one declaration; both the suspending site and the caller that resumes reference it; the compiler refuses to let either side disagree. Spec also adds a per-protocol `WithRenderResume` formatter so the LLM-visible resume message is natural language instead of raw JSON. `Agent`/`Workflow`/`Network` stay monomorphic. Untyped `Suspend(json.RawMessage)` and `(*ErrSuspended).Resume` are preserved as the escape hatch.
```

- [ ] **Step 10.7: Add the new line to "Unique to Oasis" near the bottom**

Find the `## Unique to Oasis` section. Append a new bullet:

```markdown
- **Typed HITL contracts** — `SuspendProtocol[Req, Resp]` value declared once and referenced by both suspend and resume sites; compile-time `Req`/`Resp` enforcement without making `Agent`/`Workflow`/`Network` generic; opt-in `WithRenderResume(func(Resp) string)` shapes the LLM-visible resume message; runtime string-tag check as a safety net for "wrong protocol" mistakes
```

- [ ] **Step 10.8: Show diff**

Run: `git diff docs/benchmarks/mastra-comparison.md`
Tell the user: "Task 10 done — comparison doc updated: two HITL rows flipped to Tie, subtotal recomputed, overall scorecard recomputed, history line + 'Unique to Oasis' bullet added."

Do NOT commit.

---

## Task 11: Document typed HITL in `docs/concepts/hitl.md`

**Files:**
- Modify or create: `docs/concepts/hitl.md`

- [ ] **Step 11.1: Check whether the file exists and read its structure**

Run: `ls docs/concepts/ | grep -i hitl`

If a file exists, read it to determine the right place to splice a typed section. If not, create one with the structure below.

- [ ] **Step 11.2: If creating, write the file**

Create `docs/concepts/hitl.md` with:

```markdown
# Human-in-the-Loop (HITL)

Oasis HITL has three primitives:

1. **`ask_user`** — the LLM autonomously calls a built-in tool to ask a clarifying question. Configured with `WithInputHandler(h)`. Free-form text in / text out. Best for mid-loop clarifications ("which user did you mean?").
2. **Suspend / resume** — a workflow step or processor pauses execution; the caller of `Execute()` receives `*ErrSuspended`, presents context to a human, and calls `Resume(ctx, data)` to continue. Best for structured human decisions (approvals, form fills, gated actions).
3. **`WithToolApproval(name, opts...)`** — a synchronous gate that requires the configured `InputHandler` to approve or deny a tool call before it runs. Best when the InputHandler can answer quickly (HTTP request/response, CLI prompt).

For typed structured suspend/resume, prefer `SuspendProtocol[Req, Resp]` (see below).

## Typed contracts with `SuspendProtocol`

Declare the contract once:

```go
type TransferRequest struct {
    Amount float64
    To     string
}

type ApproveResponse struct {
    Approved bool
    Reason   string
}

var ApproveTransfer = oasis.NewSuspendProtocol[TransferRequest, ApproveResponse]("billing.approve_transfer").
    WithRenderResume(func(r ApproveResponse) string {
        if r.Approved {
            return "Human approved the transfer. Reason: " + r.Reason
        }
        return "Human declined the transfer. Reason: " + r.Reason
    })
```

Suspend from a workflow step or processor:

```go
func (s *ApprovalStep) Execute(ctx context.Context, wCtx *oasis.WorkflowContext) (any, error) {
    amount, _ := wCtx.Get("amount")
    if amount.(float64) > 1000 {
        return nil, ApproveTransfer.Suspend(TransferRequest{
            Amount: amount.(float64),
            To:     "external_account",
        })
    }
    // ... normal path ...
    return nil, nil
}
```

Resume from the caller:

```go
result, err := ag.Execute(ctx, task)
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    payload, err := ApproveTransfer.PayloadFrom(suspended)
    if err != nil { /* mismatch or unmarshal */ }

    // ... present payload to the human via your UI of choice ...

    result, err = ApproveTransfer.Resume(suspended, ctx, ApproveResponse{
        Approved: true,
        Reason:   "manager OK'd",
    })
}
```

What the compiler enforces:
- The argument to `ApproveTransfer.Suspend(...)` must be `TransferRequest`.
- The return value of `ApproveTransfer.PayloadFrom(...)` is `TransferRequest`.
- The third argument of `ApproveTransfer.Resume(...)` must be `ApproveResponse`.

If someone tries to resume `ApproveTransfer` using a different protocol value (say `RefundTransfer`), the framework catches it at runtime with a clear error before any state changes.

## When to use untyped `Suspend(json.RawMessage)`

Use it for prototypes, scripts, or dynamic-shape payloads where declaring a protocol would be ceremony. The escape hatch is fully supported; nothing in spec #1 or any future spec deprecates it.

## What `SuspendProtocol` does not cover (yet)

- The synchronous `WithToolApproval` gate is not protocol-aware; a redesigned async approval gate using protocols ships in spec #6.
- `ask_user` deliberately stays free-form; it serves a different purpose.
- Durable cross-process suspend/resume snapshots are not part of typed contracts; the persistence story is its own spec when the deployment scenario demands it.
```

If the file already exists, splice the "Typed contracts with `SuspendProtocol`" section in the appropriate place (after the existing intro / suspend section) and add the "When to use untyped" + "What `SuspendProtocol` does not cover" sections.

- [ ] **Step 11.3: Verify the example compiles by building a temp file**

The doc example uses `oasis.NewSuspendProtocol`, which requires Task 6 to be done. Verify by writing a temp test in `docs/concepts/hitl_example_test.go`:

```go
//go:build hitl_example
// +build hitl_example

package concepts_test

import (
	"github.com/nevindra/oasis"
)

type TransferRequest struct {
	Amount float64
	To     string
}

type ApproveResponse struct {
	Approved bool
	Reason   string
}

var _ = oasis.NewSuspendProtocol[TransferRequest, ApproveResponse]("billing.approve_transfer").
	WithRenderResume(func(r ApproveResponse) string { return "ok" })
```

Run: `go build -tags hitl_example ./docs/concepts/...`
Expected: succeeds with no output.

Then delete the temp file:

Run: `rm docs/concepts/hitl_example_test.go`

> The build-tag-gated test is throwaway scaffolding to verify the doc example compiles before the user reads it. Keeping it around as a permanent artifact would add maintenance burden for marginal value.

- [ ] **Step 11.4: Show diff**

Run: `git diff docs/concepts/hitl.md`
Tell the user: "Task 11 done — concepts doc updated with typed example and untyped escape-hatch guidance."

Do NOT commit.

---

## Task 12: Final review and batch commit

**Files:**
- All of the above

- [ ] **Step 12.1: Run the full test matrix one more time**

Run:
```
go build ./...
go test ./...
golangci-lint run ./...
```
Expected: all PASS, no findings.

- [ ] **Step 12.2: Show the user the complete diff for sign-off**

Run: `git status`
Run: `git diff --stat`
Run: `git diff`

Tell the user:
> All 11 implementation tasks are done. The diff covers:
> - `agent/suspend.go` (tag + format plumbing through `errSuspend` / `ErrSuspended` / `checkSuspendLoop`)
> - `agent/suspend_protocol.go` (new file: `SuspendProtocol[Req, Resp]` with `Suspend`, `PayloadFrom`, `Resume`, `ResumeStream`, `WithRenderResume`, `Name`, plus the `formatBytes` and `tagDescriptor` helpers)
> - `agent/suspend_protocol_test.go` (new file: ~12 unit + integration tests)
> - `agent/agent_test.go` (added `onChat` hook on `mockProvider` if it wasn't already there)
> - `oasis.go` (re-exports: `SuspendProtocol`, `NewSuspendProtocol`, `Suspend`, `ErrSuspended`)
> - `oasis_test.go` (one re-export sanity test)
> - `CHANGELOG.md` (`[Unreleased]` → `Added` entry)
> - `docs/benchmarks/mastra-comparison.md` (two HITL rows flipped, subtotal + scorecard + history + Unique-to-Oasis updated)
> - `docs/concepts/hitl.md` (typed example, escape-hatch guidance)
>
> All tests green. Lint clean. Untyped path untouched.
>
> How would you like to commit? Single bundled commit, two commits (impl + docs), or a different split?

Then **wait for the user to instruct** how to commit. Do NOT run `git commit` autonomously.

- [ ] **Step 12.3: Run the user's chosen commit shape**

Once the user specifies the commit split, run the exact `git add` + `git commit` they approve. Use a heredoc message style consistent with the repo (look at recent commits with `git log --oneline -5` if unsure of conventions).

---

## Self-Review Notes (filled during planning)

**Spec coverage:**
- §4.1 protocol value (constructor, Name, WithRenderResume) → Task 3
- §4.2 Suspend method → Task 4
- §4.3 PayloadFrom, Resume, ResumeStream methods → Task 5
- §4.4 internal tag on errSuspend / ErrSuspended → Task 1
- §4.5 render formatter (default + override) → Tasks 2, 3, 5
- §4.6 back-compat preservation → covered by every existing test still passing (Tasks 1, 2, 7, 8)
- §5 new exported surface → Task 6
- §7 implementation outline → Tasks 1–11
- §8 testing strategy (all 11 test cases) → Tasks 5, 7, 8
- §9 risk register → acknowledged; no specific tasks needed; mitigations live in godoc strings written in Tasks 3, 4, 5
- §11 acceptance criteria → verified in Task 12

**Type consistency check:**
- `SuspendProtocol[Req, Resp]` shape used consistently across Tasks 3–6.
- `errSuspend.tag string` named identically in Tasks 1, 4.
- `errSuspend.format func(json.RawMessage) string` named identically in Tasks 2, 4.
- `tagDescriptor` (Step 5.3) is referenced only inside Task 5 — single-file scope.
- `defaultResumeFormat` (Step 2.4) is referenced only inside Task 2 — single-file scope.
- `typedSuspendingPreProcessor[Req, Resp]` test helper is defined once (end of Task 5's test block) and reused in Tasks 5, 7, 8.

**Placeholder scan:** clear (no TBD/TODO/"similar to" in steps; every code block is concrete).
