# Streaming World-Class Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the four remaining Streaming gaps from `docs/benchmarks/mastra-comparison.md` (lifecycle envelope, structured object streaming, result accessor parity, per-stream observability) by following the spec at `docs/superpowers/specs/2026-05-21-streaming-world-class-design.md`.

**Architecture:** Additive layering on the existing channel kernel. The kernel emits new event types (`run-start`/`run-finish`/`iteration-start`/`iteration-finish`/`object-delta`/`object-finish`/`element-delta`). `AgentResult` and `Stream` gain matching accessors. A new forgiving partial-JSON parser in `core/partial_json.go` powers object snapshots. Two new spans (`agent.iteration`, `llm.generate`) wrap the existing iteration. Generic free-function adapters (`StreamObjectAs[T]`, `ResultObjectAs[T]`) give typed access without contaminating `Agent` / `Network` / `Workflow` interfaces.

**Tech Stack:** Go 1.24 (root module), stdlib only for `core/`, existing `core.Tracer` for spans, existing `Stream` fan-out wrapper for typed adapters.

---

## Commit policy (project rule)

This project enforces `feedback_no_auto_commits_strict`: **never run `git commit` without explicit user approval**. Each phase ends with a "Request commit approval" step that prints what would be committed. Wait for the user to say "commit it" (or equivalent) before running `git commit`. This is non-negotiable and overrides skill instructions.

---

## Phase 0 — Read context

- [ ] **Step 0.1: Read the spec end-to-end**

Run: `Read /home/nezhifi/Code/LLM/oasis/docs/superpowers/specs/2026-05-21-streaming-world-class-design.md`
Expected: Full spec ~12 KB. Understand sections §4.1 lifecycle envelope, §4.2 object streaming, §4.3 result accessors, §4.4 observability, §4.5 provider plumbing, §5 affected packages.

- [ ] **Step 0.2: Read PHILOSOPHY.md and ENGINEERING.md**

Run: `Read /home/nezhifi/Code/LLM/oasis/docs/PHILOSOPHY.md` and `Read /home/nezhifi/Code/LLM/oasis/docs/ENGINEERING.md`
Expected: Four constraints (Fast, DX, Future-Ready, Safe), engineering rules. Especially: "No `any` at the boundary" (we use `json.RawMessage`), "Leaf-package invariant" (`core/` stdlib only), "Zero-overhead observability" (nil-check tracer).

- [ ] **Step 0.3: Skim current streaming code**

Run: `Read /home/nezhifi/Code/LLM/oasis/core/stream.go` and `Read /home/nezhifi/Code/LLM/oasis/core/types.go` (lines 364-377 for `ChatResponse`) and `Read /home/nezhifi/Code/LLM/oasis/core/agent.go` (lines 71-112 for `AgentResult` and `StepTrace`).
Expected: Understand the existing 16 event types, current `StreamEvent` struct shape, current `ChatResponse` shape, current `AgentResult` and `StepTrace` shapes.

---

## Phase 1 — Foundation types (no behavior change)

**Goal of phase:** Add all new types and constants. The codebase still compiles and behaves exactly as today. No event emission, no accessor population, no span creation yet. Two reasons to do this first: (1) confidence that the types compose without breakage before we touch the loop; (2) gives us a clean rollback point.

### Task 1.1: Add `FinishReason` type and constants

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/core/stream.go` (append at end of file)
- Test: `/home/nezhifi/Code/LLM/oasis/core/stream_test.go` (create if missing; add test function)

- [ ] **Step 1.1.1: Write the failing test**

Create or append to `/home/nezhifi/Code/LLM/oasis/core/stream_test.go`:

```go
package core

import "testing"

func TestFinishReasonValues(t *testing.T) {
	cases := []struct {
		got  FinishReason
		want string
	}{
		{FinishStop, "stop"},
		{FinishToolCalls, "tool-calls"},
		{FinishLength, "length"},
		{FinishContentFilter, "content-filter"},
		{FinishHalted, "halted"},
		{FinishSuspended, "suspended"},
		{FinishMaxIter, "max-iterations"},
		{FinishError, "error"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("FinishReason %q != %q", c.got, c.want)
		}
	}
}
```

- [ ] **Step 1.1.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestFinishReasonValues -v`
Expected: FAIL with `undefined: FinishReason` (or similar).

- [ ] **Step 1.1.3: Add the type and constants**

Append to `/home/nezhifi/Code/LLM/oasis/core/stream.go`:

```go
// FinishReason describes why an agent run ended. It is carried on
// EventRunFinish and on AgentResult.FinishReason.
type FinishReason string

const (
	// FinishStop — model produced a natural stop (no further tool calls).
	FinishStop FinishReason = "stop"
	// FinishToolCalls — model stopped to request tool calls. Intermediate
	// state on per-iteration finish; not emitted on EventRunFinish.
	FinishToolCalls FinishReason = "tool-calls"
	// FinishLength — model hit max_tokens before completing.
	FinishLength FinishReason = "length"
	// FinishContentFilter — provider safety / content filter blocked output.
	FinishContentFilter FinishReason = "content-filter"
	// FinishHalted — a processor returned *ErrHalt. Content carries the
	// canned response; Name carries the processor name on EventRunFinish.
	FinishHalted FinishReason = "halted"
	// FinishSuspended — the run paused awaiting human input. SuspendPayload
	// on AgentResult carries the payload (if any).
	FinishSuspended FinishReason = "suspended"
	// FinishMaxIter — the run hit the MaxIter cap before completing.
	FinishMaxIter FinishReason = "max-iterations"
	// FinishError — the run terminated with an error.
	FinishError FinishReason = "error"
)
```

- [ ] **Step 1.1.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestFinishReasonValues -v`
Expected: PASS.

- [ ] **Step 1.1.5: Run full core tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -v`
Expected: PASS (existing tests unaffected).

### Task 1.2: Add new lifecycle and object-stream event constants

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/core/stream.go`
- Test: `/home/nezhifi/Code/LLM/oasis/core/stream_test.go`

- [ ] **Step 1.2.1: Write the failing test**

Append to `/home/nezhifi/Code/LLM/oasis/core/stream_test.go`:

```go
func TestNewEventTypeValues(t *testing.T) {
	cases := []struct {
		got  StreamEventType
		want string
	}{
		{EventRunStart, "run-start"},
		{EventRunFinish, "run-finish"},
		{EventIterationStart, "iteration-start"},
		{EventIterationFinish, "iteration-finish"},
		{EventObjectDelta, "object-delta"},
		{EventObjectFinish, "object-finish"},
		{EventElementDelta, "element-delta"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("event type %q != %q", c.got, c.want)
		}
	}
}
```

- [ ] **Step 1.2.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestNewEventTypeValues -v`
Expected: FAIL with `undefined: EventRunStart` etc.

- [ ] **Step 1.2.3: Add the constants**

Inside the existing `const ( ... )` block in `/home/nezhifi/Code/LLM/oasis/core/stream.go`, immediately after `EventToolApprovalPending`, add:

```go
// EventRunStart is the first event on every stream. Name carries the
// agent name; Content carries the task input. Replaces the deprecated
// EventInputReceived + EventProcessingStart pair.
EventRunStart StreamEventType = "run-start"
// EventRunFinish is the last event on every stream before channel close.
// FinishReason indicates why the run ended; Warnings, ProviderMeta carry
// additional context. Content carries the final output for FinishHalted
// (canned response) and FinishSuspended (suspend payload as text).
EventRunFinish StreamEventType = "run-finish"
// EventIterationStart marks the beginning of one LLM-call iteration.
// Name carries the iteration index ("0", "1", ...).
EventIterationStart StreamEventType = "iteration-start"
// EventIterationFinish marks the end of one LLM-call iteration. Usage
// carries that iteration's token usage; Duration carries wall-clock time.
EventIterationFinish StreamEventType = "iteration-finish"
// EventObjectDelta carries a partial JSON snapshot of the structured
// output produced under WithResponseSchema. Object carries the snapshot
// bytes. Emitted only when ResponseSchema is set on the request.
EventObjectDelta StreamEventType = "object-delta"
// EventObjectFinish carries the final validated structured output.
// Object carries the final JSON bytes. Always preceded by zero or more
// EventObjectDelta events.
EventObjectFinish StreamEventType = "object-finish"
// EventElementDelta is emitted once per completed array element when the
// top-level schema is a JSON array (e.g. []Item). Content / Object carry
// the just-completed element. Not emitted for nested arrays.
EventElementDelta StreamEventType = "element-delta"
```

- [ ] **Step 1.2.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestNewEventTypeValues -v`
Expected: PASS.

### Task 1.3: Add new optional fields to `StreamEvent`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/core/stream.go` (the `StreamEvent` struct, lines ~96-117)
- Test: `/home/nezhifi/Code/LLM/oasis/core/stream_test.go`

- [ ] **Step 1.3.1: Write the failing test**

Append to `/home/nezhifi/Code/LLM/oasis/core/stream_test.go`:

```go
func TestStreamEventNewFieldsRoundTrip(t *testing.T) {
	ev := StreamEvent{
		Type:         EventRunFinish,
		FinishReason: FinishStop,
		Warnings:     []string{"rate-limited"},
		ProviderMeta: []byte(`{"stop_sequence":"END"}`),
		Object:       []byte(`{"title":"x"}`),
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back StreamEvent
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.FinishReason != FinishStop {
		t.Errorf("FinishReason lost: %q", back.FinishReason)
	}
	if len(back.Warnings) != 1 || back.Warnings[0] != "rate-limited" {
		t.Errorf("Warnings lost: %#v", back.Warnings)
	}
	if string(back.ProviderMeta) != `{"stop_sequence":"END"}` {
		t.Errorf("ProviderMeta lost: %s", back.ProviderMeta)
	}
	if string(back.Object) != `{"title":"x"}` {
		t.Errorf("Object lost: %s", back.Object)
	}
}
```

Also add at the top of the file if missing:

```go
import "encoding/json"
```

(Go will already need it for `json.RawMessage`; only add if the import is absent.)

- [ ] **Step 1.3.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestStreamEventNewFieldsRoundTrip -v`
Expected: FAIL — `unknown field FinishReason` in struct literal.

- [ ] **Step 1.3.3: Add the new fields**

In `/home/nezhifi/Code/LLM/oasis/core/stream.go`, modify the `StreamEvent` struct (the existing block at lines ~96-117). Add four new fields **at the end** of the struct, after the existing `Duration` field, **before the closing brace**:

```go
	// FinishReason is set on EventRunFinish events only. Empty on other types.
	FinishReason FinishReason `json:"finish_reason,omitempty"`
	// Warnings is set on EventRunFinish events when the run accumulated
	// non-fatal provider warnings (e.g. fallback model used, rate-limit
	// throttling, deprecated parameter ignored). Empty on other events.
	Warnings []string `json:"warnings,omitempty"`
	// ProviderMeta carries provider-specific opaque metadata on
	// EventRunFinish (e.g. Gemini safety ratings, Anthropic stop reason).
	// Consumers may decode it according to the provider's documentation.
	ProviderMeta json.RawMessage `json:"provider_meta,omitempty"`
	// Object carries the partial JSON snapshot on EventObjectDelta and
	// the final validated bytes on EventObjectFinish / EventElementDelta.
	// Empty on all other event types.
	Object json.RawMessage `json:"object,omitempty"`
```

- [ ] **Step 1.3.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestStreamEventNewFieldsRoundTrip -v`
Expected: PASS.

### Task 1.4: Add new fields to `ChatResponse`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/core/types.go` (lines 364-370 — the `ChatResponse` struct)
- Test: `/home/nezhifi/Code/LLM/oasis/core/types_test.go` (append; create if missing)

- [ ] **Step 1.4.1: Write the failing test**

Create or append to `/home/nezhifi/Code/LLM/oasis/core/types_test.go`:

```go
package core

import (
	"encoding/json"
	"testing"
)

func TestChatResponseNewFieldsRoundTrip(t *testing.T) {
	resp := ChatResponse{
		Content:      "hi",
		FinishReason: FinishStop,
		Warnings:     []string{"fallback-model"},
		ProviderMeta: []byte(`{"x":1}`),
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ChatResponse
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.FinishReason != FinishStop {
		t.Errorf("FinishReason lost: %q", back.FinishReason)
	}
	if len(back.Warnings) != 1 {
		t.Errorf("Warnings lost")
	}
	if string(back.ProviderMeta) != `{"x":1}` {
		t.Errorf("ProviderMeta lost")
	}
}
```

- [ ] **Step 1.4.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestChatResponseNewFieldsRoundTrip -v`
Expected: FAIL — `unknown field FinishReason`.

- [ ] **Step 1.4.3: Add the new fields**

In `/home/nezhifi/Code/LLM/oasis/core/types.go`, modify the `ChatResponse` struct (currently lines 364-370). Replace with:

```go
type ChatResponse struct {
	Content     string       `json:"content"`
	Thinking    string       `json:"thinking,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	Usage       Usage        `json:"usage"`
	// FinishReason is the provider-reported reason for stopping. Providers
	// that don't report a finish reason leave this empty; the agent loop
	// then synthesizes one (FinishToolCalls if ToolCalls is non-empty,
	// otherwise FinishStop) when populating EventRunFinish and AgentResult.
	FinishReason FinishReason `json:"finish_reason,omitempty"`
	// Warnings are non-fatal provider notes (e.g. fallback used, parameter
	// ignored). Decorator providers (WithRetry, WithRateLimit) may append.
	Warnings []string `json:"warnings,omitempty"`
	// ProviderMeta carries provider-specific opaque metadata. Documented
	// per provider package; consumers decode according to provider docs.
	ProviderMeta json.RawMessage `json:"provider_meta,omitempty"`
}
```

- [ ] **Step 1.4.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestChatResponseNewFieldsRoundTrip -v`
Expected: PASS.

### Task 1.5: Add `Source` type and `Sourced` / `Warner` interfaces

**Files:**
- Create: `/home/nezhifi/Code/LLM/oasis/core/source.go`
- Test: `/home/nezhifi/Code/LLM/oasis/core/source_test.go`

- [ ] **Step 1.5.1: Write the failing test**

Create `/home/nezhifi/Code/LLM/oasis/core/source_test.go`:

```go
package core

import (
	"encoding/json"
	"testing"
)

func TestSourceRoundTrip(t *testing.T) {
	src := Source{
		URL:    "https://example.com/doc",
		Title:  "Example",
		Quote:  "the relevant passage",
		Origin: "rag",
		Meta:   []byte(`{"score":0.87}`),
	}
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Source
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.URL != src.URL || back.Origin != src.Origin {
		t.Errorf("Source lost: %+v", back)
	}
}

// fakeSourced is a minimal implementation used to verify the interface is satisfiable.
type fakeSourced struct{ srcs []Source }

func (f fakeSourced) Sources() []Source { return f.srcs }

func TestSourcedInterface(t *testing.T) {
	var s Sourced = fakeSourced{srcs: []Source{{URL: "x"}}}
	if len(s.Sources()) != 1 {
		t.Errorf("Sourced interface broken")
	}
}

type fakeWarner struct{ ws []string }

func (f fakeWarner) Warnings() []string { return f.ws }

func TestWarnerInterface(t *testing.T) {
	var w Warner = fakeWarner{ws: []string{"hi"}}
	if len(w.Warnings()) != 1 {
		t.Errorf("Warner interface broken")
	}
}
```

- [ ] **Step 1.5.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run "TestSource|TestSourcedInterface|TestWarnerInterface" -v`
Expected: FAIL — `undefined: Source`.

- [ ] **Step 1.5.3: Create the source.go file**

Create `/home/nezhifi/Code/LLM/oasis/core/source.go`:

```go
package core

import "encoding/json"

// Source is a citation declared by a tool, retriever, or model. Sources
// aggregate onto AgentResult.Sources during a run and let consumers
// render "this answer was based on these documents" UX without inspecting
// individual tool results.
type Source struct {
	// URL is the canonical pointer to the source, when one exists.
	URL string `json:"url,omitempty"`
	// Title is a human-readable label (e.g. document title, page title).
	Title string `json:"title,omitempty"`
	// Quote is the specific passage the answer drew from. Optional.
	Quote string `json:"quote,omitempty"`
	// Origin marks where the source came from. Common values: "rag",
	// "tool:<name>", "model". Free-form; UIs may filter or group on it.
	Origin string `json:"origin,omitempty"`
	// Meta carries opaque metadata (relevance score, chunk ID, etc.).
	Meta json.RawMessage `json:"meta,omitempty"`
}

// Sourced is the opt-in capability for tools, retrievers, and providers
// that produce citations. The agent loop checks for this interface on
// every tool result and every provider response; implementations that
// don't satisfy it contribute nothing to AgentResult.Sources.
type Sourced interface {
	Sources() []Source
}

// Warner is the opt-in capability for providers and provider decorators
// that emit non-fatal warnings. Decorators like WithRetry or WithRateLimit
// implement this to surface "fallback model used" or "throttling applied"
// messages without writing to stderr.
type Warner interface {
	Warnings() []string
}
```

- [ ] **Step 1.5.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run "TestSource|TestSourcedInterface|TestWarnerInterface" -v`
Expected: PASS.

### Task 1.6: Add `IterationTrace` and `LLMCallTrace`; alias `StepTrace` → `ToolCallTrace`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/core/agent.go` (the `StepTrace` block at lines ~96-112)
- Test: `/home/nezhifi/Code/LLM/oasis/core/agent_trace_test.go` (create)

- [ ] **Step 1.6.1: Write the failing test**

Create `/home/nezhifi/Code/LLM/oasis/core/agent_trace_test.go`:

```go
package core

import (
	"testing"
	"time"
)

func TestIterationTraceShape(t *testing.T) {
	it := IterationTrace{
		Iter:      0,
		Model:     "gpt-4o",
		StartedAt: time.Now(),
		Duration:  100 * time.Millisecond,
		LLMCall: LLMCallTrace{
			Duration:     90 * time.Millisecond,
			InputTokens:  100,
			OutputTokens: 50,
			FinishReason: FinishStop,
		},
		ToolCalls: nil,
		Usage:     Usage{InputTokens: 100, OutputTokens: 50},
	}
	if it.LLMCall.FinishReason != FinishStop {
		t.Errorf("LLMCall.FinishReason not preserved")
	}
}

// Verify ToolCallTrace alias is interchangeable with StepTrace.
func TestToolCallTraceAlias(t *testing.T) {
	var tct ToolCallTrace = StepTrace{Name: "x"}
	if tct.Name != "x" {
		t.Errorf("alias broken")
	}
	var st StepTrace = ToolCallTrace{Name: "y"}
	if st.Name != "y" {
		t.Errorf("alias broken")
	}
}
```

- [ ] **Step 1.6.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run "TestIterationTraceShape|TestToolCallTraceAlias" -v`
Expected: FAIL — `undefined: IterationTrace`.

- [ ] **Step 1.6.3: Add the new types and the alias**

In `/home/nezhifi/Code/LLM/oasis/core/agent.go`, immediately **after** the existing `StepTrace` struct (after line ~112, before the `Text()` method at line ~117), add:

```go
// ToolCallTrace is a per-tool-call execution record. It is an alias for
// StepTrace, introduced for naming consistency with IterationTrace and
// LLMCallTrace. New code should use ToolCallTrace; StepTrace is kept as
// a name alias for back-compat for one minor release and will be removed
// in the next major.
type ToolCallTrace = StepTrace

// IterationTrace records one iteration of the agent's tool-calling loop.
// One LLM call plus zero or more tool dispatches. Collected automatically
// during runs and exposed on AgentResult.Iterations.
type IterationTrace struct {
	// Iter is the 0-indexed iteration number.
	Iter int `json:"iter"`
	// Model is the provider model used for this iteration (e.g. "gpt-4o").
	Model string `json:"model,omitempty"`
	// StartedAt is the wall-clock time the iteration began.
	StartedAt time.Time `json:"started_at"`
	// Duration is the wall-clock time for the entire iteration (LLM call
	// + tool dispatches).
	Duration time.Duration `json:"duration"`
	// LLMCall records the model call timing and usage for this iteration.
	LLMCall LLMCallTrace `json:"llm_call"`
	// ToolCalls records the tool calls that fired in this iteration.
	// In execution order. Empty if the iteration was text-only.
	ToolCalls []ToolCallTrace `json:"tool_calls,omitempty"`
	// Usage is the per-iteration token usage (excluding tool-side usage).
	Usage Usage `json:"usage"`
}

// LLMCallTrace records a single LLM model call. Nested inside
// IterationTrace.
type LLMCallTrace struct {
	// Duration is the model-side wall-clock time.
	Duration time.Duration `json:"duration"`
	// InputTokens is the prompt token count for this call.
	InputTokens int `json:"input_tokens"`
	// OutputTokens is the generated token count.
	OutputTokens int `json:"output_tokens"`
	// FinishReason is the model-reported reason for stopping this call.
	FinishReason FinishReason `json:"finish_reason,omitempty"`
}
```

- [ ] **Step 1.6.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run "TestIterationTraceShape|TestToolCallTraceAlias" -v`
Expected: PASS.

### Task 1.7: Add new fields to `AgentResult` (zero-value defaults; not populated yet)

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/core/agent.go` (the `AgentResult` struct at lines 71-87)

- [ ] **Step 1.7.1: Write the failing test**

Append to `/home/nezhifi/Code/LLM/oasis/core/agent_trace_test.go`:

```go
func TestAgentResultNewFields(t *testing.T) {
	r := AgentResult{
		FinishReason:   FinishStop,
		Warnings:       []string{"x"},
		ProviderMeta:   []byte(`{"a":1}`),
		SuspendPayload: []byte(`{"q":"more info?"}`),
		Object:         []byte(`{"title":"x"}`),
	}
	if r.FinishReason != FinishStop {
		t.Errorf("FinishReason lost")
	}
	if len(r.Warnings) != 1 {
		t.Errorf("Warnings lost")
	}
	// Sources, Files, Iterations also exist; default to nil/empty.
	if r.Sources != nil || r.Files != nil || r.Iterations != nil {
		t.Errorf("expected nil slices on fresh struct, got %+v", r)
	}
}
```

- [ ] **Step 1.7.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestAgentResultNewFields -v`
Expected: FAIL — `unknown field FinishReason in struct literal`.

- [ ] **Step 1.7.3: Add the new fields**

In `/home/nezhifi/Code/LLM/oasis/core/agent.go`, replace the existing `AgentResult` struct (lines 71-87) with:

```go
// AgentResult is the output of an Agent.
type AgentResult struct {
	// Output is the agent's final response text.
	Output string
	// Thinking carries the LLM's reasoning/chain-of-thought from the final response.
	// Populated when the provider returns thinking content (e.g. Gemini thought parts).
	// Empty when the provider does not support thinking or thinking is disabled.
	Thinking string
	// Attachments carries optional multimodal content (images, audio, etc.) from the LLM response.
	// Populated when the provider returns media alongside or instead of text.
	Attachments []Attachment
	// Usage tracks aggregate token usage across all LLM calls.
	Usage Usage
	// Steps records per-tool and per-agent execution traces in chronological order.
	// Populated by LLMAgent (tool calls) and Network (tool + agent delegations).
	// Nil when no tools were called.
	Steps []StepTrace

	// FinishReason indicates why the run ended. Zero value is empty string.
	FinishReason FinishReason `json:"finish_reason,omitempty"`
	// Sources are citations declared by tools, retrievers, or the model
	// via the Sourced interface. Nil when no source was declared.
	Sources []Source `json:"sources,omitempty"`
	// Files are attachments produced during the run (sandbox artifacts,
	// generated images). Aggregated from EventFileAttachment.
	Files []Attachment `json:"files,omitempty"`
	// Warnings are non-fatal notes accumulated from providers and
	// decorators. Empty when none.
	Warnings []string `json:"warnings,omitempty"`
	// ProviderMeta carries provider-specific opaque metadata from the
	// final LLM call. Nil when no provider populated it.
	ProviderMeta json.RawMessage `json:"provider_meta,omitempty"`
	// SuspendPayload is set when FinishReason == FinishSuspended. Carries
	// the payload from *ErrSuspended for caller convenience.
	SuspendPayload json.RawMessage `json:"suspend_payload,omitempty"`
	// Object is the final structured output when WithResponseSchema was
	// configured. Nil when the schema was not set or the response did
	// not validate.
	Object json.RawMessage `json:"object,omitempty"`
	// Iterations records per-iteration timing and usage. One entry per
	// LLM call. Nil for runs that hit cancellation before the first call.
	Iterations []IterationTrace `json:"iterations,omitempty"`
}
```

Also ensure `"encoding/json"` is imported at the top of `agent.go`:

```go
import (
	"context"
	"encoding/json"
	"time"
)
```

- [ ] **Step 1.7.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestAgentResultNewFields -v`
Expected: PASS.

- [ ] **Step 1.7.5: Run full project tests to confirm no regression**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...`
Expected: PASS (no regression — nothing populates the new fields yet, but everything still compiles).

### Task 1.8: Request commit approval for Phase 1

- [ ] **Step 1.8.1: Show the user what would be committed**

Run: `cd /home/nezhifi/Code/LLM/oasis && git status && git diff --stat`
Expected: 6 modified/created files in `core/` + new test files. Output sent to user with this message:

> Phase 1 complete. Foundation types added; no behavior change. Would commit as:
>
> `feat(core): add streaming v1 foundation types (FinishReason, lifecycle/object events, Source/Sourced/Warner, IterationTrace, AgentResult new fields)`
>
> Files: `core/stream.go`, `core/stream_test.go`, `core/types.go`, `core/types_test.go`, `core/source.go` (new), `core/source_test.go` (new), `core/agent.go`, `core/agent_trace_test.go` (new).
>
> Approve to commit?

- [ ] **Step 1.8.2: Commit only on explicit approval**

Wait for user message containing "commit it" or equivalent. On approval, run:

```bash
cd /home/nezhifi/Code/LLM/oasis && git add core/stream.go core/stream_test.go core/types.go core/types_test.go core/source.go core/source_test.go core/agent.go core/agent_trace_test.go && git commit -m "$(cat <<'EOF'
feat(core): add streaming v1 foundation types

Adds FinishReason enum, lifecycle event types (run-start/-finish,
iteration-start/-finish), object-stream event types (object-delta,
object-finish, element-delta), Source / Sourced / Warner, and
IterationTrace / LLMCallTrace. AgentResult and ChatResponse gain
optional fields. ToolCallTrace alias for StepTrace.

No behavior change in this commit; emission and population follow
in subsequent phases.

See docs/superpowers/specs/2026-05-21-streaming-world-class-design.md.
EOF
)"
```

---

## Phase 2 — Lifecycle envelope emission

**Goal of phase:** Make the agent loop emit `EventRunStart`, `EventIterationStart`/`Finish`, and `EventRunFinish`. Stop emitting the deprecated `EventInputReceived`, `EventProcessingStart`, `EventMaxIterReached`, `EventHalt`. Update consumers (Network subagent forwarder, SSE) to recognize the new envelope.

### Task 2.1: Emit `EventRunStart` (replacing `EventInputReceived` + `EventProcessingStart`)

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/agentcore.go` (line ~367 — the existing `EventInputReceived` emission)
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/loop.go` (lines ~118-126 — the existing `EventProcessingStart` emission)
- Test: `/home/nezhifi/Code/LLM/oasis/agent/lifecycle_test.go` (create)

- [ ] **Step 2.1.1: Write the failing test**

Create `/home/nezhifi/Code/LLM/oasis/agent/lifecycle_test.go`:

```go
package agent

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestLifecycleEnvelopeRunStart(t *testing.T) {
	// Use the existing callbackProvider helper (defined in agent_test.go)
	// configured to return a simple "ok" response with no tool calls.
	provider := newCallbackProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{Content: "ok", FinishReason: core.FinishStop}, nil
	})
	a := NewLLMAgent("t", "test", provider)

	ch := make(chan core.StreamEvent, 64)
	go func() {
		_, _ = a.ExecuteStream(context.Background(), AgentTask{Input: "hello"}, ch)
	}()

	got := []core.StreamEventType{}
	for ev := range ch {
		got = append(got, ev.Type)
	}

	// First event must be run-start, last must be run-finish. No
	// EventInputReceived or EventProcessingStart should appear.
	if len(got) == 0 || got[0] != core.EventRunStart {
		t.Errorf("first event = %v, want EventRunStart; full: %v", got[0], got)
	}
	if got[len(got)-1] != core.EventRunFinish {
		t.Errorf("last event = %v, want EventRunFinish; full: %v", got[len(got)-1], got)
	}
	for _, ev := range got {
		if ev == core.EventInputReceived || ev == core.EventProcessingStart {
			t.Errorf("deprecated event %v still emitted", ev)
		}
	}
}
```

(If `newCallbackProvider` is not exported, write a minimal in-test stub matching the `core.Provider` interface. Check `agent/agent_test.go` for the pattern.)

- [ ] **Step 2.1.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestLifecycleEnvelopeRunStart -v`
Expected: FAIL — first event is `EventInputReceived`, last is silent close (no `EventRunFinish` emitted yet).

- [ ] **Step 2.1.3: Replace `EventInputReceived` emission with `EventRunStart`**

In `/home/nezhifi/Code/LLM/oasis/agent/agentcore.go` around line 367, find:

```go
case ch <- StreamEvent{Type: EventInputReceived, Name: c.name, Content: task.Input}:
```

Replace with:

```go
case ch <- StreamEvent{Type: EventRunStart, Name: c.name, Content: task.Input}:
```

- [ ] **Step 2.1.4: Delete the now-redundant `EventProcessingStart` emission**

In `/home/nezhifi/Code/LLM/oasis/agent/loop.go` around lines 118-126, delete the entire `// Emit processing-start event...` block (the `if ch != nil { select { case ch <- StreamEvent{Type: EventProcessingStart, ...} } }`). `EventRunStart` (emitted in `agentcore.go`) now covers what `EventProcessingStart` covered.

- [ ] **Step 2.1.5: Run test to verify partial progress**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestLifecycleEnvelopeRunStart -v`
Expected: Still FAILS on the `EventRunFinish` assertion (we haven't added that yet), but the first-event check passes and no deprecated events appear.

### Task 2.2: Emit `EventRunFinish` at every loop exit

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/loop.go` (every `return AgentResult{...}` site in `runLoop` and `forceSynthesis`)
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/iteration.go` (similar)
- Test: `/home/nezhifi/Code/LLM/oasis/agent/lifecycle_test.go`

- [ ] **Step 2.2.1: Read every loop exit site**

Run: `grep -n "return AgentResult\|return result.final\|return result.err" /home/nezhifi/Code/LLM/oasis/agent/loop.go /home/nezhifi/Code/LLM/oasis/agent/iteration.go`
Expected: A list of exit sites. Note each line number.

- [ ] **Step 2.2.2: Add a `finalizeRun` helper**

In `/home/nezhifi/Code/LLM/oasis/agent/loop.go`, add a new helper function at the bottom of the file (after `forceSynthesis`):

```go
// finalizeRun emits EventRunFinish with the supplied FinishReason and result
// metadata, then closes the channel. Idempotent via state.safeCloseCh.
// Pass nil ch in non-streaming mode (Execute path); the function then only
// invokes safeCloseCh and returns.
func finalizeRun(ctx context.Context, ch chan<- StreamEvent, state *loopState, name string, reason FinishReason, result AgentResult) {
	if ch == nil {
		state.safeCloseCh()
		return
	}
	ev := StreamEvent{
		Type:         EventRunFinish,
		Name:         name,
		Content:      result.Output,
		Usage:        result.Usage,
		FinishReason: reason,
		Warnings:     result.Warnings,
		ProviderMeta: result.ProviderMeta,
	}
	if reason == FinishSuspended {
		ev.Content = string(result.SuspendPayload)
	}
	select {
	case ch <- ev:
	case <-ctx.Done():
		// Best-effort: still close.
	}
	state.safeCloseCh()
}
```

- [ ] **Step 2.2.3: Wire `finalizeRun` into each exit site**

For every `return AgentResult{...}` or similar terminal return in `loop.go` and `iteration.go`, add a `finalizeRun(ctx, ch, state, cfg.name, REASON, result)` call **before** the return, where `REASON` is the appropriate `FinishReason` for that exit path:

- Natural completion (no tool calls, model said stop): `FinishStop`
- Tool-call iteration completed and model returned final response: `FinishStop`
- `MaxIter` hit, force synthesis ran: `FinishMaxIter`
- `*ErrHalt` from processor: `FinishHalted`
- `*ErrSuspended`: `FinishSuspended`
- Generic error: `FinishError`
- Context cancellation: `FinishError`

Example transformation in `forceSynthesis` (replacing the existing `EventMaxIterReached` emission):

Before (lines ~177-192):
```go
if ch != nil {
	payload, _ := json.Marshal(...)
	select {
	case ch <- StreamEvent{Type: EventMaxIterReached, ...}:
	case <-ctx.Done():
		state.safeCloseCh()
		return AgentResult{Usage: state.totalUsage}, ctx.Err()
	}
}
```

After:
```go
// EventMaxIterReached is collapsed into FinishReason=FinishMaxIter on
// EventRunFinish (emitted by finalizeRun at the end of this function).
```

And at every `return AgentResult{...}, err` site in this function:

```go
result := AgentResult{...}
finalizeRun(ctx, ch, state, cfg.name, FinishMaxIter, result)
return result, err
```

Repeat the pattern for each exit in `runLoop` and `iteration.go` with the appropriate `FinishReason`.

- [ ] **Step 2.2.4: Run lifecycle test**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestLifecycleEnvelopeRunStart -v`
Expected: PASS.

- [ ] **Step 2.2.5: Run full agent tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -v`
Expected: PASS. If any existing test asserts `EventMaxIterReached` or `EventHalt`, it will fail. Update those assertions to look for `EventRunFinish` with the appropriate `FinishReason` instead.

### Task 2.3: Emit `EventIterationStart` / `EventIterationFinish`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/iteration.go` (around `runIteration`)
- Test: `/home/nezhifi/Code/LLM/oasis/agent/lifecycle_test.go`

- [ ] **Step 2.3.1: Write the failing test**

Append to `/home/nezhifi/Code/LLM/oasis/agent/lifecycle_test.go`:

```go
func TestLifecycleEnvelopeIterations(t *testing.T) {
	// Provider returns a tool call on iteration 0, then "done" on iteration 1.
	iter := 0
	provider := newCallbackProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		defer func() { iter++ }()
		if iter == 0 {
			return core.ChatResponse{
				ToolCalls:    []core.ToolCall{{ID: "1", Name: "noop", Args: []byte(`{}`)}},
				FinishReason: core.FinishToolCalls,
			}, nil
		}
		return core.ChatResponse{Content: "done", FinishReason: core.FinishStop}, nil
	})
	noop := newCallbackTool("noop", func(ctx context.Context, args []byte) (core.ToolResult, error) {
		return core.ToolResult{Content: []byte(`"ok"`)}, nil
	})
	a := NewLLMAgent("t", "test", provider, WithTools(noop))

	ch := make(chan core.StreamEvent, 64)
	go func() { _, _ = a.ExecuteStream(context.Background(), AgentTask{Input: "x"}, ch) }()

	starts, finishes := 0, 0
	for ev := range ch {
		if ev.Type == core.EventIterationStart {
			starts++
		}
		if ev.Type == core.EventIterationFinish {
			finishes++
		}
	}
	if starts != 2 || finishes != 2 {
		t.Errorf("starts=%d finishes=%d, want 2/2", starts, finishes)
	}
}
```

If `newCallbackTool` is not present in test helpers, write a minimal in-test stub.

- [ ] **Step 2.3.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestLifecycleEnvelopeIterations -v`
Expected: FAIL — zero iteration events emitted.

- [ ] **Step 2.3.3: Wrap iteration body with envelope events**

In `/home/nezhifi/Code/LLM/oasis/agent/iteration.go`, find the entry to `runIteration(...)`. At the start of the function (after any nil-guard) and before the iteration's main work:

```go
// Emit iteration-start.
if ch != nil {
	select {
	case ch <- StreamEvent{
		Type: EventIterationStart,
		Name: strconv.Itoa(iter),
	}:
	case <-ctx.Done():
	}
}
iterStart := time.Now()
```

Then at every exit from `runIteration` (success or error, before `return`), emit `EventIterationFinish`:

```go
if ch != nil {
	select {
	case ch <- StreamEvent{
		Type:     EventIterationFinish,
		Name:     strconv.Itoa(iter),
		Usage:    iterUsage,
		Duration: time.Since(iterStart),
	}:
	case <-ctx.Done():
	}
}
```

Where `iterUsage` is the usage for that iteration (the LLM call's `resp.Usage` for a non-tool iteration; sum of model + tool usage for a tool iteration). If exact per-iteration usage is not yet tracked, use `core.Usage{}` for now and a TODO to fill it in Task 4.3.

Add `"strconv"` and `"time"` to the imports if missing.

- [ ] **Step 2.3.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestLifecycleEnvelopeIterations -v`
Expected: PASS.

### Task 2.4: Update subagent stream forwarder to recognize the new envelope

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/agentcore.go` (lines ~438-460 — the `ExecuteSubAgent`-style forwarder that filters `EventInputReceived` from subagents)

- [ ] **Step 2.4.1: Write the failing test**

Create `/home/nezhifi/Code/LLM/oasis/agent/lifecycle_subagent_test.go`:

```go
package agent

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/core"
)

// Verifies that when a Network forwards subagent events to the parent channel,
// the subagent's EventRunStart and EventRunFinish are suppressed (the Network
// uses its own EventAgentStart / EventAgentFinish envelope).
func TestSubagentEnvelopeSuppressed(t *testing.T) {
	// Construct a Network with one subagent that produces a single-text-delta run.
	// Assert no EventRunStart or EventRunFinish from the subagent leaks through.
	// (Refer to network_test.go for Network construction patterns.)
	t.Skip("implementation depends on network test harness; flesh out during execution")
}
```

(This test is a placeholder skip — flesh out using the patterns in `network/network_test.go` during execution.)

- [ ] **Step 2.4.2: Update the subagent-event filter**

In `/home/nezhifi/Code/LLM/oasis/agent/agentcore.go` around line 459, find:

```go
if ev.Type == EventInputReceived {
```

Replace with:

```go
// Subagents emit their own run envelope; suppress it because the parent
// Network emits EventAgentStart / EventAgentFinish for the same boundary.
if ev.Type == EventRunStart || ev.Type == EventRunFinish ||
	ev.Type == EventIterationStart || ev.Type == EventIterationFinish {
```

Update the surrounding comment to:

```go
// Filter run/iteration envelope events from sub-agents — the parent
// network emits EventAgentStart / EventAgentFinish as its own envelope.
```

- [ ] **Step 2.4.3: Run network tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent ./network -v`
Expected: PASS.

### Task 2.5: Replace `EventHalt` emission with `EventRunFinish{FinishReason: FinishHalted}`

**Files:**
- Search: where `EventHalt` is currently emitted (likely in processor halt handling in `loop.go` or a processor wrapper)

- [ ] **Step 2.5.1: Find the current `EventHalt` emission site**

Run: `grep -rn "EventHalt" /home/nezhifi/Code/LLM/oasis --include='*.go'`
Expected: One or two emission sites in agent code, plus the constant declaration.

- [ ] **Step 2.5.2: Replace the emission with `finalizeRun(..., FinishHalted, ...)`**

At each `EventHalt` emission site, replace the emission with construction of an `AgentResult{Output: cannedResponse}` and a call to `finalizeRun(ctx, ch, state, cfg.name, FinishHalted, result)`. The processor's canned response becomes `EventRunFinish.Content`.

- [ ] **Step 2.5.3: Run tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -v`
Expected: PASS.

### Task 2.6: Update `ServeSSE` to recognize the run-finish envelope

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/stream.go` (the `for ev := range ch` loop and the `done` payload)

- [ ] **Step 2.6.1: Read the current `ServeSSE`**

Run: `Read /home/nezhifi/Code/LLM/oasis/agent/stream.go`
Expected: See the existing pattern of writing each event and emitting a final `done` event with the result.

- [ ] **Step 2.6.2: Decide on the `done` event**

Keep the existing `done` event for back-compat (web clients depend on it). `EventRunFinish` is *also* emitted as a regular event inside the loop just before `done`. Net effect: clients that read `EventRunFinish` get rich envelope data; legacy clients that wait for `done` keep working.

No code change required for `ServeSSE`. Document this in the godoc:

```go
// ServeSSE streams an agent's response as Server-Sent Events over HTTP.
// ...
// The stream emits EventRunStart first and EventRunFinish last; the loop
// then writes a final "done" SSE event for legacy clients that wait on it.
```

- [ ] **Step 2.6.3: Update the godoc**

In `/home/nezhifi/Code/LLM/oasis/agent/stream.go`, update the `ServeSSE` godoc comment to mention the envelope.

- [ ] **Step 2.6.4: Run tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -v`
Expected: PASS.

### Task 2.7: Request commit approval for Phase 2

- [ ] **Step 2.7.1: Show the user what would be committed**

Run: `cd /home/nezhifi/Code/LLM/oasis && git status && git diff --stat`
Expected: ~5 modified files plus 2 new test files. Output to user:

> Phase 2 complete. Lifecycle envelope is live: every run begins with run-start and ends with run-finish carrying FinishReason. Deprecated EventInputReceived/EventProcessingStart/EventMaxIterReached/EventHalt are no longer emitted (constants kept for one-minor back-compat).
>
> Approve to commit as `feat(agent): emit lifecycle envelope (run-start/finish, iteration-start/finish) and replace deprecated special-case events`?

- [ ] **Step 2.7.2: Commit only on explicit approval**

Same pattern as Step 1.8.2. Wait for "commit it" before running `git commit`.

---

## Phase 3 — Result accessor populators (sync path)

**Goal of phase:** Fill `AgentResult.FinishReason`, `.Warnings`, `.ProviderMeta`, `.SuspendPayload`, `.Files` during the run. (`Sources` deferred to Phase 7 after RAG retrievers implement `Sourced`; `Object` deferred to Phase 6; `Iterations` to Phase 4.)

### Task 3.1: Set `AgentResult.FinishReason` at every loop exit

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/loop.go` and `iteration.go` (the same exit sites updated in Phase 2)
- Test: `/home/nezhifi/Code/LLM/oasis/agent/result_accessor_test.go` (create)

- [ ] **Step 3.1.1: Write the failing test**

Create `/home/nezhifi/Code/LLM/oasis/agent/result_accessor_test.go`:

```go
package agent

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestAgentResultFinishReasonNaturalStop(t *testing.T) {
	provider := newCallbackProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{Content: "done", FinishReason: core.FinishStop}, nil
	})
	a := NewLLMAgent("t", "test", provider)
	result, err := a.Execute(context.Background(), AgentTask{Input: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinishReason != core.FinishStop {
		t.Errorf("FinishReason = %q, want stop", result.FinishReason)
	}
}
```

- [ ] **Step 3.1.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestAgentResultFinishReasonNaturalStop -v`
Expected: FAIL — `FinishReason = ""`.

- [ ] **Step 3.1.3: Set `FinishReason` at each exit**

In the same exit sites updated in Task 2.2, set `result.FinishReason = REASON` (using the same `REASON` value passed to `finalizeRun`) **before** constructing the result for `finalizeRun`. The `finalizeRun` helper already carries it to the event; this commit makes it visible on the returned `AgentResult` too.

Example:
```go
result := AgentResult{
	Output:       finalContent,
	Usage:        state.totalUsage,
	FinishReason: FinishStop,
}
finalizeRun(ctx, ch, state, cfg.name, FinishStop, result)
return result, nil
```

- [ ] **Step 3.1.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestAgentResultFinishReasonNaturalStop -v`
Expected: PASS.

### Task 3.2: Set `AgentResult.SuspendPayload` on suspend exits

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/suspend.go` (or wherever `*ErrSuspended` returns are constructed)
- Test: `result_accessor_test.go`

- [ ] **Step 3.2.1: Find the suspend exit site**

Run: `grep -rn "ErrSuspended\b" /home/nezhifi/Code/LLM/oasis/agent --include='*.go'`
Expected: One or two sites where `*ErrSuspended` is created with a payload.

- [ ] **Step 3.2.2: Write the failing test**

Append to `result_accessor_test.go`:

```go
func TestAgentResultSuspendPayload(t *testing.T) {
	// Build a provider whose first reply triggers a suspend-via-tool.
	// (Reuse existing suspend test harness; or stub a tool that returns
	// (zero, agent.Suspend(payload)).)
	t.Skip("flesh out using suspend_test.go patterns during execution")
}
```

- [ ] **Step 3.2.3: Populate `SuspendPayload` on the result**

At the suspend-exit site, set:

```go
result.FinishReason = FinishSuspended
result.SuspendPayload = json.RawMessage(suspendedErr.Payload)  // or however the payload is referenced
```

before returning. `finalizeRun` already picks up `result.SuspendPayload` (Phase 2 Task 2.2.2 finalizeRun copies it into `EventRunFinish.Content` when `FinishSuspended`).

- [ ] **Step 3.2.4: Run tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -v`
Expected: PASS.

### Task 3.3: Carry `Warnings` and `ProviderMeta` from the last LLM call onto `AgentResult`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/iteration.go` (after each `provider.ChatStream` / `Chat` call site)
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/loop.go` (the `loopState` struct — track latest provider response metadata)
- Test: `result_accessor_test.go`

- [ ] **Step 3.3.1: Add metadata fields to `loopState`**

In `/home/nezhifi/Code/LLM/oasis/agent/loop.go`, find the `loopState` struct (around line 152) and add fields:

```go
type loopState struct {
	messages          []ChatMessage
	messageRuneCount  int
	attachByteBudget  int
	hasAgentTools     bool
	compressThreshold int
	safeCloseCh       func()

	// Carry forward across iterations; the final values land on AgentResult.
	totalUsage   Usage
	lastWarnings []string
	lastProviderMeta json.RawMessage
}
```

(Existing fields shown for context; add only the three new ones if `totalUsage` already exists. Adjust based on actual struct.)

- [ ] **Step 3.3.2: After each provider call, capture the metadata**

In `/home/nezhifi/Code/LLM/oasis/agent/iteration.go`, immediately after each successful `provider.ChatStream` / `Chat` call:

```go
// Accumulate provider warnings and remember the latest provider meta.
if len(resp.Warnings) > 0 {
	state.lastWarnings = append(state.lastWarnings, resp.Warnings...)
}
if len(resp.ProviderMeta) > 0 {
	state.lastProviderMeta = resp.ProviderMeta
}
```

- [ ] **Step 3.3.3: Carry into the final `AgentResult`**

At each `return AgentResult{...}` site, populate:

```go
result := AgentResult{
	// existing fields ...
	Warnings:     state.lastWarnings,
	ProviderMeta: state.lastProviderMeta,
}
```

- [ ] **Step 3.3.4: Write the test**

Append to `result_accessor_test.go`:

```go
func TestAgentResultWarningsAndProviderMeta(t *testing.T) {
	provider := newCallbackProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{
			Content:      "ok",
			FinishReason: core.FinishStop,
			Warnings:     []string{"fallback-model-used"},
			ProviderMeta: []byte(`{"safety":"ok"}`),
		}, nil
	})
	a := NewLLMAgent("t", "test", provider)
	result, _ := a.Execute(context.Background(), AgentTask{Input: "x"})
	if len(result.Warnings) != 1 || result.Warnings[0] != "fallback-model-used" {
		t.Errorf("Warnings = %v", result.Warnings)
	}
	if string(result.ProviderMeta) != `{"safety":"ok"}` {
		t.Errorf("ProviderMeta = %s", result.ProviderMeta)
	}
}
```

- [ ] **Step 3.3.5: Run tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestAgentResultWarningsAndProviderMeta -v`
Expected: PASS.

### Task 3.4: Aggregate `EventFileAttachment` events into `AgentResult.Files`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/loop.go` (the loop reads `EventFileAttachment` already; sink them into `state.files`)
- Test: `result_accessor_test.go`

- [ ] **Step 3.4.1: Add `files` slice to `loopState`**

In `loopState`, add:

```go
files []Attachment
```

- [ ] **Step 3.4.2: Capture file events**

Find the existing site that handles `EventFileAttachment` (likely an intermediate channel between provider and external `ch`). When an event of that type passes through, also append to `state.files`:

```go
if ev.Type == EventFileAttachment {
	var att Attachment
	if err := json.Unmarshal(ev.Args, &att); err == nil {
		state.files = append(state.files, att)
	}
}
```

(The exact unmarshal target depends on what's carried on `ev`. If `EventFileAttachment` carries an `Attachment` JSON in `Content`, adapt accordingly.)

- [ ] **Step 3.4.3: Carry into `AgentResult`**

Set `result.Files = state.files` at every result-construction site.

- [ ] **Step 3.4.4: Write the test**

Use a stub provider that pushes an `EventFileAttachment` into the channel, plus a tool that produces it; verify `result.Files` has length 1.

```go
func TestAgentResultFilesAggregated(t *testing.T) {
	t.Skip("requires a stub provider that emits EventFileAttachment; flesh out during execution using patterns in agent_test.go")
}
```

- [ ] **Step 3.4.5: Run tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -v`
Expected: PASS.

### Task 3.5: Request commit approval for Phase 3

- [ ] **Step 3.5.1: Show what would be committed**

Run: `cd /home/nezhifi/Code/LLM/oasis && git status && git diff --stat`
Expected: ~4 modified files. Output:

> Phase 3 complete. AgentResult now carries FinishReason, Warnings, ProviderMeta, SuspendPayload, Files. Sources and Object deferred to later phases.
>
> Approve to commit as `feat(agent): populate AgentResult.FinishReason / Warnings / ProviderMeta / SuspendPayload / Files`?

- [ ] **Step 3.5.2: Commit only on approval**

Same pattern as previous commit gates.

---

## Phase 4 — Per-stream observability (spans + IterationTrace)

**Goal of phase:** Add `agent.iteration` and `llm.generate` spans nested under the existing `agent.execute` root. Populate `AgentResult.Iterations` with per-iteration timing/usage.

### Task 4.1: Add `agent.iteration` span around `runIteration`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/iteration.go`
- Test: `/home/nezhifi/Code/LLM/oasis/agent/span_test.go` (create — uses a fake tracer)

- [ ] **Step 4.1.1: Find or write a fake tracer for testing**

Run: `grep -rn "type fakeTracer\|type recordingTracer\|core.Tracer" /home/nezhifi/Code/LLM/oasis/agent --include='*_test.go'`
Expected: May find an existing test fake. If not, write one.

If absent, create `/home/nezhifi/Code/LLM/oasis/agent/span_test.go` with:

```go
package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/nevindra/oasis/core"
)

type recordingSpan struct {
	name  string
	attrs []core.SpanAttr
	ended bool
}

func (s *recordingSpan) SetAttr(attrs ...core.SpanAttr) { s.attrs = append(s.attrs, attrs...) }
func (s *recordingSpan) End(attrs ...core.SpanAttr)     { s.attrs = append(s.attrs, attrs...); s.ended = true }
func (s *recordingSpan) Event(name string, attrs ...core.SpanAttr) {}

type recordingTracer struct {
	mu    sync.Mutex
	spans []*recordingSpan
}

func (t *recordingTracer) Start(ctx context.Context, name string, attrs ...core.SpanAttr) (context.Context, core.Span) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sp := &recordingSpan{name: name, attrs: append([]core.SpanAttr(nil), attrs...)}
	t.spans = append(t.spans, sp)
	return ctx, sp
}

func (t *recordingTracer) names() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.spans))
	for i, s := range t.spans {
		out[i] = s.name
	}
	return out
}

func TestIterationSpanCreated(t *testing.T) {
	tracer := &recordingTracer{}
	provider := newCallbackProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{Content: "ok", FinishReason: core.FinishStop}, nil
	})
	a := NewLLMAgent("t", "test", provider, WithTracer(tracer))
	_, _ = a.Execute(context.Background(), AgentTask{Input: "x"})

	names := tracer.names()
	want := []string{"agent.execute", "agent.iteration"}
	for _, w := range want {
		found := false
		for _, n := range names {
			if n == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected span %q, got %v", w, names)
		}
	}
}
```

(`WithTracer` may already exist; check `agent/options.go` or similar. If not, add it via `WithTracer(t core.Tracer)` as a new option following the existing pattern.)

- [ ] **Step 4.1.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestIterationSpanCreated -v`
Expected: FAIL — `agent.iteration` span missing.

- [ ] **Step 4.1.3: Add the span**

In `/home/nezhifi/Code/LLM/oasis/agent/iteration.go`, immediately at the start of `runIteration`:

```go
var iterSpan core.Span
iterCtx := ctx
if cfg.tracer != nil {
	iterCtx, iterSpan = cfg.tracer.Start(ctx, "agent.iteration",
		core.IntAttr("iter", iter),
	)
	defer func() {
		if iterSpan != nil {
			iterSpan.End()
		}
	}()
}
// Use iterCtx for the rest of the iteration's work.
ctx = iterCtx
```

- [ ] **Step 4.1.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestIterationSpanCreated -v`
Expected: PASS.

### Task 4.2: Add `llm.generate` span around each provider call

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/iteration.go` (each `provider.ChatStream` and `Chat` call site)
- Test: `span_test.go`

- [ ] **Step 4.2.1: Write the failing test**

Append to `span_test.go`:

```go
func TestLLMGenerateSpanCreated(t *testing.T) {
	tracer := &recordingTracer{}
	provider := newCallbackProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{Content: "ok", FinishReason: core.FinishStop}, nil
	})
	a := NewLLMAgent("t", "test", provider, WithTracer(tracer))
	_, _ = a.Execute(context.Background(), AgentTask{Input: "x"})

	names := tracer.names()
	found := false
	for _, n := range names {
		if n == "llm.generate" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected llm.generate span, got %v", names)
	}
}
```

- [ ] **Step 4.2.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestLLMGenerateSpanCreated -v`
Expected: FAIL.

- [ ] **Step 4.2.3: Add the span wrapper around each provider call**

At each `provider.ChatStream(...)` or `provider.Chat(...)` call site in `iteration.go`, wrap with:

```go
var llmSpan core.Span
llmCtx := ctx
if cfg.tracer != nil {
	llmCtx, llmSpan = cfg.tracer.Start(ctx, "llm.generate",
		core.StringAttr("provider", cfg.provider.Name()),
		// Add model name if the provider exposes it.
	)
}
resp, err := cfg.provider.ChatStream(llmCtx, req, ch)
if llmSpan != nil {
	llmSpan.SetAttr(
		core.IntAttr("input_tokens", resp.Usage.InputTokens),
		core.IntAttr("output_tokens", resp.Usage.OutputTokens),
		core.StringAttr("finish_reason", string(resp.FinishReason)),
	)
	llmSpan.End()
}
```

- [ ] **Step 4.2.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestLLMGenerateSpanCreated -v`
Expected: PASS.

### Task 4.3: Populate `AgentResult.Iterations`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/iteration.go` (build an `IterationTrace` per iteration; append to `state.iterations`)
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/loop.go` (add `iterations []IterationTrace` to `loopState`; copy onto `AgentResult`)
- Test: `result_accessor_test.go`

- [ ] **Step 4.3.1: Write the failing test**

Append to `result_accessor_test.go`:

```go
func TestAgentResultIterationsPopulated(t *testing.T) {
	provider := newCallbackProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{
			Content:      "ok",
			Usage:        core.Usage{InputTokens: 10, OutputTokens: 5},
			FinishReason: core.FinishStop,
		}, nil
	})
	a := NewLLMAgent("t", "test", provider)
	result, _ := a.Execute(context.Background(), AgentTask{Input: "x"})
	if len(result.Iterations) != 1 {
		t.Fatalf("Iterations len = %d, want 1", len(result.Iterations))
	}
	it := result.Iterations[0]
	if it.Iter != 0 {
		t.Errorf("Iter = %d", it.Iter)
	}
	if it.LLMCall.InputTokens != 10 || it.LLMCall.OutputTokens != 5 {
		t.Errorf("LLMCall = %+v", it.LLMCall)
	}
	if it.LLMCall.FinishReason != core.FinishStop {
		t.Errorf("LLMCall.FinishReason = %q", it.LLMCall.FinishReason)
	}
}
```

- [ ] **Step 4.3.2: Add `iterations` to `loopState`**

In `loop.go`:

```go
type loopState struct {
	// existing fields ...
	iterations []IterationTrace
}
```

- [ ] **Step 4.3.3: Build the `IterationTrace` per iteration**

In `iteration.go`, near where the iteration finishes (just before the `EventIterationFinish` emission from Task 2.3), construct:

```go
trace := IterationTrace{
	Iter:      iter,
	Model:     cfg.provider.Name(), // Use a richer source if model name is available.
	StartedAt: iterStart,
	Duration:  time.Since(iterStart),
	LLMCall: LLMCallTrace{
		Duration:     llmDuration,
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		FinishReason: resp.FinishReason,
	},
	ToolCalls: iterToolCalls,  // built from this iteration's tool dispatches
	Usage:     resp.Usage,
}
state.iterations = append(state.iterations, trace)
```

(`llmDuration` and `iterToolCalls` are local variables you maintain during the iteration. If `iterToolCalls` isn't tracked yet, set it to nil for now — a future iteration of this plan can populate it from existing step recording.)

- [ ] **Step 4.3.4: Copy onto `AgentResult`**

At every result-construction site:

```go
result := AgentResult{
	// existing fields ...
	Iterations: state.iterations,
}
```

- [ ] **Step 4.3.5: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestAgentResultIterationsPopulated -v`
Expected: PASS.

### Task 4.4: Request commit approval for Phase 4

- [ ] **Step 4.4.1: Show what would be committed**

Run: `cd /home/nezhifi/Code/LLM/oasis && git status && git diff --stat`
Expected: ~3 modified files + 1 new test file. Output:

> Phase 4 complete. agent.iteration and llm.generate spans nest under agent.execute when a Tracer is configured. AgentResult.Iterations is populated for every run. Zero overhead when no tracer.
>
> Approve to commit as `feat(agent): per-iteration and per-LLM spans + IterationTrace on AgentResult`?

- [ ] **Step 4.4.2: Commit only on approval**

Same pattern.

---

## Phase 5 — Forgiving partial-JSON parser (`core/partial_json.go`)

**Goal of phase:** Build a pure-stdlib parser that takes incomplete JSON input and returns the most-complete valid JSON snapshot it can produce — closing open strings, dropping incomplete tail values, terminating open objects/arrays. Zero external deps. Heavily tested.

### Task 5.1: Define the parser entry point and write fixture-based tests

**Files:**
- Create: `/home/nezhifi/Code/LLM/oasis/core/partial_json.go`
- Create: `/home/nezhifi/Code/LLM/oasis/core/partial_json_test.go`

- [ ] **Step 5.1.1: Write the failing test (cases)**

Create `/home/nezhifi/Code/LLM/oasis/core/partial_json_test.go`:

```go
package core

import (
	"encoding/json"
	"testing"
)

func TestPartialJSONComplete(t *testing.T) {
	got, ok := PartialJSON([]byte(`{"a":1}`))
	if !ok {
		t.Fatal("expected ok")
	}
	if string(got) != `{"a":1}` {
		t.Errorf("got %s", got)
	}
}

func TestPartialJSONOpenString(t *testing.T) {
	got, ok := PartialJSON([]byte(`{"title":"hello wor`))
	if !ok {
		t.Fatal("expected ok")
	}
	// Closes the open string and the open object.
	var probe map[string]any
	if err := json.Unmarshal(got, &probe); err != nil {
		t.Fatalf("not valid JSON: %s (%v)", got, err)
	}
	if probe["title"] != "hello wor" {
		t.Errorf("title = %v", probe["title"])
	}
}

func TestPartialJSONOpenObject(t *testing.T) {
	got, ok := PartialJSON([]byte(`{"a":1,`))
	if !ok {
		t.Fatal("expected ok")
	}
	// Trailing comma is dropped, object closed.
	if string(got) != `{"a":1}` {
		t.Errorf("got %s", got)
	}
}

func TestPartialJSONOpenArray(t *testing.T) {
	got, ok := PartialJSON([]byte(`{"items":[1,2,`))
	if !ok {
		t.Fatal("expected ok")
	}
	if string(got) != `{"items":[1,2]}` {
		t.Errorf("got %s", got)
	}
}

func TestPartialJSONIncompleteNumber(t *testing.T) {
	got, ok := PartialJSON([]byte(`{"n":12.`))
	if !ok {
		t.Fatal("expected ok")
	}
	// Incomplete number dropped along with the open key.
	if string(got) != `{}` {
		t.Errorf("got %s", got)
	}
}

func TestPartialJSONEscapedQuote(t *testing.T) {
	got, ok := PartialJSON([]byte(`{"a":"x\"y`))
	if !ok {
		t.Fatal("expected ok")
	}
	var probe map[string]any
	if err := json.Unmarshal(got, &probe); err != nil {
		t.Fatalf("not valid: %s (%v)", got, err)
	}
	if probe["a"] != `x"y` {
		t.Errorf("a = %v", probe["a"])
	}
}

func TestPartialJSONEmpty(t *testing.T) {
	_, ok := PartialJSON([]byte(``))
	if ok {
		t.Error("expected !ok for empty input")
	}
}
```

- [ ] **Step 5.1.2: Run tests to verify they fail**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestPartialJSON -v`
Expected: FAIL — `undefined: PartialJSON`.

- [ ] **Step 5.1.3: Implement the parser**

Create `/home/nezhifi/Code/LLM/oasis/core/partial_json.go`:

```go
package core

import "encoding/json"

// PartialJSON takes a byte prefix of a valid JSON document and returns the
// most-complete valid JSON value it can produce by closing open strings,
// dropping incomplete tail values, and terminating open objects/arrays.
//
// Returns (snapshot, true) when a valid snapshot is produced; (nil, false)
// when the input is too incomplete (empty, malformed, or contains no
// emittable value).
//
// Used by the agent loop to emit EventObjectDelta snapshots as the model
// streams structured output under WithResponseSchema. Pure stdlib; safe
// for concurrent use.
//
// Semantics:
//   - Inside an open string ("hello wor): close the string, terminate
//     all enclosing containers, return the result.
//   - Open container ({ "a":1, or [1,2,): drop any trailing-comma /
//     incomplete tail value, then close the container.
//   - Incomplete primitive (12., tru, fals): drop the key:value pair
//     it belongs to, then close enclosing containers.
//   - Escapes inside strings are honored (\\\", \\\\, \\n, etc.).
func PartialJSON(input []byte) ([]byte, bool) {
	if len(input) == 0 {
		return nil, false
	}

	// Parser state.
	var (
		stack    []byte // container stack: '{' or '['
		inString bool
		escape   bool
		// safeEnd marks the byte offset just past the last "safely
		// completable" position. We rewind to here if we encounter an
		// incomplete tail value.
		safeEnd int
	)

	emitClose := func(out []byte) []byte {
		// Close any open containers in reverse order.
		for i := len(stack) - 1; i >= 0; i-- {
			switch stack[i] {
			case '{':
				out = append(out, '}')
			case '[':
				out = append(out, ']')
			}
		}
		return out
	}

	// First pass: track string/escape state and stack depth. Identify
	// safe-end positions (after a complete value or after a comma we can
	// trim).
	out := make([]byte, 0, len(input)+8)
	for i := 0; i < len(input); i++ {
		b := input[i]
		if inString {
			out = append(out, b)
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
				safeEnd = len(out)
			}
			continue
		}
		switch b {
		case '"':
			inString = true
			out = append(out, b)
		case '{', '[':
			stack = append(stack, b)
			out = append(out, b)
			safeEnd = len(out)
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			out = append(out, b)
			safeEnd = len(out)
		case ' ', '\t', '\n', '\r':
			out = append(out, b)
		case ',':
			out = append(out, b)
		default:
			out = append(out, b)
		}
	}

	// If we ended mid-string, close it.
	if inString {
		out = append(out, '"')
		safeEnd = len(out)
	}

	// Trim to last safe-end (drops trailing partial values and lone
	// trailing commas inside containers).
	out = out[:safeEnd]
	// Drop a trailing comma if any container's last token is "," — that
	// would make the resulting JSON invalid.
	for len(out) > 0 && (out[len(out)-1] == ',' || out[len(out)-1] == ' ' || out[len(out)-1] == '\t' || out[len(out)-1] == '\n' || out[len(out)-1] == '\r') {
		out = out[:len(out)-1]
	}

	// Close still-open containers.
	out = emitClose(out)

	// Validate before returning. If the trim/close left an invalid result,
	// signal "no snapshot."
	if !json.Valid(out) {
		return nil, false
	}
	return out, true
}
```

- [ ] **Step 5.1.4: Run tests to verify they pass**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestPartialJSON -v`
Expected: PASS. If any case fails, iterate on the parser. Note: the parser above is a starting point — execution may discover edge cases (deeply nested incomplete values, key-without-value, etc.) that need targeted fixes. Add failing-case tests first; fix; repeat.

### Task 5.2: Property-based test against `encoding/json`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/core/partial_json_test.go`

- [ ] **Step 5.2.1: Add the property test**

Append:

```go
func TestPartialJSONPropertyAllPrefixesValid(t *testing.T) {
	// Inputs: well-formed JSON. Every byte prefix MUST yield either
	// (nil, false) or (snapshot, true) where snapshot is valid JSON.
	inputs := [][]byte{
		[]byte(`{"a":1,"b":[1,2,3]}`),
		[]byte(`["x","y","z"]`),
		[]byte(`{"nested":{"deep":{"k":"v"}}}`),
		[]byte(`{"s":"hello \"world\""}`),
		[]byte(`[true,false,null,42,3.14]`),
	}
	for _, full := range inputs {
		for i := 1; i <= len(full); i++ {
			prefix := full[:i]
			snap, ok := PartialJSON(prefix)
			if !ok {
				continue
			}
			if !json.Valid(snap) {
				t.Errorf("prefix %q produced invalid JSON: %s", prefix, snap)
			}
		}
	}
}
```

- [ ] **Step 5.2.2: Run the property test**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./core -run TestPartialJSONPropertyAllPrefixesValid -v`
Expected: PASS. If failures appear, fix the parser to handle them and re-run until clean.

### Task 5.3: Request commit approval for Phase 5

- [ ] **Step 5.3.1: Show what would be committed**

Run: `cd /home/nezhifi/Code/LLM/oasis && git status && git diff --stat`
Expected: 2 new files. Output:

> Phase 5 complete. Forgiving partial-JSON parser in core/partial_json.go (~80 LOC) with unit + property tests. No integration with the loop yet.
>
> Approve to commit as `feat(core): add forgiving partial-JSON parser for structured object streaming`?

- [ ] **Step 5.3.2: Commit only on approval**

Same pattern.

---

## Phase 6 — Structured object streaming integration

**Goal of phase:** When `WithResponseSchema` is configured, the loop accumulates text deltas, calls `PartialJSON` on the buffer, emits `EventObjectDelta` with the snapshot, and emits `EventObjectFinish` with the final validated bytes. For top-level array schemas, emit one `EventElementDelta` per completed element.

### Task 6.1: Emit `EventObjectDelta` during text streaming when schema is set

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/iteration.go` (or wherever text deltas flow through)
- Test: `/home/nezhifi/Code/LLM/oasis/agent/object_stream_test.go` (create)

- [ ] **Step 6.1.1: Write the failing test**

Create `/home/nezhifi/Code/LLM/oasis/agent/object_stream_test.go`:

```go
package agent

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestObjectDeltaEmitted(t *testing.T) {
	provider := newCallbackProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `{"title":`}
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `"Q3 Report","sections":[`}
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `"intro","summary"]}`}
		close(ch)
		return core.ChatResponse{
			Content:      `{"title":"Q3 Report","sections":["intro","summary"]}`,
			FinishReason: core.FinishStop,
		}, nil
	})

	schema := core.NewResponseSchema("Report", &core.SchemaObject{
		Type: "object",
		Properties: map[string]*core.SchemaObject{
			"title":    {Type: "string"},
			"sections": {Type: "array", Items: &core.SchemaObject{Type: "string"}},
		},
	})

	a := NewLLMAgent("t", "test", provider, WithResponseSchema(schema))

	ch := make(chan core.StreamEvent, 64)
	go func() { _, _ = a.ExecuteStream(context.Background(), AgentTask{Input: "x"}, ch) }()

	deltas, finish := 0, 0
	for ev := range ch {
		if ev.Type == core.EventObjectDelta {
			deltas++
		}
		if ev.Type == core.EventObjectFinish {
			finish++
		}
	}
	if deltas < 1 {
		t.Errorf("expected >=1 EventObjectDelta, got %d", deltas)
	}
	if finish != 1 {
		t.Errorf("expected exactly 1 EventObjectFinish, got %d", finish)
	}
}
```

- [ ] **Step 6.1.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestObjectDeltaEmitted -v`
Expected: FAIL — zero object events.

- [ ] **Step 6.1.3: Add a stream-side object emitter**

In `iteration.go` (or in a new helper file `agent/object_stream.go`), add a goroutine-side handler that watches the `iterCh` text-delta accumulator. The cleanest hook point is the existing per-iteration channel forwarder (`newStreamForwarder`). Modify the forwarder so that when `cfg.responseSchema != nil`:

1. Accumulate `EventTextDelta.Content` into a local buffer.
2. After each delta, call `core.PartialJSON(buf)`.
3. If a snapshot is returned and differs from the last emitted snapshot, emit `EventObjectDelta{Object: snapshot}`.

Sketch:

```go
// In the forwarder, when schema mode is active:
var (
	buf        []byte
	lastEmit   []byte
)
for ev := range iterCh {
	if ev.Type == EventTextDelta {
		buf = append(buf, ev.Content...)
		if snap, ok := core.PartialJSON(buf); ok && !bytes.Equal(snap, lastEmit) {
			lastEmit = append(lastEmit[:0], snap...)
			// Emit before forwarding the text-delta so consumers see the
			// object update alongside the text.
			select {
			case dest <- StreamEvent{Type: EventObjectDelta, Object: snap}:
			case <-ctx.Done():
				return
			}
		}
	}
	// Forward the original event as before.
	select {
	case dest <- ev:
	case <-ctx.Done():
		return
	}
}
```

(Adapt to actual forwarder shape; the goal is: text delta in → object delta out alongside.)

- [ ] **Step 6.1.4: Emit `EventObjectFinish` after the final LLM call**

In the final `AgentResult` construction site (inside the last iteration's completion path), if `cfg.responseSchema != nil` and the final `resp.Content` is non-empty:

```go
if ch != nil && cfg.responseSchema != nil && len(resp.Content) > 0 {
	if json.Valid([]byte(resp.Content)) {
		select {
		case ch <- StreamEvent{Type: EventObjectFinish, Object: []byte(resp.Content)}:
		case <-ctx.Done():
		}
	}
}
```

Also set `result.Object = []byte(resp.Content)` on the result.

- [ ] **Step 6.1.5: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestObjectDeltaEmitted -v`
Expected: PASS.

### Task 6.2: Emit `EventElementDelta` for top-level array schemas

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/object_stream.go` (the helper added in 6.1)
- Test: `object_stream_test.go`

- [ ] **Step 6.2.1: Write the failing test**

Append to `object_stream_test.go`:

```go
func TestElementDeltaForTopLevelArray(t *testing.T) {
	provider := newCallbackProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `[{"name":"a"},`}
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `{"name":"b"},`}
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `{"name":"c"}]`}
		close(ch)
		return core.ChatResponse{
			Content:      `[{"name":"a"},{"name":"b"},{"name":"c"}]`,
			FinishReason: core.FinishStop,
		}, nil
	})

	schema := core.NewResponseSchema("items", &core.SchemaObject{
		Type:  "array",
		Items: &core.SchemaObject{Type: "object"},
	})

	a := NewLLMAgent("t", "test", provider, WithResponseSchema(schema))

	ch := make(chan core.StreamEvent, 64)
	go func() { _, _ = a.ExecuteStream(context.Background(), AgentTask{Input: "x"}, ch) }()

	elems := 0
	for ev := range ch {
		if ev.Type == core.EventElementDelta {
			elems++
		}
	}
	if elems != 3 {
		t.Errorf("EventElementDelta count = %d, want 3", elems)
	}
}
```

- [ ] **Step 6.2.2: Detect top-level array schema at agent construction**

Where the loop reads the schema, inspect whether the top-level type is `"array"`:

```go
var isArraySchema bool
if cfg.responseSchema != nil {
	var probe struct{ Type string `json:"type"` }
	_ = json.Unmarshal(cfg.responseSchema.Schema, &probe)
	isArraySchema = probe.Type == "array"
}
```

Store `isArraySchema` somewhere the forwarder can read it (e.g. on `loopState` or pass as a flag into the forwarder).

- [ ] **Step 6.2.3: Track element boundaries and emit per element**

When `isArraySchema` is true, modify the forwarder logic from 6.1 to:

- Track a depth counter that increments on `{` / `[` and decrements on `}` / `]` (skipping inside strings).
- When the depth returns to 1 after being >1 (an element of the top-level array just closed), find the byte range of that element in `buf`, parse it, and emit `EventElementDelta{Object: elementBytes}`.

(The implementation is fiddly. Use the same stack/string-state tracking from `core.PartialJSON` — extract a shared helper if it's clean.)

- [ ] **Step 6.2.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestElementDeltaForTopLevelArray -v`
Expected: PASS.

### Task 6.3: Request commit approval for Phase 6

- [ ] **Step 6.3.1: Show what would be committed**

Run: `cd /home/nezhifi/Code/LLM/oasis && git status && git diff --stat`
Expected: ~3 modified files + 1 new (`object_stream.go`) + 1 new test. Output:

> Phase 6 complete. EventObjectDelta / EventObjectFinish emitted when WithResponseSchema is set; EventElementDelta emitted for top-level array schemas. AgentResult.Object populated.
>
> Approve to commit as `feat(agent): structured object streaming events (object-delta/finish, element-delta)`?

- [ ] **Step 6.3.2: Commit only on approval**

Same pattern.

---

## Phase 7 — Typed adapters: `StreamObjectAs[T]` and `ResultObjectAs[T]`

**Goal of phase:** Free generic functions that give users typed access to the structured stream without contaminating the `Agent` interface.

### Task 7.1: Implement `ResultObjectAs[T]`

**Files:**
- Create: `/home/nezhifi/Code/LLM/oasis/agent/result_as.go`
- Test: `/home/nezhifi/Code/LLM/oasis/agent/result_as_test.go`

- [ ] **Step 7.1.1: Write the failing test**

Create `/home/nezhifi/Code/LLM/oasis/agent/result_as_test.go`:

```go
package agent

import (
	"encoding/json"
	"testing"
)

type Report struct {
	Title    string   `json:"title"`
	Sections []string `json:"sections"`
}

func TestResultObjectAsTyped(t *testing.T) {
	r := AgentResult{Object: json.RawMessage(`{"title":"Q3","sections":["x","y"]}`)}
	got, err := ResultObjectAs[Report](r)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Title != "Q3" || len(got.Sections) != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestResultObjectAsEmpty(t *testing.T) {
	r := AgentResult{}
	_, err := ResultObjectAs[Report](r)
	if err == nil {
		t.Error("expected error on empty Object")
	}
}
```

- [ ] **Step 7.1.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestResultObjectAs -v`
Expected: FAIL — `undefined: ResultObjectAs`.

- [ ] **Step 7.1.3: Implement the function**

Create `/home/nezhifi/Code/LLM/oasis/agent/result_as.go`:

```go
package agent

import (
	"encoding/json"
	"errors"
)

// ResultObjectAs decodes AgentResult.Object into a typed T. Use after
// running an agent with WithResponseSchema to get a typed final result.
//
//	type Report struct {
//	    Title string `json:"title"`
//	}
//	result, _ := agent.Execute(ctx, task)
//	report, err := oasis.ResultObjectAs[Report](result)
//
// Returns an error when r.Object is empty (agent had no schema, or the
// model produced an unparseable response) or when the JSON does not
// decode into T.
func ResultObjectAs[T any](r AgentResult) (T, error) {
	var zero T
	if len(r.Object) == 0 {
		return zero, errors.New("oasis: AgentResult.Object is empty (no schema configured, or no structured output produced)")
	}
	var v T
	if err := json.Unmarshal(r.Object, &v); err != nil {
		return zero, err
	}
	return v, nil
}
```

- [ ] **Step 7.1.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestResultObjectAs -v`
Expected: PASS.

### Task 7.2: Implement `StreamObjectAs[T]`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/result_as.go`
- Test: `result_as_test.go`

- [ ] **Step 7.2.1: Write the failing test**

Append to `result_as_test.go`:

```go
import "context"

func TestStreamObjectAsTyped(t *testing.T) {
	provider := newCallbackProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `{"title":"Q3","sections":[`}
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `"intro"`}
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `,"summary"]}`}
		close(ch)
		return core.ChatResponse{
			Content:      `{"title":"Q3","sections":["intro","summary"]}`,
			FinishReason: core.FinishStop,
		}, nil
	})

	schema := core.NewResponseSchema("Report", &core.SchemaObject{
		Type: "object",
		Properties: map[string]*core.SchemaObject{
			"title":    {Type: "string"},
			"sections": {Type: "array", Items: &core.SchemaObject{Type: "string"}},
		},
	})

	a := NewLLMAgent("t", "test", provider, WithResponseSchema(schema))
	stream := StartStream(context.Background(), a, AgentTask{Input: "x"})

	var snapshots []Report
	for partial := range StreamObjectAs[Report](stream) {
		snapshots = append(snapshots, partial)
	}
	if len(snapshots) == 0 {
		t.Fatal("expected at least one snapshot")
	}
	// Final snapshot should have the full report.
	last := snapshots[len(snapshots)-1]
	if last.Title != "Q3" || len(last.Sections) != 2 {
		t.Errorf("final snapshot: %+v", last)
	}
}
```

- [ ] **Step 7.2.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestStreamObjectAs -v`
Expected: FAIL — `undefined: StreamObjectAs`.

- [ ] **Step 7.2.3: Implement `StreamObjectAs`**

Append to `/home/nezhifi/Code/LLM/oasis/agent/result_as.go`:

```go
// StreamObjectAs subscribes to a Stream and forwards each EventObjectDelta
// (and the final EventObjectFinish) as a typed T value. The returned
// channel closes when the underlying stream finishes.
//
// Internally allocates one goroutine that reads from the stream's
// fan-out wrapper and decodes each snapshot. Failed decodes (a snapshot
// that doesn't fit T) are silently dropped — the next valid snapshot
// supersedes. The final EventObjectFinish always produces a successful
// decode (or the run ended without structured output, in which case the
// channel just closes).
//
//	for partial := range oasis.StreamObjectAs[Report](stream) {
//	    ui.Render(partial)
//	}
func StreamObjectAs[T any](s *Stream) <-chan T {
	out := make(chan T, 8)
	go func() {
		defer close(out)
		evs := s.Events()
		for ev := range evs {
			if ev.Type != core.EventObjectDelta && ev.Type != core.EventObjectFinish && ev.Type != core.EventElementDelta {
				continue
			}
			var v T
			if err := json.Unmarshal(ev.Object, &v); err != nil {
				continue
			}
			select {
			case out <- v:
			case <-s.Done():
				return
			}
		}
	}()
	return out
}
```

Adjust imports at the top of `result_as.go` to include `"github.com/nevindra/oasis/core"`:

```go
import (
	"encoding/json"
	"errors"

	"github.com/nevindra/oasis/core"
)
```

- [ ] **Step 7.2.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestStreamObjectAs -v`
Expected: PASS.

### Task 7.3: Request commit approval for Phase 7

- [ ] **Step 7.3.1: Show what would be committed**

Run: `cd /home/nezhifi/Code/LLM/oasis && git status && git diff --stat`
Expected: 2 new files. Output:

> Phase 7 complete. StreamObjectAs[T] and ResultObjectAs[T] generic adapters give typed access without contaminating Agent / Network / Workflow.
>
> Approve to commit as `feat(agent): StreamObjectAs[T] / ResultObjectAs[T] typed adapters for structured streaming`?

- [ ] **Step 7.3.2: Commit only on approval**

Same pattern.

---

## Phase 8 — Stream wrapper blocking accessors

**Goal of phase:** Add `Stream.Sources()`, `.Files()`, `.Warnings()`, `.FinishReason()`, `.ProviderMeta()`, `.SuspendPayload()`, `.Iterations()`. All block until the run completes and return the same data as the matching `AgentResult` fields.

### Task 8.1: Add the seven blocking accessors

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/stream_wrapper.go` (append after the existing `ToolResults` accessor at line ~291)
- Test: `/home/nezhifi/Code/LLM/oasis/agent/stream_wrapper_test.go` (append)

- [ ] **Step 8.1.1: Write the failing test**

Append to `/home/nezhifi/Code/LLM/oasis/agent/stream_wrapper_test.go`:

```go
func TestStreamBlockingAccessors(t *testing.T) {
	provider := newCallbackProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{
			Content:      "done",
			FinishReason: core.FinishStop,
			Warnings:     []string{"x"},
			ProviderMeta: []byte(`{"a":1}`),
		}, nil
	})
	a := NewLLMAgent("t", "test", provider)
	s := StartStream(context.Background(), a, AgentTask{Input: "x"})

	if s.FinishReason() != core.FinishStop {
		t.Errorf("FinishReason = %q", s.FinishReason())
	}
	if len(s.Warnings()) != 1 || s.Warnings()[0] != "x" {
		t.Errorf("Warnings = %v", s.Warnings())
	}
	if string(s.ProviderMeta()) != `{"a":1}` {
		t.Errorf("ProviderMeta = %s", s.ProviderMeta())
	}
	// Sources/Files default to nil for this trivial run.
	if s.Sources() != nil {
		t.Errorf("Sources should be nil, got %v", s.Sources())
	}
	if s.Files() != nil {
		t.Errorf("Files should be nil, got %v", s.Files())
	}
	if s.SuspendPayload() != nil {
		t.Errorf("SuspendPayload should be nil, got %s", s.SuspendPayload())
	}
	if len(s.Iterations()) != 1 {
		t.Errorf("Iterations len = %d", len(s.Iterations()))
	}
}
```

- [ ] **Step 8.1.2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestStreamBlockingAccessors -v`
Expected: FAIL — `undefined: s.FinishReason`.

- [ ] **Step 8.1.3: Add the accessors**

Append to `/home/nezhifi/Code/LLM/oasis/agent/stream_wrapper.go`:

```go
// FinishReason blocks until completion and returns Result().FinishReason.
func (s *Stream) FinishReason() core.FinishReason {
	r, _ := s.Result()
	return r.FinishReason
}

// Sources blocks until completion and returns Result().Sources.
func (s *Stream) Sources() []core.Source {
	r, _ := s.Result()
	return r.Sources
}

// Files blocks until completion and returns Result().Files.
func (s *Stream) Files() []core.Attachment {
	r, _ := s.Result()
	return r.Files
}

// Warnings blocks until completion and returns Result().Warnings.
func (s *Stream) Warnings() []string {
	r, _ := s.Result()
	return r.Warnings
}

// ProviderMeta blocks until completion and returns Result().ProviderMeta.
func (s *Stream) ProviderMeta() json.RawMessage {
	r, _ := s.Result()
	return r.ProviderMeta
}

// SuspendPayload blocks until completion and returns Result().SuspendPayload.
func (s *Stream) SuspendPayload() json.RawMessage {
	r, _ := s.Result()
	return r.SuspendPayload
}

// Iterations blocks until completion and returns Result().Iterations.
func (s *Stream) Iterations() []core.IterationTrace {
	r, _ := s.Result()
	return r.Iterations
}
```

Add `"encoding/json"` to imports if not already present.

- [ ] **Step 8.1.4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent -run TestStreamBlockingAccessors -v`
Expected: PASS.

### Task 8.2: Request commit approval for Phase 8

- [ ] **Step 8.2.1: Show what would be committed**

Run: `cd /home/nezhifi/Code/LLM/oasis && git status && git diff --stat`
Expected: 2 modified files. Output:

> Phase 8 complete. Stream wrapper now mirrors AgentResult — same method names, same data, blocking until done.
>
> Approve to commit as `feat(agent): Stream blocking accessors mirror AgentResult (Sources, Files, Warnings, FinishReason, ProviderMeta, SuspendPayload, Iterations)`?

- [ ] **Step 8.2.2: Commit only on approval**

Same pattern.

---

## Phase 9 — Provider populates FinishReason / ProviderMeta

**Goal of phase:** Native Gemini and OpenAI-compat providers populate `ChatResponse.FinishReason`, `.ProviderMeta`, and (where applicable) `.Warnings`. Providers that don't report a reason leave the field empty; the agent loop's existing synthesis logic fills in `FinishStop` / `FinishToolCalls` as fallback.

### Task 9.1: Update OpenAI-compat provider

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/provider/openaicompat/provider.go` (the ChatStream / parse-response site)
- Test: `/home/nezhifi/Code/LLM/oasis/provider/openaicompat/provider_test.go` (append)

- [ ] **Step 9.1.1: Find the ChatStream response-finalize site**

Run: `grep -n "ChatResponse{" /home/nezhifi/Code/LLM/oasis/provider/openaicompat/provider.go`
Expected: A small list of sites where the final `ChatResponse` is constructed at end of streaming.

- [ ] **Step 9.1.2: Write the failing test**

Append a test that drives the provider against a captured stub HTTP response containing `"finish_reason":"stop"` and asserts `resp.FinishReason == core.FinishStop`. (Use the existing test scaffolding in the file as a template.) If the test scaffolding is heavy, document the test fixture path here and write it during execution.

- [ ] **Step 9.1.3: Map OpenAI finish_reason → core.FinishReason**

Add a helper near the response-finalize site:

```go
func mapOpenAIFinishReason(s string) core.FinishReason {
	switch s {
	case "stop", "":
		return core.FinishStop
	case "tool_calls", "function_call":
		return core.FinishToolCalls
	case "length":
		return core.FinishLength
	case "content_filter":
		return core.FinishContentFilter
	default:
		return core.FinishReason(s)
	}
}
```

Use it when constructing the final `ChatResponse`:

```go
resp := core.ChatResponse{
	// existing ...
	FinishReason: mapOpenAIFinishReason(raw.Choices[0].FinishReason),
}
```

Populate `ProviderMeta` with any provider-specific extras (e.g. `system_fingerprint`):

```go
if raw.SystemFingerprint != "" {
	meta, _ := json.Marshal(map[string]string{"system_fingerprint": raw.SystemFingerprint})
	resp.ProviderMeta = meta
}
```

- [ ] **Step 9.1.4: Run tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && cd provider/openaicompat && go test ./... -v`
Expected: PASS.

### Task 9.2: Update Gemini provider

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/provider/gemini/provider.go` (or wherever `ChatResponse` is finalized)
- Test: `/home/nezhifi/Code/LLM/oasis/provider/gemini/provider_test.go` (append)

- [ ] **Step 9.2.1: Map Gemini finish reasons**

Add a helper:

```go
func mapGeminiFinishReason(s string) core.FinishReason {
	switch s {
	case "STOP", "":
		return core.FinishStop
	case "MAX_TOKENS":
		return core.FinishLength
	case "SAFETY":
		return core.FinishContentFilter
	default:
		return core.FinishReason(s)
	}
}
```

Use at the response-finalize site. Populate `ProviderMeta` with Gemini-specific data (safety ratings, thought signature length, etc.):

```go
if len(raw.SafetyRatings) > 0 {
	meta, _ := json.Marshal(map[string]any{"safety_ratings": raw.SafetyRatings})
	resp.ProviderMeta = meta
}
```

- [ ] **Step 9.2.2: Run tests**

Run: `cd /home/nezhifi/Code/LLM/oasis/provider/gemini && go test ./... -v`
Expected: PASS.

### Task 9.3: Request commit approval for Phase 9

- [ ] **Step 9.3.1: Show what would be committed**

Run: `cd /home/nezhifi/Code/LLM/oasis && git status && git diff --stat`
Expected: ~4 modified files across two satellites. Output:

> Phase 9 complete. Gemini and OpenAI-compat providers populate FinishReason and ProviderMeta. Other providers (or decorators) continue to work with zero-value fields.
>
> Approve to commit as `feat(provider): populate ChatResponse.FinishReason and ProviderMeta in Gemini and OpenAI-compat`?

- [ ] **Step 9.3.2: Commit only on approval**

Same pattern. Note: provider satellites have separate `go.mod` files; the commit may need to be split per-satellite or use a single commit covering both. Check `git status` carefully.

---

## Phase 10 — RAG retrievers implement `Sourced`

**Goal of phase:** `HybridRetriever` and `GraphRetriever` in the `rag` satellite declare their retrieved chunks as `Source` entries. The agent loop collects them onto `AgentResult.Sources`.

### Task 10.1: Implement `Sourced` on `HybridRetriever`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/rag/retriever.go` (around the `HybridRetriever` struct)
- Test: `/home/nezhifi/Code/LLM/oasis/rag/retriever_test.go`

- [ ] **Step 10.1.1: Read existing retriever shape**

Run: `Read /home/nezhifi/Code/LLM/oasis/rag/retriever.go` (lines around `HybridRetriever`, ~140-200)

- [ ] **Step 10.1.2: Add a `lastSources` field**

```go
type HybridRetriever struct {
	// existing fields ...
	mu          sync.Mutex
	lastSources []core.Source
}
```

- [ ] **Step 10.1.3: Populate after each `Retrieve`**

At the end of `(*HybridRetriever).Retrieve(...)`, build sources from the retrieved chunks and store:

```go
sources := make([]core.Source, 0, len(chunks))
for _, c := range chunks {
	meta, _ := json.Marshal(map[string]any{
		"chunk_id": c.ID,
		"score":    c.Score,
	})
	sources = append(sources, core.Source{
		URL:    c.SourceURL,   // adapt to actual chunk fields
		Title:  c.SourceTitle,
		Quote:  c.Content,
		Origin: "rag",
		Meta:   meta,
	})
}
r.mu.Lock()
r.lastSources = sources
r.mu.Unlock()
```

- [ ] **Step 10.1.4: Implement `Sources()`**

Append the method:

```go
// Sources returns the chunks cited in the most recent Retrieve call.
// Implements core.Sourced.
func (r *HybridRetriever) Sources() []core.Source {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]core.Source, len(r.lastSources))
	copy(out, r.lastSources)
	return out
}
```

- [ ] **Step 10.1.5: Repeat for `GraphRetriever`**

Same pattern in the `GraphRetriever` struct, populated from its retrieved chunks. Add `Origin: "rag"` (or `"rag:graph"` if you want to distinguish).

- [ ] **Step 10.1.6: Run tests**

Run: `cd /home/nezhifi/Code/LLM/oasis/rag && go test ./... -v`
Expected: PASS.

### Task 10.2: Collect retriever sources into `AgentResult.Sources`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/loop.go` (after each tool execution, runtime-check the tool for `core.Sourced`)
- Modify: `/home/nezhifi/Code/LLM/oasis/agent/dispatch.go` (or wherever tools are dispatched)

- [ ] **Step 10.2.1: Add a `sources []core.Source` slice to `loopState`**

```go
type loopState struct {
	// existing fields ...
	sources []core.Source
}
```

- [ ] **Step 10.2.2: After each tool dispatch, check for `Sourced`**

In the tool-dispatch path, after invoking a tool, check if the tool implements `core.Sourced` and aggregate:

```go
if sourced, ok := tool.(core.Sourced); ok {
	state.sources = append(state.sources, sourced.Sources()...)
}
```

- [ ] **Step 10.2.3: Copy onto `AgentResult.Sources`**

At result-construction sites:

```go
result.Sources = state.sources
```

- [ ] **Step 10.2.4: Run agent + rag tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./agent ./rag -v`
Expected: PASS.

### Task 10.3: Request commit approval for Phase 10

- [ ] **Step 10.3.1: Show what would be committed**

Run: `cd /home/nezhifi/Code/LLM/oasis && git status && git diff --stat`
Expected: 2 modified files in `rag/` + 2 in `agent/`. Output:

> Phase 10 complete. HybridRetriever and GraphRetriever implement core.Sourced. Agent loop aggregates Sources from any tool that opts in.
>
> Approve to commit as `feat(rag,agent): RAG retrievers declare citations via core.Sourced; agent loop collects into AgentResult.Sources`?

- [ ] **Step 10.3.2: Commit only on approval**

Same pattern.

---

## Phase 11 — Re-exports + CHANGELOG

### Task 11.1: Re-export new symbols in `oasis.go`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/oasis.go`

- [ ] **Step 11.1.1: Read current re-exports**

Run: `grep -n "type.*=.*core\.\|^var" /home/nezhifi/Code/LLM/oasis/oasis.go | head -50`
Expected: A list of type aliases to `core.*`. We need to add the new types/funcs.

- [ ] **Step 11.1.2: Add re-exports**

Append (or insert in the appropriate sections) to `/home/nezhifi/Code/LLM/oasis/oasis.go`:

```go
// Lifecycle envelope and structured streaming event types.
const (
	EventRunStart        = core.EventRunStart
	EventRunFinish       = core.EventRunFinish
	EventIterationStart  = core.EventIterationStart
	EventIterationFinish = core.EventIterationFinish
	EventObjectDelta     = core.EventObjectDelta
	EventObjectFinish    = core.EventObjectFinish
	EventElementDelta    = core.EventElementDelta
)

// FinishReason and its constants.
type FinishReason = core.FinishReason

const (
	FinishStop          = core.FinishStop
	FinishToolCalls     = core.FinishToolCalls
	FinishLength        = core.FinishLength
	FinishContentFilter = core.FinishContentFilter
	FinishHalted        = core.FinishHalted
	FinishSuspended     = core.FinishSuspended
	FinishMaxIter       = core.FinishMaxIter
	FinishError         = core.FinishError
)

// Source / Sourced / Warner.
type Source = core.Source
type Sourced = core.Sourced
type Warner = core.Warner

// Iteration / LLM call trace types.
type IterationTrace = core.IterationTrace
type LLMCallTrace = core.LLMCallTrace
type ToolCallTrace = core.ToolCallTrace

// Typed structured-output adapters.
var StreamObjectAs = agent.StreamObjectAs
```

Note: `var StreamObjectAs = agent.StreamObjectAs` won't compile because Go doesn't allow generic function variables. Instead, leave `StreamObjectAs` reachable only as `agent.StreamObjectAs[T]` — and update godoc / README to show that import. Alternatively, expose a thin wrapper:

```go
// StreamObjectAs decodes typed structured-output snapshots from a Stream.
// See agent.StreamObjectAs for details.
func StreamObjectAs[T any](s *agent.Stream) <-chan T {
	return agent.StreamObjectAs[T](s)
}

// ResultObjectAs decodes AgentResult.Object into T.
// See agent.ResultObjectAs for details.
func ResultObjectAs[T any](r AgentResult) (T, error) {
	return agent.ResultObjectAs[T](r)
}
```

(`AgentResult` is already aliased to `core.AgentResult` in `oasis.go`; the wrapper compiles.)

- [ ] **Step 11.1.3: Build and run all tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...`
Expected: PASS.

### Task 11.2: Update `CHANGELOG.md`

**Files:**
- Modify: `/home/nezhifi/Code/LLM/oasis/CHANGELOG.md`

- [ ] **Step 11.2.1: Read current `[Unreleased]` section**

Run: `Read /home/nezhifi/Code/LLM/oasis/CHANGELOG.md` (top ~40 lines)

- [ ] **Step 11.2.2: Add a streaming-world-class entry**

Insert under `[Unreleased]`:

```markdown
### Added — Streaming world-class
- **Lifecycle envelope:** every run now starts with `EventRunStart` and ends with `EventRunFinish` carrying `FinishReason`, `Warnings`, and `ProviderMeta`. Iterations are bracketed by `EventIterationStart`/`Finish`. See `docs/superpowers/specs/2026-05-21-streaming-world-class-design.md`.
- **Structured object streaming:** when `WithResponseSchema` is configured, the loop emits `EventObjectDelta` snapshots of partial JSON and `EventObjectFinish` with the final validated bytes. Top-level array schemas additionally emit one `EventElementDelta` per completed element.
- **Typed adapters:** `oasis.StreamObjectAs[T](stream)` returns a typed channel of partial-object snapshots; `oasis.ResultObjectAs[T](result)` decodes the final object. Generic free functions — no contagion of generics through `Agent` / `Network` / `Workflow`.
- **Result accessor parity:** `AgentResult` and `Stream` gain `FinishReason`, `Sources`, `Files`, `Warnings`, `ProviderMeta`, `SuspendPayload`, `Object`, `Iterations`. Same method names on both paths.
- **Per-stream observability:** new `agent.iteration` and `llm.generate` OTel spans under the existing `agent.execute` root, populated with model / temperature / max-tokens / input-tokens / output-tokens / finish-reason attributes. `AgentResult.Iterations` exposes the same data without OTel.
- **`core.Sourced` / `core.Warner`:** opt-in interfaces for tools, retrievers, and providers to declare citations and non-fatal warnings.

### Changed
- `StepTrace` is now an alias for `ToolCallTrace` (rename for naming consistency with `IterationTrace` and `LLMCallTrace`). The old name is kept; rename your variables at convenience.
- `HybridRetriever` and `GraphRetriever` implement `core.Sourced`.
- Native Gemini and OpenAI-compat providers populate `ChatResponse.FinishReason` and `ChatResponse.ProviderMeta`.

### Deprecated
- `EventInputReceived`, `EventProcessingStart`, `EventMaxIterReached`, `EventHalt` are no longer emitted. The constants remain exported for one minor release for back-compat with consumers that type-switch on them. Replace with `EventRunStart` (for the first two) and `EventRunFinish{FinishReason: ...}` (for the last two).

### Migration
- Consumers iterating events should expect `EventRunStart` as the first event and `EventRunFinish` as the last. Code that triggered on `EventMaxIterReached` or `EventHalt` should switch on `EventRunFinish.FinishReason`.
- Code calling `result.Output` continues to work; `result.Text()` is identical.
- New `AgentResult` fields are zero-value by default; existing reads are unaffected.
```

- [ ] **Step 11.2.3: Run tests one more time end-to-end**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...`
Expected: PASS across root + satellites.

### Task 11.3: Request commit approval for Phase 11

- [ ] **Step 11.3.1: Show what would be committed**

Run: `cd /home/nezhifi/Code/LLM/oasis && git status && git diff --stat`
Expected: `oasis.go` + `CHANGELOG.md`. Output:

> Phase 11 complete (final). Re-exports added; CHANGELOG updated with migration notes. Full test suite passes.
>
> Approve to commit as `docs(changelog): streaming v1 — lifecycle envelope, structured object streaming, accessor parity, per-iteration spans`?

- [ ] **Step 11.3.2: Commit only on approval**

Same pattern.

---

## Self-review checklist (executed before declaring the plan ready)

**Spec coverage:**
- §4.1 Lifecycle envelope → Phase 2 (Tasks 2.1-2.6). ✓
- §4.2 Structured object streaming → Phase 5 (parser), Phase 6 (events), Phase 7 (adapters). ✓
- §4.3 Result accessor parity → Phase 3 (populators), Phase 8 (Stream accessors), Phase 10 (Sources via RAG). ✓
- §4.4 Per-stream observability → Phase 4. ✓
- §4.5 Provider plumbing → Phase 9. ✓
- §5 Affected packages — every row covered across phases. ✓
- §7 Migration — Phase 11.2 covers the CHANGELOG. ✓
- §8 Testing strategy — each phase has unit tests; Phase 5 has the property test; the partial-parse property test covers the "any byte prefix yields valid JSON or nothing" invariant. ✓

**Placeholder scan:** Two tasks use `t.Skip(...)` placeholders (2.4.1 subagent envelope test, 3.4.4 file aggregation test, 3.2.2 suspend test). Each calls out the test-harness it should reuse during execution — acceptable for tests that depend on existing in-package helpers I cannot fully resolve without reading more code. The implementation steps are concrete.

**Type consistency:**
- `FinishReason` referenced in: Task 1.1 (defined), Task 1.3 (StreamEvent field), Task 1.4 (ChatResponse), Task 1.6 (LLMCallTrace), Task 1.7 (AgentResult), Task 2.2 (finalizeRun), Task 3.1 (population), Task 4.2 (span attr), Task 8.1 (Stream.FinishReason), Task 9.1 (mapping), Task 11.1 (re-export). ✓
- `IterationTrace` referenced in: Task 1.6 (defined), Task 1.7 (AgentResult.Iterations), Task 4.3 (population), Task 8.1 (Stream.Iterations), Task 11.1 (re-export). ✓
- `Source` / `Sourced`: Task 1.5 (defined), Task 8.1 (Stream.Sources), Task 10.1 (RAG impl), Task 10.2 (collection), Task 11.1 (re-export). ✓
- `PartialJSON`: Task 5.1 (defined), Task 6.1 (used). ✓
- `StreamObjectAs[T]`, `ResultObjectAs[T]`: Task 7.1 (defined), Task 7.2 (defined), Task 11.1 (re-export). ✓

No name drift detected.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-21-streaming-world-class-plan.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration. Good fit for this plan because the phases are clearly bounded and each task is independently testable.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints. Good fit if you prefer to see every step live in this conversation.

Which approach?
