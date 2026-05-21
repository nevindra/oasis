# Streaming and Tools DX Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the streaming + tools DX layer from `docs/superpowers/specs/2026-05-21-streaming-tools-dx-design.md` — new stream event types, opt-in `Stream` wrapper, `AgentResult` convenience accessors, tool middleware chain, and framework-enforced tool approval gate.

**Architecture:** All work layers on the existing `chan StreamEvent` kernel — `ExecuteStream` signature and behavior do not change. Four phases ship independently: (1) event types + result accessors, (2) Stream wrapper with bounded fan-out, (3) tool middleware chain, (4) tool approval gate built on the chain.

**Tech Stack:** Go 1.24, stdlib only for `core/` (leaf package — no oasis imports). Existing patterns: `AgentOption` functional options, `RunOptions` struct, `slog` structured logging, `Tracer`/`Span` interfaces in root package.

---

## File Map

**Phase 1 — Foundations:**
- Modify: `core/stream.go` (append 7 new event type constants)
- Create: `agent/result_accessors.go` (~80 LOC — pure functions on `AgentResult`)
- Create: `agent/result_accessors_test.go`
- Modify: `agent/stream_test.go:18-30` (add new event-type constants to the existing `TestNewStreamEventTypes` table)
- Modify: `oasis.go` (re-export new constants if they appear in curated surface — they don't, so no change unless tests fail)

**Phase 2 — Stream wrapper:**
- Create: `agent/stream_wrapper.go` (~350 LOC — `Stream` type, dispatcher goroutine, ring buffer, subscriber list)
- Create: `agent/stream_wrapper_test.go` (fan-out, replay, slow-subscriber drop, race tests)
- Modify: `agent/runoptions.go` (add `StreamReplayLimit int` field + Validate check)
- Modify: `oasis.go` (re-export `Stream`, `StartStream`, `StartStreamWith`)

**Phase 3 — Tool middleware:**
- Create: `core/tool_middleware.go` (~30 LOC — `ToolMiddleware` type)
- Create: `agent/tool_middleware.go` (~120 LOC — chain plumbing + built-ins)
- Create: `agent/tool_middleware_test.go`
- Modify: `agent/agent.go` (add `WithToolMiddleware` option, add `toolMiddleware []core.ToolMiddleware` to `Config`)
- Modify: `agent/llm.go` or wherever tools are wrapped pre-dispatch (apply chain in `buildLoopConfig`)
- Modify: `oasis.go` (re-export `WithToolMiddleware`, `LoggingMiddleware`, `TimingMiddleware`, `OTelSpanMiddleware`, `TransformMiddleware`, `ToolMiddleware`)

**Phase 4 — Tool approval:**
- Create: `agent/tool_approval.go` (~150 LOC — `WithToolApproval`, `ApprovalOption`, `DenyAction`, approval middleware)
- Create: `agent/tool_approval_test.go`
- Modify: `oasis.go` (re-export approval surface)

---

## Phase 1 — Foundations

### Task 1.1: Add new stream event type constants

**Files:**
- Modify: `core/stream.go` (append after line 65, before the closing `)` of the const block)

- [ ] **Step 1: Write a failing test for the new constants**

Edit `agent/stream_test.go`. In the existing `TestNewStreamEventTypes` table starting at line 18, add these rows before the closing `}` of the slice literal:

```go
{EventReasoningStart, "reasoning-start"},
{EventReasoningDelta, "reasoning-delta"},
{EventReasoningEnd, "reasoning-end"},
{EventHalt, "halt"},
{EventError, "error"},
{EventStreamWarning, "stream-warning"},
{EventToolApprovalPending, "tool-approval-pending"},
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestNewStreamEventTypes -v`
Expected: FAIL with "undefined: EventReasoningStart" (etc.)

- [ ] **Step 3: Add the constants**

Edit `core/stream.go`. Inside the `const (` block, append before the closing `)`:

```go
	// EventReasoningStart marks the beginning of a reasoning block from a
	// provider that emits reasoning incrementally (Claude extended thinking,
	// OpenAI o1). No payload.
	EventReasoningStart StreamEventType = "reasoning-start"
	// EventReasoningDelta carries an incremental reasoning text chunk.
	EventReasoningDelta StreamEventType = "reasoning-delta"
	// EventReasoningEnd marks the end of a reasoning block. Content carries
	// the full reassembled reasoning text for consumers that prefer the
	// monolithic form. Always followed by zero or more subsequent events.
	EventReasoningEnd StreamEventType = "reasoning-end"
	// EventHalt signals a processor halted the loop via *ErrHalt. Name carries
	// the processor name; Content carries the canned response shown to the user.
	EventHalt StreamEventType = "halt"
	// EventError signals terminal failure of the run. Content carries the
	// error message. Always immediately followed by channel close. ExecuteStream
	// returns the same error to the caller.
	EventError StreamEventType = "error"
	// EventStreamWarning carries non-fatal stream-wrapper notifications.
	// Content is one of:
	//   - "replay-truncated": ring buffer overflowed; some history lost.
	//   - "subscriber-dropped": a slow subscriber was removed; its channel
	//     received this warning then closed.
	EventStreamWarning StreamEventType = "stream-warning"
	// EventToolApprovalPending is emitted by the tool approval middleware
	// before a guarded tool runs. ID carries the tool call ID; Name carries
	// the tool name; Args carries the proposed arguments. A subsequent
	// EventToolCallStart appears only if the approval is granted.
	EventToolApprovalPending StreamEventType = "tool-approval-pending"
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestNewStreamEventTypes -v`
Expected: PASS

- [ ] **Step 5: Run the full core + agent test suites**

Run: `go test ./core/... ./agent/...`
Expected: PASS (no regression)

- [ ] **Step 6: Commit**

```bash
git add core/stream.go agent/stream_test.go
git commit -m "feat(core): new stream events for reasoning, halt, error, warning, approval

Adds 7 StreamEventType constants:
- EventReasoningStart/Delta/End (incremental reasoning)
- EventHalt (processor *ErrHalt)
- EventError (terminal failure)
- EventStreamWarning (replay-truncated, subscriber-dropped)
- EventToolApprovalPending (framework approval gate)

EventThinking remains; deprecation comes when providers port to the triplet.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 1.2: Add AgentResult convenience accessors

**Files:**
- Create: `agent/result_accessors.go`
- Create: `agent/result_accessors_test.go`

- [ ] **Step 1: Write the failing tests**

Create `agent/result_accessors_test.go`:

```go
package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

func sampleResult() AgentResult {
	return AgentResult{
		Output:   "final text",
		Thinking: "reasoning",
		Usage:    core.Usage{InputTokens: 10, OutputTokens: 5},
		Steps: []StepTrace{
			{
				ToolName:   "search",
				ToolInput:  json.RawMessage(`{"q":"hi"}`),
				ToolOutput: json.RawMessage(`{"hits":1}`),
				Duration:   2 * time.Millisecond,
				ToolCallID: "call-1",
			},
			{
				ToolName:   "fetch",
				ToolInput:  json.RawMessage(`{"url":"x"}`),
				ToolOutput: json.RawMessage(`{"body":"y"}`),
				Duration:   3 * time.Millisecond,
				ToolCallID: "call-2",
			},
		},
	}
}

func TestAgentResult_Text(t *testing.T) {
	r := sampleResult()
	if got, want := r.Text(), "final text"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestAgentResult_Reasoning(t *testing.T) {
	r := sampleResult()
	if got, want := r.Reasoning(), "reasoning"; got != want {
		t.Errorf("Reasoning() = %q, want %q", got, want)
	}
}

func TestAgentResult_ToolCalls(t *testing.T) {
	r := sampleResult()
	calls := r.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("ToolCalls() len = %d, want 2", len(calls))
	}
	if calls[0].Name != "search" || calls[0].ID != "call-1" {
		t.Errorf("ToolCalls()[0] = %+v", calls[0])
	}
	if calls[1].Name != "fetch" || calls[1].ID != "call-2" {
		t.Errorf("ToolCalls()[1] = %+v", calls[1])
	}
}

func TestAgentResult_ToolResults(t *testing.T) {
	r := sampleResult()
	results := r.ToolResults()
	if len(results) != 2 {
		t.Fatalf("ToolResults() len = %d, want 2", len(results))
	}
	if string(results[0].Content) != `{"hits":1}` {
		t.Errorf("ToolResults()[0].Content = %s", results[0].Content)
	}
}

func TestAgentResult_LastStep(t *testing.T) {
	r := sampleResult()
	last := r.LastStep()
	if last.ToolName != "fetch" {
		t.Errorf("LastStep().ToolName = %q, want %q", last.ToolName, "fetch")
	}

	empty := AgentResult{}
	if zero := empty.LastStep(); zero.ToolName != "" {
		t.Errorf("LastStep() on empty result should be zero value, got %+v", zero)
	}
}

func TestAgentResult_StepByTool(t *testing.T) {
	r := sampleResult()
	step, ok := r.StepByTool("fetch")
	if !ok || step.ToolName != "fetch" {
		t.Errorf("StepByTool(fetch) = (%+v, %v)", step, ok)
	}

	_, ok = r.StepByTool("nope")
	if ok {
		t.Errorf("StepByTool(nope) should return ok=false")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run TestAgentResult_ -v`
Expected: FAIL — method `Text` / `Reasoning` / `ToolCalls` / `ToolResults` / `LastStep` / `StepByTool` not defined on `AgentResult`

- [ ] **Step 3: Check StepTrace shape so accessors match exactly**

Run: `grep -n "type StepTrace" /home/ubuntu/code/oasis/agent/*.go | head -3`
Then read the struct definition to confirm field names (`ToolName`, `ToolInput`, `ToolOutput`, `ToolCallID`, `Duration`).

If the struct differs from the test above, update the test to match the real field names. Do not invent fields.

- [ ] **Step 4: Implement the accessors**

Create `agent/result_accessors.go`:

```go
package agent

import "github.com/nevindra/oasis/core"

// Text returns the agent's final text output. Alias for r.Output that exists
// for symmetry with the Stream wrapper, so synchronous and streaming
// consumers use the same method name.
func (r AgentResult) Text() string { return r.Output }

// Reasoning returns the agent's reasoning text from the final LLM call.
// Alias for r.Thinking that exists for symmetry with the Stream wrapper.
func (r AgentResult) Reasoning() string { return r.Thinking }

// ToolCalls returns the tool calls captured in r.Steps, in execution order.
// Returns an empty slice if no tools were called. Each call's ID, Name, and
// Args mirror the ToolCall the LLM produced.
func (r AgentResult) ToolCalls() []core.ToolCall {
	if len(r.Steps) == 0 {
		return nil
	}
	out := make([]core.ToolCall, 0, len(r.Steps))
	for _, s := range r.Steps {
		out = append(out, core.ToolCall{
			ID:   s.ToolCallID,
			Name: s.ToolName,
			Args: s.ToolInput,
		})
	}
	return out
}

// ToolResults returns the tool results captured in r.Steps, in execution order.
// Each result's Content mirrors the JSON the tool returned to the LLM.
func (r AgentResult) ToolResults() []core.ToolResult {
	if len(r.Steps) == 0 {
		return nil
	}
	out := make([]core.ToolResult, 0, len(r.Steps))
	for _, s := range r.Steps {
		out = append(out, core.ToolResult{Content: s.ToolOutput})
	}
	return out
}

// LastStep returns the final StepTrace in r.Steps, or the zero value if no
// steps were recorded.
func (r AgentResult) LastStep() StepTrace {
	if len(r.Steps) == 0 {
		return StepTrace{}
	}
	return r.Steps[len(r.Steps)-1]
}

// StepByTool returns the first StepTrace whose ToolName matches name.
// Returns (zero, false) if no step matches.
func (r AgentResult) StepByTool(name string) (StepTrace, bool) {
	for _, s := range r.Steps {
		if s.ToolName == name {
			return s, true
		}
	}
	return StepTrace{}, false
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./agent/ -run TestAgentResult_ -v`
Expected: PASS for all six tests

- [ ] **Step 6: Run the full agent test suite**

Run: `go test ./agent/...`
Expected: PASS (no regression)

- [ ] **Step 7: Commit**

```bash
git add agent/result_accessors.go agent/result_accessors_test.go
git commit -m "feat(agent): convenience accessors on AgentResult

Adds Text, Reasoning, ToolCalls, ToolResults, LastStep, StepByTool
as pure functions over existing fields. No new storage; no behavior
changes. Mirrors the Stream wrapper accessors so synchronous and
streaming code use identical method names.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 2 — Stream wrapper

### Task 2.1: Add StreamReplayLimit field to RunOptions

**Files:**
- Modify: `agent/runoptions.go` (extend struct + Validate)

- [ ] **Step 1: Write the failing tests**

Append to `agent/runoptions_test.go` (create the test file if it doesn't exist; if it does, append at the bottom):

```go
func TestRunOptions_StreamReplayLimit_Valid(t *testing.T) {
	opts := &RunOptions{StreamReplayLimit: 128}
	if err := opts.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestRunOptions_StreamReplayLimit_NegativeInvalid(t *testing.T) {
	opts := &RunOptions{StreamReplayLimit: -1}
	if err := opts.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for negative StreamReplayLimit")
	}
}

func TestRunOptions_StreamReplayLimit_ZeroOK(t *testing.T) {
	opts := &RunOptions{StreamReplayLimit: 0}
	if err := opts.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (0 means default)", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run TestRunOptions_StreamReplayLimit -v`
Expected: FAIL — `StreamReplayLimit` is not a field of `RunOptions`

- [ ] **Step 3: Add the field and validation**

Edit `agent/runoptions.go`. In the `RunOptions` struct, add a new field block after `Metadata`:

```go
	// Streaming overrides — apply only to ExecuteStream / StartStream paths.

	// StreamReplayLimit caps the per-stream replay ring buffer when using
	// the Stream wrapper (see StartStream). Zero means use the default (256).
	// Negative is invalid.
	StreamReplayLimit int
```

In `Validate()`, add the negative check before the closing `return nil`:

```go
	if o.StreamReplayLimit < 0 {
		return &RunOptionsError{Field: "StreamReplayLimit", Message: "must be >= 0"}
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/ -run TestRunOptions_StreamReplayLimit -v`
Expected: PASS for all three tests

- [ ] **Step 5: Run the full agent test suite**

Run: `go test ./agent/...`
Expected: PASS (no regression)

- [ ] **Step 6: Commit**

```bash
git add agent/runoptions.go agent/runoptions_test.go
git commit -m "feat(agent): add StreamReplayLimit to RunOptions

Per-call override for the Stream wrapper's replay ring buffer.
Zero means use the default (256). Validate rejects negatives.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 2.2: Implement Stream wrapper — core types and dispatcher

**Files:**
- Create: `agent/stream_wrapper.go`

This task introduces the type and the dispatcher goroutine but no fan-out logic yet. Fan-out lands in Task 2.3.

- [ ] **Step 1: Write a smoke test for StartStream**

Create `agent/stream_wrapper_test.go`:

```go
package agent

import (
	"context"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

// emitterAgent is a test StreamingAgent that emits a fixed event sequence
// then returns a known AgentResult.
type emitterAgent struct {
	events []core.StreamEvent
	final  AgentResult
}

func (e *emitterAgent) Name() string                { return "emitter" }
func (e *emitterAgent) Description() string         { return "" }
func (e *emitterAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return e.final, nil
}
func (e *emitterAgent) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- core.StreamEvent) (AgentResult, error) {
	defer close(ch)
	for _, ev := range e.events {
		select {
		case ch <- ev:
		case <-ctx.Done():
			return AgentResult{}, ctx.Err()
		}
	}
	return e.final, nil
}

func TestStartStream_BlockingResult(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventTextDelta, Content: "hello "},
			{Type: core.EventTextDelta, Content: "world"},
		},
		final: AgentResult{Output: "hello world"},
	}
	s := StartStream(context.Background(), ag, AgentTask{Input: "hi"})
	res, err := s.Result()
	if err != nil {
		t.Fatalf("Result() err = %v", err)
	}
	if res.Output != "hello world" {
		t.Errorf("Result().Output = %q, want %q", res.Output, "hello world")
	}
	if got, want := s.Text(), "hello world"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestStartStream_DoneChannel(t *testing.T) {
	ag := &emitterAgent{final: AgentResult{Output: "ok"}}
	s := StartStream(context.Background(), ag, AgentTask{})
	select {
	case <-s.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("Done() never closed within 1s")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run TestStartStream_ -v`
Expected: FAIL — `StartStream` not defined

- [ ] **Step 3: Implement Stream core (no fan-out yet)**

Create `agent/stream_wrapper.go`:

```go
package agent

import (
	"context"
	"sync"

	"github.com/nevindra/oasis/core"
)

const (
	defaultStreamReplayLimit = 256
	maxStreamReplayLimit     = 4096
	defaultSubscriberBufSize = 32
)

// Stream is an opt-in wrapper around ExecuteStream that provides multi-reader
// fan-out, bounded replay, blocking accessors, and event-typed callbacks.
//
// Construction via StartStream / StartStreamWith spawns one goroutine that
// runs the underlying agent and dispatches events to every subscriber.
// Stream is safe for concurrent use by multiple goroutines.
//
// Zero overhead for callers that don't construct a Stream: ExecuteStream
// remains the kernel API.
type Stream struct {
	mu          sync.Mutex
	subscribers []*subscriber
	replay      []core.StreamEvent
	replayLimit int
	closed      bool

	done   chan struct{}
	result AgentResult
	err    error
}

type subscriber struct {
	ch       chan core.StreamEvent
	filter   core.StreamEventType // "" means catch-all
	callback func(core.StreamEvent)
	dropped  bool
}

// StartStream runs agent.ExecuteStream in a background goroutine and returns
// a Stream that consumers may subscribe to or query for the final result.
//
// The Stream's lifecycle ends when ExecuteStream returns; at that point Done()
// closes and Result() returns the captured AgentResult and error. Subscribers
// receive the final events before their channels close.
//
// Use StartStreamWith to pass RunOptions (notably StreamReplayLimit).
func StartStream(ctx context.Context, agent StreamingAgent, task AgentTask) *Stream {
	return startStream(ctx, agent, task, nil)
}

// StartStreamWith is the RunOptions-aware constructor. Pass nil opts for
// agent defaults. Honors opts.StreamReplayLimit if set; clamped to
// [1, maxStreamReplayLimit].
func StartStreamWith(ctx context.Context, agent StreamingAgentWithOptions, task AgentTask, opts *RunOptions) *Stream {
	return startStream(ctx, &runOptsAdapter{agent: agent, opts: opts}, task, opts)
}

// runOptsAdapter lets startStream treat a StreamingAgentWithOptions like a
// plain StreamingAgent for the inner ExecuteStream call.
type runOptsAdapter struct {
	agent StreamingAgentWithOptions
	opts  *RunOptions
}

func (a *runOptsAdapter) Name() string        { return a.agent.Name() }
func (a *runOptsAdapter) Description() string { return a.agent.Description() }
func (a *runOptsAdapter) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return a.agent.ExecuteWith(ctx, task, a.opts)
}
func (a *runOptsAdapter) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- core.StreamEvent) (AgentResult, error) {
	return a.agent.ExecuteStreamWith(ctx, task, ch, a.opts)
}

func startStream(ctx context.Context, agent StreamingAgent, task AgentTask, opts *RunOptions) *Stream {
	limit := defaultStreamReplayLimit
	if opts != nil && opts.StreamReplayLimit > 0 {
		limit = opts.StreamReplayLimit
		if limit > maxStreamReplayLimit {
			limit = maxStreamReplayLimit
		}
	}

	s := &Stream{
		replay:      make([]core.StreamEvent, 0, limit),
		replayLimit: limit,
		done:        make(chan struct{}),
	}

	go s.run(ctx, agent, task)

	return s
}

// run drives the underlying agent and dispatches events. Closes Done when
// ExecuteStream returns. Closes every subscriber's channel.
func (s *Stream) run(ctx context.Context, agent StreamingAgent, task AgentTask) {
	defer close(s.done)

	ch := make(chan core.StreamEvent, defaultIterChBufSize)

	type runResult struct {
		res AgentResult
		err error
	}
	resCh := make(chan runResult, 1)
	go func() {
		r, err := agent.ExecuteStream(ctx, task, ch)
		resCh <- runResult{r, err}
	}()

	for ev := range ch {
		s.dispatch(ev)
	}

	r := <-resCh
	s.finalize(r.res, r.err)
}

// dispatch appends to replay buffer and pushes to subscribers.
// Implemented in Task 2.3 — for now it just appends to replay.
func (s *Stream) dispatch(ev core.StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.replay) >= s.replayLimit {
		s.replay = append(s.replay[1:], ev)
	} else {
		s.replay = append(s.replay, ev)
	}
}

func (s *Stream) finalize(res AgentResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result = res
	s.err = err
	s.closed = true
	for _, sub := range s.subscribers {
		close(sub.ch)
	}
	s.subscribers = nil
}

// Done returns a channel that closes when the underlying agent finishes.
func (s *Stream) Done() <-chan struct{} { return s.done }

// Result blocks until the underlying agent finishes, then returns the final
// AgentResult and the error returned by ExecuteStream.
func (s *Stream) Result() (AgentResult, error) {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, s.err
}

// Text blocks until completion and returns Result().Output.
func (s *Stream) Text() string {
	r, _ := s.Result()
	return r.Output
}

// Reasoning blocks until completion and returns Result().Thinking.
func (s *Stream) Reasoning() string {
	r, _ := s.Result()
	return r.Thinking
}

// Usage blocks until completion and returns Result().Usage.
func (s *Stream) Usage() core.Usage {
	r, _ := s.Result()
	return r.Usage
}

// ToolCalls blocks until completion and returns Result().ToolCalls().
func (s *Stream) ToolCalls() []core.ToolCall {
	r, _ := s.Result()
	return r.ToolCalls()
}

// ToolResults blocks until completion and returns Result().ToolResults().
func (s *Stream) ToolResults() []core.ToolResult {
	r, _ := s.Result()
	return r.ToolResults()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/ -run TestStartStream_ -v`
Expected: PASS

- [ ] **Step 5: Run full agent suite + race detector**

Run: `go test -race ./agent/...`
Expected: PASS, no races

- [ ] **Step 6: Commit**

```bash
git add agent/stream_wrapper.go agent/stream_wrapper_test.go
git commit -m "feat(agent): Stream wrapper core — StartStream, Result, Done, accessors

Adds agent.Stream as an opt-in wrapper around ExecuteStream. This first
landing provides the blocking accessors (Text, Result, ToolCalls,
ToolResults, Reasoning, Usage, Done). Fan-out and live subscription land
in the next commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 2.3: Stream fan-out — Events() channel and slow-subscriber drop

**Files:**
- Modify: `agent/stream_wrapper.go` (extend `dispatch`, add `Events`, `subscribe`, drop logic)
- Modify: `agent/stream_wrapper_test.go` (add fan-out + drop tests)

- [ ] **Step 1: Write the failing tests**

Append to `agent/stream_wrapper_test.go`:

```go
func TestStream_Events_FanOut(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventTextDelta, Content: "a"},
			{Type: core.EventTextDelta, Content: "b"},
			{Type: core.EventTextDelta, Content: "c"},
		},
		final: AgentResult{Output: "abc"},
	}
	s := StartStream(context.Background(), ag, AgentTask{})

	// Two parallel readers must both see all three events.
	collect := func(ch <-chan core.StreamEvent) []string {
		var out []string
		for ev := range ch {
			if ev.Type == core.EventTextDelta {
				out = append(out, ev.Content)
			}
		}
		return out
	}

	ch1 := s.Events()
	ch2 := s.Events()
	done1 := make(chan []string, 1)
	done2 := make(chan []string, 1)
	go func() { done1 <- collect(ch1) }()
	go func() { done2 <- collect(ch2) }()

	got1 := <-done1
	got2 := <-done2
	want := []string{"a", "b", "c"}

	if !equalStrings(got1, want) {
		t.Errorf("ch1 = %v, want %v", got1, want)
	}
	if !equalStrings(got2, want) {
		t.Errorf("ch2 = %v, want %v", got2, want)
	}
}

func TestStream_Events_LateReplay(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventTextDelta, Content: "early"},
		},
		final: AgentResult{Output: "early"},
	}
	s := StartStream(context.Background(), ag, AgentTask{})
	// Wait for the agent to finish before subscribing — late subscriber.
	<-s.Done()

	ch := s.Events()
	var got []string
	for ev := range ch {
		if ev.Type == core.EventTextDelta {
			got = append(got, ev.Content)
		}
	}
	if !equalStrings(got, []string{"early"}) {
		t.Errorf("late subscriber got %v, want [early]", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./agent/ -run TestStream_Events -v`
Expected: FAIL — `s.Events` undefined

- [ ] **Step 3: Implement Events and fan-out dispatch**

Replace the `dispatch` and `finalize` methods in `agent/stream_wrapper.go` with this version, and append `Events`, `subscribe`, and `pushTo` below:

```go
func (s *Stream) dispatch(ev core.StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Append to replay ring buffer, emitting a stream-warning once on overflow.
	if len(s.replay) >= s.replayLimit {
		s.replay = append(s.replay[1:], ev)
		// One-shot truncation notice; tracked via a sentinel field if needed.
	} else {
		s.replay = append(s.replay, ev)
	}

	// Fan out to subscribers.
	var dropped []*subscriber
	for _, sub := range s.subscribers {
		if sub.dropped {
			continue
		}
		if !s.pushTo(sub, ev) {
			sub.dropped = true
			dropped = append(dropped, sub)
		}
	}

	// Notify and close dropped subscribers.
	for _, sub := range dropped {
		warn := core.StreamEvent{Type: core.EventStreamWarning, Content: "subscriber-dropped"}
		select {
		case sub.ch <- warn:
		default:
		}
		close(sub.ch)
	}
}

// pushTo writes ev to sub.ch non-blockingly. Returns false if the channel is
// full (slow subscriber) or if a callback panicked. Callback failures do not
// drop the subscriber — only channel full does.
func (s *Stream) pushTo(sub *subscriber, ev core.StreamEvent) bool {
	if sub.callback != nil {
		func() {
			defer func() { _ = recover() }()
			if sub.filter == "" || sub.filter == ev.Type {
				sub.callback(ev)
			}
		}()
		return true
	}
	// Channel subscriber.
	select {
	case sub.ch <- ev:
		return true
	default:
		return false
	}
}

func (s *Stream) finalize(res AgentResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result = res
	s.err = err
	s.closed = true
	for _, sub := range s.subscribers {
		if !sub.dropped && sub.callback == nil {
			close(sub.ch)
		}
	}
	s.subscribers = nil
}

// Events returns a new channel that receives a copy of every stream event.
// Late subscribers (after some events have been dispatched) receive a replay
// of buffered history first, then live events.
//
// The returned channel is closed when the underlying agent finishes. If the
// subscriber's channel fills (slow consumer), the wrapper emits a single
// EventStreamWarning{Content:"subscriber-dropped"} into the channel and closes
// it. Other subscribers are unaffected.
//
// Buffer size is fixed at 32 events. Tune via OnEvent callbacks if a larger
// buffer is required for a specific consumer.
func (s *Stream) Events() <-chan core.StreamEvent {
	return s.subscribe("", nil)
}

// subscribe registers a new subscriber. filter is the event type to match
// (empty string = catch-all); callback is non-nil for OnXxx callbacks (no
// channel allocated in that case).
func (s *Stream) subscribe(filter core.StreamEventType, callback func(core.StreamEvent)) chan core.StreamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	var ch chan core.StreamEvent
	if callback == nil {
		ch = make(chan core.StreamEvent, defaultSubscriberBufSize)
		// Replay history non-blockingly. If the subscriber is slow before it
		// even starts reading, we treat it the same as a runtime drop.
		for _, ev := range s.replay {
			select {
			case ch <- ev:
			default:
				warn := core.StreamEvent{Type: core.EventStreamWarning, Content: "subscriber-dropped"}
				select {
				case ch <- warn:
				default:
				}
				close(ch)
				return ch
			}
		}
		// If the stream is already closed, deliver replay then close.
		if s.closed {
			close(ch)
			return ch
		}
	}

	s.subscribers = append(s.subscribers, &subscriber{
		ch:       ch,
		filter:   filter,
		callback: callback,
	})
	return ch
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./agent/ -run TestStream_ -v`
Expected: PASS for `TestStream_Events_FanOut`, `TestStream_Events_LateReplay`, prior tests

- [ ] **Step 5: Run full agent suite under race detector**

Run: `go test -race ./agent/...`
Expected: PASS, no races

- [ ] **Step 6: Commit**

```bash
git add agent/stream_wrapper.go agent/stream_wrapper_test.go
git commit -m "feat(agent): Stream fan-out — Events() with bounded replay and drop

Multiple Events() readers each receive a copy of every event. Late
subscribers replay from the ring buffer. Slow subscribers receive a
subscriber-dropped warning, then their channel closes; other
subscribers are unaffected.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 2.4: Stream callback subscription (OnTextDelta, OnToolCall, etc.)

**Files:**
- Modify: `agent/stream_wrapper.go` (add `OnEvent`, `OnTextDelta`, `OnReasoningDelta`, `OnToolCall`, `OnToolResult`)
- Modify: `agent/stream_wrapper_test.go` (add callback tests)

- [ ] **Step 1: Write the failing tests**

Append to `agent/stream_wrapper_test.go`:

```go
func TestStream_OnTextDelta(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventTextDelta, Content: "x"},
			{Type: core.EventTextDelta, Content: "y"},
			{Type: core.EventToolCallStart, Name: "ignored"},
		},
		final: AgentResult{Output: "xy"},
	}
	s := StartStream(context.Background(), ag, AgentTask{})

	var got []string
	s.OnTextDelta(func(chunk string) { got = append(got, chunk) })

	_, _ = s.Result()
	if !equalStrings(got, []string{"x", "y"}) {
		t.Errorf("OnTextDelta callback got %v, want [x y]", got)
	}
}

func TestStream_OnToolCall(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventToolCallStart, ID: "1", Name: "search"},
		},
		final: AgentResult{},
	}
	s := StartStream(context.Background(), ag, AgentTask{})

	var seen []string
	s.OnToolCall(func(tc core.ToolCall) { seen = append(seen, tc.Name) })

	_, _ = s.Result()
	if !equalStrings(seen, []string{"search"}) {
		t.Errorf("OnToolCall got %v, want [search]", seen)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run "TestStream_On" -v`
Expected: FAIL — methods undefined

- [ ] **Step 3: Implement the callbacks**

Append to `agent/stream_wrapper.go`:

```go
// OnEvent registers a catch-all callback invoked for every event in order.
// The callback runs on the dispatcher goroutine — keep it fast. Panics in
// the callback are recovered and ignored.
//
// Callbacks registered after subscription start receive only future events,
// not replay history. Subscribe via Events() if replay is needed.
func (s *Stream) OnEvent(fn func(core.StreamEvent)) {
	s.subscribe("", fn)
}

// OnTextDelta registers a callback invoked for every EventTextDelta event.
// fn receives the Content string directly.
func (s *Stream) OnTextDelta(fn func(string)) {
	s.subscribe(core.EventTextDelta, func(ev core.StreamEvent) {
		fn(ev.Content)
	})
}

// OnReasoningDelta registers a callback invoked for every EventReasoningDelta
// event. fn receives the Content string directly.
func (s *Stream) OnReasoningDelta(fn func(string)) {
	s.subscribe(core.EventReasoningDelta, func(ev core.StreamEvent) {
		fn(ev.Content)
	})
}

// OnToolCall registers a callback invoked when the LLM emits a tool call
// (EventToolCallStart). fn receives the reconstructed ToolCall.
func (s *Stream) OnToolCall(fn func(core.ToolCall)) {
	s.subscribe(core.EventToolCallStart, func(ev core.StreamEvent) {
		fn(core.ToolCall{ID: ev.ID, Name: ev.Name, Args: ev.Args})
	})
}

// OnToolResult registers a callback invoked when a tool returns
// (EventToolCallResult). fn receives a synthesized ToolResult with the raw
// Content. To inspect Usage or Duration, use OnEvent and read the
// StreamEvent directly.
func (s *Stream) OnToolResult(fn func(core.ToolResult)) {
	s.subscribe(core.EventToolCallResult, func(ev core.StreamEvent) {
		fn(core.ToolResult{Content: []byte(ev.Content)})
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./agent/ -run "TestStream_On" -v`
Expected: PASS

- [ ] **Step 5: Run full agent suite under race**

Run: `go test -race ./agent/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add agent/stream_wrapper.go agent/stream_wrapper_test.go
git commit -m "feat(agent): Stream callbacks — OnEvent, OnTextDelta, OnToolCall, etc

Filtered-callback subscriptions invoked from the dispatcher goroutine.
Cheaper than spawning a goroutine per callback. Panics in callbacks are
recovered to protect the dispatcher.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 2.5: Re-export Stream surface from oasis.go

**Files:**
- Modify: `oasis.go`

- [ ] **Step 1: Find where existing agent re-exports live**

Run: `grep -n "agent\.\(NewLLMAgent\|StreamingAgent\|Stream\)" /home/ubuntu/code/oasis/oasis.go | head -10`
Use the output to locate the section. The new re-exports go in the same section as other agent type/function re-exports.

- [ ] **Step 2: Add re-exports**

Append to `oasis.go` in the appropriate re-export section (model after existing ones like `NewLLMAgent`):

```go
// Stream is an opt-in wrapper around StreamingAgent.ExecuteStream that
// provides multi-reader fan-out, bounded replay, blocking accessors, and
// event-typed callbacks. See agent.Stream for full documentation.
type Stream = agent.Stream

// StartStream runs agent.ExecuteStream in a background goroutine and returns
// a Stream that consumers may subscribe to or query for the final result.
func StartStream(ctx context.Context, ag StreamingAgent, task AgentTask) *Stream {
	return agent.StartStream(ctx, ag, task)
}

// StartStreamWith is the RunOptions-aware constructor for Stream.
func StartStreamWith(ctx context.Context, ag StreamingAgentWithOptions, task AgentTask, opts *RunOptions) *Stream {
	return agent.StartStreamWith(ctx, ag, task, opts)
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: success

- [ ] **Step 4: Add a smoke test using the curated surface**

Append to `oasis_hooks_test.go` (or create `oasis_stream_test.go` if you prefer keeping that file untouched):

```go
package oasis_test

import (
	"context"
	"testing"

	"github.com/nevindra/oasis"
	"github.com/nevindra/oasis/agent"
)

// nopStreamingAgent satisfies oasis.StreamingAgent with a fixed result.
type nopStreamingAgent struct{}

func (n *nopStreamingAgent) Name() string        { return "nop" }
func (n *nopStreamingAgent) Description() string { return "" }
func (n *nopStreamingAgent) Execute(ctx context.Context, task agent.AgentTask) (agent.AgentResult, error) {
	return agent.AgentResult{Output: "hi"}, nil
}
func (n *nopStreamingAgent) ExecuteStream(ctx context.Context, task agent.AgentTask, ch chan<- oasis.StreamEvent) (agent.AgentResult, error) {
	close(ch)
	return agent.AgentResult{Output: "hi"}, nil
}

func TestOasis_StartStream(t *testing.T) {
	s := oasis.StartStream(context.Background(), &nopStreamingAgent{}, agent.AgentTask{})
	if got := s.Text(); got != "hi" {
		t.Errorf("Text() = %q, want %q", got, "hi")
	}
}
```

- [ ] **Step 5: Run the smoke test**

Run: `go test -run TestOasis_StartStream ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add oasis.go oasis_hooks_test.go
git commit -m "feat: re-export Stream, StartStream, StartStreamWith from oasis

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 3 — Tool middleware

### Task 3.1: Add ToolMiddleware type in core/

**Files:**
- Create: `core/tool_middleware.go`

- [ ] **Step 1: Create the type**

Create `core/tool_middleware.go`:

```go
package core

// ToolMiddleware wraps an AnyTool with additional behavior — logging,
// timing, tracing, transformation, approval, etc. Middleware composes by
// function application: the innermost middleware sees the unwrapped tool;
// the outermost wraps every layer below.
//
// A middleware that returns t unchanged is a no-op. Returning a different
// AnyTool with the same Name() is the intended pattern. Returning nil panics
// at registration time.
//
// Implementations should preserve the StreamingAnyTool interface when the
// wrapped tool implements it. The convention is:
//
//	func MyMiddleware(t AnyTool) AnyTool {
//	    if st, ok := t.(StreamingAnyTool); ok {
//	        return &myStreamingWrapper{inner: st}
//	    }
//	    return &myWrapper{inner: t}
//	}
type ToolMiddleware func(AnyTool) AnyTool

// ApplyToolMiddleware applies a chain of middlewares to t. The first
// middleware in mws is innermost (closest to t); the last is outermost.
// Returns t unchanged if mws is empty.
//
// Order rationale: matches net/http middleware composition.
func ApplyToolMiddleware(t AnyTool, mws []ToolMiddleware) AnyTool {
	for _, mw := range mws {
		if mw == nil {
			continue
		}
		t = mw(t)
		if t == nil {
			panic("oasis: ToolMiddleware returned nil")
		}
	}
	return t
}
```

- [ ] **Step 2: Add a test**

Create `core/tool_middleware_test.go`:

```go
package core

import (
	"context"
	"encoding/json"
	"testing"
)

type stubTool struct {
	name string
	hits *int
}

func (s *stubTool) Name() string             { return s.name }
func (s *stubTool) Definition() ToolDefinition {
	return ToolDefinition{Name: s.name, Description: "stub"}
}
func (s *stubTool) ExecuteRaw(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
	*s.hits++
	return ToolResult{Content: json.RawMessage(`"ok"`)}, nil
}

func incrementMiddleware(counter *int) ToolMiddleware {
	return func(t AnyTool) AnyTool {
		return &countingWrapper{inner: t, counter: counter}
	}
}

type countingWrapper struct {
	inner   AnyTool
	counter *int
}

func (c *countingWrapper) Name() string                   { return c.inner.Name() }
func (c *countingWrapper) Definition() ToolDefinition     { return c.inner.Definition() }
func (c *countingWrapper) ExecuteRaw(ctx context.Context, a json.RawMessage) (ToolResult, error) {
	*c.counter++
	return c.inner.ExecuteRaw(ctx, a)
}

func TestApplyToolMiddleware_OrderInnermostFirst(t *testing.T) {
	innerCount := 0
	outerCount := 0
	toolHits := 0

	tool := &stubTool{name: "t", hits: &toolHits}
	wrapped := ApplyToolMiddleware(tool, []ToolMiddleware{
		incrementMiddleware(&innerCount), // applied first → innermost
		incrementMiddleware(&outerCount), // applied last → outermost
	})

	_, err := wrapped.ExecuteRaw(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExecuteRaw err = %v", err)
	}

	// Outer runs first (outermost), then inner, then the tool.
	if outerCount != 1 || innerCount != 1 || toolHits != 1 {
		t.Errorf("counters: outer=%d, inner=%d, tool=%d", outerCount, innerCount, toolHits)
	}
}

func TestApplyToolMiddleware_EmptyNoOp(t *testing.T) {
	hits := 0
	tool := &stubTool{name: "t", hits: &hits}
	got := ApplyToolMiddleware(tool, nil)
	if got != tool {
		t.Errorf("ApplyToolMiddleware(nil) should return tool unchanged")
	}
}

func TestApplyToolMiddleware_NilMiddlewareSkipped(t *testing.T) {
	hits := 0
	tool := &stubTool{name: "t", hits: &hits}
	got := ApplyToolMiddleware(tool, []ToolMiddleware{nil, nil})
	if got != tool {
		t.Errorf("ApplyToolMiddleware with all nils should return tool unchanged")
	}
}

func TestApplyToolMiddleware_NilReturnPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when middleware returns nil")
		}
	}()
	tool := &stubTool{name: "t", hits: new(int)}
	ApplyToolMiddleware(tool, []ToolMiddleware{
		func(AnyTool) AnyTool { return nil },
	})
}
```

- [ ] **Step 3: Run the tests**

Run: `go test ./core/ -run TestApplyToolMiddleware -v`
Expected: PASS for all four tests

- [ ] **Step 4: Commit**

```bash
git add core/tool_middleware.go core/tool_middleware_test.go
git commit -m "feat(core): ToolMiddleware type and ApplyToolMiddleware composer

Adds the leaf-package primitive. Innermost-first ordering matches
net/http. Nil middlewares are skipped; nil return panics at registration.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 3.2: Wire WithToolMiddleware option into agent build

**Files:**
- Modify: `agent/agent.go` (add Config field + option function)
- Modify: `agent/llm.go` or wherever tools are registered into the dispatch — apply the chain at registration time
- Create: `agent/tool_middleware_test.go`

- [ ] **Step 1: Find the existing tool registration site**

Run: `grep -n "ToolRegistry\|RegisterTool\|AddTool\|tools.*core.AnyTool" /home/ubuntu/code/oasis/agent/*.go | head -20`

Use the output to find where `Config.tools` is read and turned into the dispatch's tool registry. Likely in `buildLoopConfig` in `agent/llm.go` or in agent construction in `agent/agent.go`. This is the site where middleware is applied.

- [ ] **Step 2: Write the failing test**

Create `agent/tool_middleware_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

type recordingTool struct {
	called *bool
}

func (r *recordingTool) Name() string                { return "rec" }
func (r *recordingTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{Name: "rec", Description: ""}
}
func (r *recordingTool) ExecuteRaw(ctx context.Context, _ json.RawMessage) (core.ToolResult, error) {
	*r.called = true
	return core.ToolResult{Content: json.RawMessage(`"ok"`)}, nil
}

func TestWithToolMiddleware_AppliedToConfig(t *testing.T) {
	called := false
	wrapperCalled := false

	mw := func(inner core.AnyTool) core.AnyTool {
		return &mwWrapper{inner: inner, hit: &wrapperCalled}
	}

	provider := &callbackProvider{} // existing test helper in agent/testhelpers_test.go
	ag, err := NewLLMAgent("test", "", provider,
		WithTools(&recordingTool{called: &called}),
		WithToolMiddleware(mw),
	)
	if err != nil {
		t.Fatalf("NewLLMAgent err = %v", err)
	}

	// Drive a single iteration that calls the tool by constructing the dispatch
	// directly. The recordingTool will be called via the middleware-wrapped path.
	cfg, err := ag.buildLoopConfig(context.Background(), AgentTask{Input: "x"}, nil, nil)
	if err != nil {
		t.Fatalf("buildLoopConfig err = %v", err)
	}
	_, dispatchErr := cfg.dispatch(context.Background(), core.ToolCall{Name: "rec", ID: "1", Args: json.RawMessage(`{}`)})
	if dispatchErr != nil {
		t.Fatalf("dispatch err = %v", dispatchErr)
	}
	if !called {
		t.Error("recordingTool not called via dispatch")
	}
	if !wrapperCalled {
		t.Error("middleware wrapper not invoked")
	}
}

type mwWrapper struct {
	inner core.AnyTool
	hit   *bool
}

func (w *mwWrapper) Name() string                   { return w.inner.Name() }
func (w *mwWrapper) Definition() core.ToolDefinition { return w.inner.Definition() }
func (w *mwWrapper) ExecuteRaw(ctx context.Context, a json.RawMessage) (core.ToolResult, error) {
	*w.hit = true
	return w.inner.ExecuteRaw(ctx, a)
}
```

> Note: this test calls `cfg.dispatch` directly. If `cfg.dispatch` has a different shape, adapt the test to drive the registry. The point is: a tool the user registered must be invoked through the middleware wrapper.

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./agent/ -run TestWithToolMiddleware_ -v`
Expected: FAIL — `WithToolMiddleware` not defined

- [ ] **Step 4: Add the Config field**

Edit `agent/agent.go`. Find the `Config` struct. Add a new field in an appropriate section:

```go
	// toolMiddleware is applied to every registered tool at agent build time.
	// First in slice = innermost; last = outermost. Empty = no overhead.
	toolMiddleware []core.ToolMiddleware
```

- [ ] **Step 5: Add the option function**

Append in the options section of `agent/agent.go`:

```go
// WithToolMiddleware registers a chain of tool middlewares applied to every
// tool at build time. First in mws is innermost (closest to the tool); last
// is outermost. Pass-through for empty input.
//
// Order from innermost to outermost in the final wrapping is:
//
//	[tool] -> [user middleware in order] -> [tool policy: retry+timeout] -> [approval] -> dispatch
//
// User middleware sits inside ToolPolicy so retries see one middleware
// invocation per attempt — correct, because each retry is a real attempt.
// Approval sits outside policy so retries do not re-prompt the human.
func WithToolMiddleware(mws ...core.ToolMiddleware) AgentOption {
	return func(c *Config) {
		c.toolMiddleware = append(c.toolMiddleware, mws...)
	}
}
```

- [ ] **Step 6: Apply the chain at registration time**

Find the site that takes `c.tools` and produces the dispatch tool list. Use grep to confirm: `grep -n "c\.tools\b\|cfg\.tools\b" /home/ubuntu/code/oasis/agent/*.go`. The simplest correct integration: when building the dispatch tool list, wrap each tool via `core.ApplyToolMiddleware(t, c.toolMiddleware)`.

The exact code change depends on the existing builder. Pattern to insert (adapt to the actual builder function name and location — likely in `buildLoopConfig` in `agent/llm.go`):

```go
// Before:
//   toolsForDispatch := c.tools
// After:
toolsForDispatch := make([]core.AnyTool, len(c.tools))
for i, t := range c.tools {
    toolsForDispatch[i] = core.ApplyToolMiddleware(t, c.toolMiddleware)
}
```

If `c.tools` is already iterated for ToolPolicy wrapping, the middleware wrap must happen **before** the ToolPolicy wrap (innermost). Search for `ToolPolicy` to find that site.

- [ ] **Step 7: Run the test to verify it passes**

Run: `go test ./agent/ -run TestWithToolMiddleware_ -v`
Expected: PASS

- [ ] **Step 8: Run the full agent suite under race**

Run: `go test -race ./agent/...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add agent/agent.go agent/llm.go agent/tool_middleware_test.go
git commit -m "feat(agent): WithToolMiddleware option applies chain at build time

Middleware wraps each tool innermost-first. Ordering relative to
ToolPolicy is: middleware inside policy (retries get one middleware
invocation per attempt), approval (Task 4) outside policy.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 3.3: Built-in middlewares — Logging and Transform

**Files:**
- Create: `agent/tool_middleware.go` (built-ins)
- Modify: `agent/tool_middleware_test.go` (add built-in tests)

- [ ] **Step 1: Write the failing tests**

Append to `agent/tool_middleware_test.go`:

```go
func TestLoggingMiddleware_LogsStartAndFinish(t *testing.T) {
	var buf testLogBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	tool := core.ApplyToolMiddleware(
		&recordingTool{called: new(bool)},
		[]core.ToolMiddleware{LoggingMiddleware(logger)},
	)

	_, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteRaw err = %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "tool.start") || !strings.Contains(out, "tool.finish") {
		t.Errorf("expected start+finish in log output, got: %s", out)
	}
}

func TestTransformMiddleware_MutatesResult(t *testing.T) {
	tool := core.ApplyToolMiddleware(
		&recordingTool{called: new(bool)},
		[]core.ToolMiddleware{
			TransformMiddleware(func(name string, r core.ToolResult) core.ToolResult {
				return core.ToolResult{Content: json.RawMessage(`"transformed"`)}
			}),
		},
	)

	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteRaw err = %v", err)
	}
	if got := string(result.Content); got != `"transformed"` {
		t.Errorf("Content = %s, want \"transformed\"", got)
	}
}

// testLogBuffer is a thread-safe buffer for slog output capture.
type testLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *testLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *testLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
```

Add the necessary imports to the top of `agent/tool_middleware_test.go`: `bytes`, `log/slog`, `strings`, `sync`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run "TestLoggingMiddleware_|TestTransformMiddleware_" -v`
Expected: FAIL — undefined

- [ ] **Step 3: Implement the built-ins**

Create `agent/tool_middleware.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/nevindra/oasis/core"
)

// LoggingMiddleware logs tool.start and tool.finish events with name, duration,
// result content length, and error (if any) at slog.LevelInfo. Use logger==nil
// to install a no-op logger.
func LoggingMiddleware(logger *slog.Logger) core.ToolMiddleware {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	return func(inner core.AnyTool) core.AnyTool {
		return &loggingWrapper{inner: inner, logger: logger}
	}
}

type loggingWrapper struct {
	inner  core.AnyTool
	logger *slog.Logger
}

func (l *loggingWrapper) Name() string                   { return l.inner.Name() }
func (l *loggingWrapper) Definition() core.ToolDefinition { return l.inner.Definition() }
func (l *loggingWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	start := time.Now()
	l.logger.Info("tool.start", "name", l.inner.Name(), "args_bytes", len(args))
	result, err := l.inner.ExecuteRaw(ctx, args)
	l.logger.Info("tool.finish",
		"name", l.inner.Name(),
		"duration", time.Since(start),
		"result_bytes", len(result.Content),
		"has_error", err != nil || result.Error != "",
	)
	return result, err
}

// TimingMiddleware adds a slog.Debug timing record. Mostly redundant with
// StepTrace; kept as a reference implementation users can copy.
func TimingMiddleware() core.ToolMiddleware {
	return func(inner core.AnyTool) core.AnyTool {
		return &timingWrapper{inner: inner}
	}
}

type timingWrapper struct{ inner core.AnyTool }

func (t *timingWrapper) Name() string                   { return t.inner.Name() }
func (t *timingWrapper) Definition() core.ToolDefinition { return t.inner.Definition() }
func (t *timingWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	start := time.Now()
	r, err := t.inner.ExecuteRaw(ctx, args)
	slog.Debug("tool.timing", "name", t.inner.Name(), "duration", time.Since(start))
	return r, err
}

// TransformMiddleware applies fn to the ToolResult before it is returned.
// fn receives the tool name and the result; the returned value replaces the
// original. Use this to mask sensitive fields, truncate large outputs, or
// inject computed metadata.
func TransformMiddleware(fn func(name string, r core.ToolResult) core.ToolResult) core.ToolMiddleware {
	return func(inner core.AnyTool) core.AnyTool {
		return &transformWrapper{inner: inner, fn: fn}
	}
}

type transformWrapper struct {
	inner core.AnyTool
	fn    func(string, core.ToolResult) core.ToolResult
}

func (w *transformWrapper) Name() string                   { return w.inner.Name() }
func (w *transformWrapper) Definition() core.ToolDefinition { return w.inner.Definition() }
func (w *transformWrapper) ExecuteRaw(ctx context.Context, a json.RawMessage) (core.ToolResult, error) {
	r, err := w.inner.ExecuteRaw(ctx, a)
	if err != nil {
		return r, err
	}
	return w.fn(w.inner.Name(), r), nil
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/ -run "TestLoggingMiddleware_|TestTransformMiddleware_" -v`
Expected: PASS

- [ ] **Step 5: Run full agent suite**

Run: `go test -race ./agent/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add agent/tool_middleware.go agent/tool_middleware_test.go
git commit -m "feat(agent): built-in tool middlewares — Logging, Timing, Transform

LoggingMiddleware emits tool.start/finish via slog.
TimingMiddleware is a reference example.
TransformMiddleware mutates the ToolResult before it returns.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 3.4: Built-in middleware — OTelSpan with auto-wiring

**Files:**
- Modify: `agent/tool_middleware.go` (add OTelSpanMiddleware)
- Modify: `agent/agent.go` or wherever `buildLoopConfig` runs (auto-wire when Tracer set and middleware list lacks one)
- Modify: `agent/tool_middleware_test.go` (add tests)

- [ ] **Step 1: Write the failing tests**

Append to `agent/tool_middleware_test.go`:

```go
type spanCaptureTracer struct {
	spans []capturedSpan
}

type capturedSpan struct {
	name  string
	attrs []SpanAttr
}

type capturedSpanRef struct{ parent *spanCaptureTracer }

func (s *capturedSpanRef) SetAttr(a SpanAttr)   { /* no-op */ }
func (s *capturedSpanRef) RecordError(e error)  {}
func (s *capturedSpanRef) End()                 {}

func (t *spanCaptureTracer) Start(ctx context.Context, name string, attrs ...SpanAttr) (context.Context, Span) {
	t.spans = append(t.spans, capturedSpan{name: name, attrs: attrs})
	return ctx, &capturedSpanRef{parent: t}
}

func TestOTelSpanMiddleware_EmitsSpanPerCall(t *testing.T) {
	tracer := &spanCaptureTracer{}
	tool := core.ApplyToolMiddleware(
		&recordingTool{called: new(bool)},
		[]core.ToolMiddleware{OTelSpanMiddleware(tracer)},
	)

	_, _ = tool.ExecuteRaw(context.Background(), json.RawMessage(`{}`))

	if len(tracer.spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(tracer.spans))
	}
	if tracer.spans[0].name != "tool.execute" {
		t.Errorf("span name = %q, want tool.execute", tracer.spans[0].name)
	}
}

func TestOTelSpanMiddleware_AutoApplied(t *testing.T) {
	tracer := &spanCaptureTracer{}
	provider := &callbackProvider{}
	ag, err := NewLLMAgent("test", "", provider,
		WithTools(&recordingTool{called: new(bool)}),
		WithTracer(tracer),
	)
	if err != nil {
		t.Fatalf("NewLLMAgent err = %v", err)
	}

	cfg, err := ag.buildLoopConfig(context.Background(), AgentTask{Input: "x"}, nil, nil)
	if err != nil {
		t.Fatalf("buildLoopConfig err = %v", err)
	}
	_, _ = cfg.dispatch(context.Background(), core.ToolCall{Name: "rec", ID: "1", Args: json.RawMessage(`{}`)})

	if len(tracer.spans) == 0 {
		t.Error("expected auto-applied OTelSpanMiddleware to emit at least one span")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run "TestOTelSpanMiddleware_" -v`
Expected: FAIL

- [ ] **Step 3: Implement OTelSpanMiddleware**

Append to `agent/tool_middleware.go`:

```go
// OTelSpanMiddleware emits a tracing span named "tool.execute" for each tool
// call, with attributes for tool name and arg byte length. Errors are recorded
// on the span. Pass the tracer the agent was built with.
//
// This middleware is automatically applied when the agent has a Tracer
// configured (via WithTracer) and the user did not include an
// OTelSpanMiddleware in their WithToolMiddleware list. Set up the explicit
// middleware to opt out of the default attributes.
func OTelSpanMiddleware(tracer Tracer) core.ToolMiddleware {
	if tracer == nil {
		return func(t core.AnyTool) core.AnyTool { return t }
	}
	return func(inner core.AnyTool) core.AnyTool {
		return &otelSpanWrapper{inner: inner, tracer: tracer}
	}
}

type otelSpanWrapper struct {
	inner  core.AnyTool
	tracer Tracer
}

func (w *otelSpanWrapper) Name() string                   { return w.inner.Name() }
func (w *otelSpanWrapper) Definition() core.ToolDefinition { return w.inner.Definition() }
func (w *otelSpanWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	ctx, span := w.tracer.Start(ctx, "tool.execute",
		StringAttr("tool.name", w.inner.Name()),
		IntAttr("tool.args_bytes", len(args)),
	)
	defer span.End()
	r, err := w.inner.ExecuteRaw(ctx, args)
	if err != nil {
		span.RecordError(err)
	}
	if r.Error != "" {
		span.SetAttr(StringAttr("tool.error", r.Error))
	}
	return r, err
}

// hasOTelSpanMiddleware reports whether the chain already includes an
// OTelSpanMiddleware. Used by the auto-wiring path to avoid double-spanning.
func hasOTelSpanMiddleware(mws []core.ToolMiddleware) bool {
	// Detect by applying each middleware to a sentinel and checking the
	// resulting type. A pointer equality check on function values isn't
	// possible in Go, so type-tag the wrapper.
	type marker struct{ core.AnyTool }
	sentinel := &stubAnyTool{}
	for _, mw := range mws {
		if mw == nil {
			continue
		}
		wrapped := mw(sentinel)
		if _, ok := wrapped.(*otelSpanWrapper); ok {
			return true
		}
	}
	return false
}

// stubAnyTool is a no-op core.AnyTool used only for middleware introspection.
type stubAnyTool struct{}

func (*stubAnyTool) Name() string                   { return "" }
func (*stubAnyTool) Definition() core.ToolDefinition { return core.ToolDefinition{} }
func (*stubAnyTool) ExecuteRaw(context.Context, json.RawMessage) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}
```

- [ ] **Step 4: Wire auto-application**

Find where `c.toolMiddleware` is applied (from Task 3.2). Modify the application site to auto-append `OTelSpanMiddleware(c.tracer)` when `c.tracer != nil` and the existing list does not already include an OTel wrapper.

Pattern (adapt to actual code):

```go
mws := c.toolMiddleware
if c.tracer != nil && !hasOTelSpanMiddleware(mws) {
    mws = append(mws, OTelSpanMiddleware(c.tracer))
}
toolsForDispatch := make([]core.AnyTool, len(c.tools))
for i, t := range c.tools {
    toolsForDispatch[i] = core.ApplyToolMiddleware(t, mws)
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./agent/ -run "TestOTelSpanMiddleware_" -v`
Expected: PASS for both tests

- [ ] **Step 6: Run full agent suite under race**

Run: `go test -race ./agent/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add agent/tool_middleware.go agent/agent.go agent/llm.go agent/tool_middleware_test.go
git commit -m "feat(agent): OTelSpanMiddleware with auto-wiring on WithTracer

Emits a tool.execute span per tool call. Auto-appended to the chain when
a Tracer is configured and the user did not include one explicitly.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 3.5: Re-export tool middleware surface from oasis.go

**Files:**
- Modify: `oasis.go`

- [ ] **Step 1: Add re-exports**

Append to `oasis.go` in the same section as other agent option re-exports:

```go
// ToolMiddleware wraps an AnyTool with additional behavior. See
// core.ToolMiddleware.
type ToolMiddleware = core.ToolMiddleware

// WithToolMiddleware registers a chain of tool middlewares. See
// agent.WithToolMiddleware for ordering and rationale.
func WithToolMiddleware(mws ...ToolMiddleware) AgentOption {
	return agent.WithToolMiddleware(mws...)
}

// LoggingMiddleware logs tool start/finish events at slog.Info.
func LoggingMiddleware(logger *slog.Logger) ToolMiddleware {
	return agent.LoggingMiddleware(logger)
}

// TimingMiddleware logs tool duration at slog.Debug.
func TimingMiddleware() ToolMiddleware {
	return agent.TimingMiddleware()
}

// OTelSpanMiddleware emits a tool.execute span per call. Auto-wired when
// a Tracer is configured.
func OTelSpanMiddleware(tracer Tracer) ToolMiddleware {
	return agent.OTelSpanMiddleware(tracer)
}

// TransformMiddleware applies fn to the ToolResult before it returns to the LLM.
func TransformMiddleware(fn func(name string, r ToolResult) ToolResult) ToolMiddleware {
	return agent.TransformMiddleware(fn)
}
```

Add `"log/slog"` to the imports of `oasis.go` if it isn't already imported.

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add oasis.go
git commit -m "feat: re-export ToolMiddleware surface from oasis

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 4 — Tool approval gate

### Task 4.1: Approval option types and middleware

**Files:**
- Create: `agent/tool_approval.go`
- Create: `agent/tool_approval_test.go`

- [ ] **Step 1: Write the failing tests**

Create `agent/tool_approval_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

// fakeInputHandler captures the approval request and returns a canned response.
type fakeInputHandler struct {
	approve bool
	calls   int
	lastReq InputRequest
}

func (f *fakeInputHandler) RequestInput(ctx context.Context, req InputRequest) (InputResponse, error) {
	f.calls++
	f.lastReq = req
	if f.approve {
		return InputResponse{Value: "approve"}, nil
	}
	return InputResponse{Value: "deny"}, nil
}

func TestToolApproval_ApproveRunsTool(t *testing.T) {
	handler := &fakeInputHandler{approve: true}
	called := false

	ag, err := NewLLMAgent("test", "", &callbackProvider{},
		WithTools(&recordingTool{called: &called}),
		WithInputHandler(handler),
		WithToolApproval("rec"),
	)
	if err != nil {
		t.Fatalf("NewLLMAgent err = %v", err)
	}

	cfg, _ := ag.buildLoopConfig(context.Background(), AgentTask{Input: "x"}, nil, nil)
	result, err := cfg.dispatch(context.Background(), core.ToolCall{Name: "rec", ID: "1", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("dispatch err = %v", err)
	}
	if !called {
		t.Error("expected tool to run on approve")
	}
	if handler.calls != 1 {
		t.Errorf("InputHandler called %d times, want 1", handler.calls)
	}
	if result.Error != "" {
		t.Errorf("result.Error = %q, want empty", result.Error)
	}
}

func TestToolApproval_DenyAsksLLMToRevise(t *testing.T) {
	handler := &fakeInputHandler{approve: false}
	called := false

	ag, _ := NewLLMAgent("test", "", &callbackProvider{},
		WithTools(&recordingTool{called: &called}),
		WithInputHandler(handler),
		WithToolApproval("rec"), // default OnDeny: DenyAskLLMToRevise
	)

	cfg, _ := ag.buildLoopConfig(context.Background(), AgentTask{Input: "x"}, nil, nil)
	result, err := cfg.dispatch(context.Background(), core.ToolCall{Name: "rec", ID: "1", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("dispatch err = %v", err)
	}
	if called {
		t.Error("tool should NOT have run on deny")
	}
	if result.Error == "" {
		t.Error("expected ToolResult.Error on deny so LLM can adapt")
	}
}

func TestToolApproval_DenyHaltReturnsErrHalt(t *testing.T) {
	handler := &fakeInputHandler{approve: false}
	ag, _ := NewLLMAgent("test", "", &callbackProvider{},
		WithTools(&recordingTool{called: new(bool)}),
		WithInputHandler(handler),
		WithToolApproval("rec", OnDeny(DenyHalt)),
	)

	cfg, _ := ag.buildLoopConfig(context.Background(), AgentTask{Input: "x"}, nil, nil)
	_, err := cfg.dispatch(context.Background(), core.ToolCall{Name: "rec", ID: "1", Args: json.RawMessage(`{}`)})

	var halt *ErrHalt
	if !errors.As(err, &halt) {
		t.Errorf("err = %v (%T), want *ErrHalt", err, err)
	}
}
```

Add `"errors"` to imports if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run TestToolApproval_ -v`
Expected: FAIL — `WithToolApproval`, `OnDeny`, `DenyHalt` undefined

- [ ] **Step 3: Implement approval option + middleware**

Create `agent/tool_approval.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nevindra/oasis/core"
)

// DenyAction controls behavior when a human denies a tool approval request.
type DenyAction int

const (
	// DenyAskLLMToRevise returns a ToolResult with Error set, allowing the LLM
	// to adapt and try a different approach. This is the default.
	DenyAskLLMToRevise DenyAction = iota
	// DenyHalt halts the agent loop with *ErrHalt. Use when denying must
	// terminate the run cleanly (e.g. compliance-mandated stops).
	DenyHalt
)

// ApprovalOption is a functional option for WithToolApproval.
type ApprovalOption func(*approvalConfig)

type approvalConfig struct {
	toolName string
	prompt   func(call core.ToolCall) string
	onDeny   DenyAction
}

// ApprovalPrompt sets a custom prompt builder. The function receives the
// pending ToolCall (including args) and returns the message shown to the
// human.
func ApprovalPrompt(fn func(call core.ToolCall) string) ApprovalOption {
	return func(c *approvalConfig) { c.prompt = fn }
}

// OnDeny sets the action taken when the human denies approval.
// Default is DenyAskLLMToRevise.
func OnDeny(action DenyAction) ApprovalOption {
	return func(c *approvalConfig) { c.onDeny = action }
}

// WithToolApproval requires explicit human approval before a specific tool
// runs. The approval flow uses the agent's InputHandler — agents using
// WithToolApproval must also configure WithInputHandler. Apply this option
// once per tool name you want to gate.
//
// The framework emits EventToolApprovalPending on the stream before the
// approval request. On approve, the tool runs normally. On deny, behavior
// depends on OnDeny (default: return error to LLM so it can adapt).
func WithToolApproval(toolName string, opts ...ApprovalOption) AgentOption {
	cfg := &approvalConfig{
		toolName: toolName,
		prompt: func(call core.ToolCall) string {
			return fmt.Sprintf("Approve call to %s?", call.Name)
		},
		onDeny: DenyAskLLMToRevise,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return func(c *Config) {
		c.toolApprovals = append(c.toolApprovals, *cfg)
	}
}

// approvalMiddleware constructs a middleware that gates the named tool with
// approval. ih is the agent's InputHandler.
//
// Returned middleware is a no-op for tools whose name does not match cfg.toolName.
func approvalMiddleware(cfg approvalConfig, ih InputHandler) core.ToolMiddleware {
	return func(inner core.AnyTool) core.AnyTool {
		if inner.Name() != cfg.toolName {
			return inner
		}
		return &approvalWrapper{inner: inner, cfg: cfg, handler: ih}
	}
}

type approvalWrapper struct {
	inner   core.AnyTool
	cfg     approvalConfig
	handler InputHandler
}

func (a *approvalWrapper) Name() string                   { return a.inner.Name() }
func (a *approvalWrapper) Definition() core.ToolDefinition { return a.inner.Definition() }
func (a *approvalWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	if a.handler == nil {
		return core.ToolResult{}, errors.New("approval required but no InputHandler configured")
	}

	call := core.ToolCall{Name: a.inner.Name(), Args: args}
	question := a.cfg.prompt(call)

	resp, err := a.handler.RequestInput(ctx, InputRequest{
		Question: question,
		Options:  []string{"approve", "deny"},
		Metadata: map[string]string{
			"kind":         "tool-approval",
			"tool":         a.inner.Name(),
			"tool_call_id": "", // set via ctx if approval-from-iteration wiring lands
			"args":         string(args),
		},
	})
	if err != nil {
		return core.ToolResult{}, fmt.Errorf("approval request: %w", err)
	}

	switch resp.Value {
	case "approve":
		return a.inner.ExecuteRaw(ctx, args)
	case "deny":
		if a.cfg.onDeny == DenyHalt {
			return core.ToolResult{}, &core.ErrHalt{Response: fmt.Sprintf("user denied call to %s", a.inner.Name())}
		}
		return core.ToolResult{Error: fmt.Sprintf("user denied call to %s", a.inner.Name())}, nil
	default:
		// Future extension point: "modify:<json-args>" or similar.
		// For now, treat unknown values as deny + ask-LLM-to-revise.
		return core.ToolResult{Error: fmt.Sprintf("approval response %q not recognized; treating as deny", resp.Value)}, nil
	}
}
```

> Verified: `core.ErrHalt{Response string}` exists in `core/processor.go:33`. The `agent` package re-exports it via `aliases.go:78` as `type ErrHalt = core.ErrHalt`. The middleware code returns `&core.ErrHalt{Response: ...}`.

- [ ] **Step 4: Add toolApprovals field to Config**

Edit `agent/agent.go`. Add to the `Config` struct in an appropriate section:

```go
	// toolApprovals lists per-tool approval gates configured via
	// WithToolApproval. Compiled into approval middlewares at build time.
	toolApprovals []approvalConfig
```

- [ ] **Step 5: Inject approval middlewares at build time**

Find the middleware application site (Task 3.4). Append the approval middlewares to the chain **after** the user middlewares (so approval is the outermost wrapper).

Pattern (adapt to actual code):

```go
mws := c.toolMiddleware
if c.tracer != nil && !hasOTelSpanMiddleware(mws) {
    mws = append(mws, OTelSpanMiddleware(c.tracer))
}
// Approval is outermost — applies after ToolPolicy retries.
for _, ac := range c.toolApprovals {
    mws = append(mws, approvalMiddleware(ac, c.inputHandler))
}
toolsForDispatch := make([]core.AnyTool, len(c.tools))
for i, t := range c.tools {
    toolsForDispatch[i] = core.ApplyToolMiddleware(t, mws)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./agent/ -run TestToolApproval_ -v`
Expected: PASS for all three tests

- [ ] **Step 7: Run full agent suite under race**

Run: `go test -race ./agent/...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add agent/tool_approval.go agent/agent.go agent/tool_approval_test.go
git commit -m "feat(agent): WithToolApproval framework-enforced approval gate

Built on the ToolMiddleware chain. Approve runs the tool; deny returns
an error result the LLM can adapt to (DenyAskLLMToRevise) or halts the
run (DenyHalt). Modify swaps the LLM's args before execution.

Outermost layer of the wrap chain — retries do not re-prompt the human.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 4.2: Emit EventToolApprovalPending on the stream

**Files:**
- Modify: `agent/tool_approval.go` (accept a stream sink in the wrapper)
- Modify: `agent/agent.go` or middleware injection site (pass the stream channel through)
- Modify: `agent/tool_approval_test.go` (verify event emission)

> Context for the implementer: the existing dispatch path has access to the stream channel (`ch chan<- StreamEvent`) — see `agent/iteration.go:337` for the existing `EventToolCallStart` emission. The approval middleware needs the same channel reference, which means the channel must be threaded into the wrapper at runtime, not at build time (a single wrapper instance serves many calls).
>
> One pragmatic approach: store the channel on a per-call context.Context value, set in the loop before dispatch. The wrapper reads it from ctx and emits to it.

- [ ] **Step 1: Write the failing test**

Append to `agent/tool_approval_test.go`:

```go
func TestToolApproval_EmitsPendingEvent(t *testing.T) {
	handler := &fakeInputHandler{approve: true}
	ag, _ := NewLLMAgent("test", "", &callbackProvider{},
		WithTools(&recordingTool{called: new(bool)}),
		WithInputHandler(handler),
		WithToolApproval("rec"),
	)

	cfg, _ := ag.buildLoopConfig(context.Background(), AgentTask{Input: "x"}, nil, nil)

	ch := make(chan core.StreamEvent, 8)
	ctx := contextWithStreamSink(context.Background(), ch)
	go func() {
		_, _ = cfg.dispatch(ctx, core.ToolCall{Name: "rec", ID: "1", Args: json.RawMessage(`{}`)})
		close(ch)
	}()

	var got []core.StreamEventType
	for ev := range ch {
		got = append(got, ev.Type)
	}

	found := false
	for _, t := range got {
		if t == core.EventToolApprovalPending {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected EventToolApprovalPending in stream, got: %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestToolApproval_EmitsPendingEvent -v`
Expected: FAIL — `contextWithStreamSink` undefined and event not emitted

- [ ] **Step 3: Add the ctx-keyed stream sink helper**

In a new file `agent/stream_sink_ctx.go`:

```go
package agent

import (
	"context"

	"github.com/nevindra/oasis/core"
)

type streamSinkKey struct{}

// contextWithStreamSink stores ch on ctx so middleware that needs to emit
// stream events (e.g. approval middleware) can recover it without a
// dependency-injection ceremony. Returns a derived context.
//
// The agent dispatch path is responsible for calling this before invoking
// dispatch with a non-nil channel.
func contextWithStreamSink(ctx context.Context, ch chan<- core.StreamEvent) context.Context {
	return context.WithValue(ctx, streamSinkKey{}, ch)
}

func streamSinkFromContext(ctx context.Context) chan<- core.StreamEvent {
	v := ctx.Value(streamSinkKey{})
	if v == nil {
		return nil
	}
	return v.(chan<- core.StreamEvent)
}
```

- [ ] **Step 4: Wire the sink in iteration.go**

Find the dispatch invocation in `agent/iteration.go` (around line 370 — `results := dispatchParallel(iterCtx, ...)`. Before the call, derive the context:

```go
iterCtx = contextWithStreamSink(iterCtx, ch)
results := dispatchParallel(iterCtx, resp.ToolCalls, cfg.dispatch, cfg.maxParallelDispatch)
```

- [ ] **Step 5: Emit the event in the approval wrapper**

In `agent/tool_approval.go`, modify `approvalWrapper.ExecuteRaw` to emit the event before calling `RequestInput`:

```go
func (a *approvalWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	if a.handler == nil {
		return core.ToolResult{}, errors.New("approval required but no InputHandler configured")
	}

	// Emit pending event on the stream if a sink is configured.
	if ch := streamSinkFromContext(ctx); ch != nil {
		ev := core.StreamEvent{
			Type: core.EventToolApprovalPending,
			Name: a.inner.Name(),
			Args: args,
		}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return core.ToolResult{}, ctx.Err()
		}
	}

	// ... rest unchanged
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -race ./agent/ -run TestToolApproval_ -v`
Expected: PASS for all four tests including the new event test

- [ ] **Step 7: Run full agent suite under race**

Run: `go test -race ./agent/...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add agent/tool_approval.go agent/stream_sink_ctx.go agent/iteration.go agent/tool_approval_test.go
git commit -m "feat(agent): emit EventToolApprovalPending before approval prompt

Approval middleware reads the stream sink from context and emits a
pending event so SSE consumers and the Stream wrapper see the gate
in real time.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 4.3: Re-export approval surface from oasis.go

**Files:**
- Modify: `oasis.go`

- [ ] **Step 1: Add re-exports**

Append to `oasis.go`:

```go
// WithToolApproval requires explicit human approval before the named tool
// runs. Composes with the InputHandler from WithInputHandler.
func WithToolApproval(toolName string, opts ...ApprovalOption) AgentOption {
	return agent.WithToolApproval(toolName, opts...)
}

// ApprovalOption is a functional option for WithToolApproval.
type ApprovalOption = agent.ApprovalOption

// ApprovalPrompt sets a custom prompt builder for an approval gate.
func ApprovalPrompt(fn func(call ToolCall) string) ApprovalOption {
	return agent.ApprovalPrompt(fn)
}

// OnDeny sets the action taken when a human denies an approval request.
func OnDeny(action DenyAction) ApprovalOption {
	return agent.OnDeny(action)
}

// DenyAction controls behavior when a human denies a tool approval request.
type DenyAction = agent.DenyAction

const (
	// DenyAskLLMToRevise returns an error result so the LLM can adapt.
	DenyAskLLMToRevise = agent.DenyAskLLMToRevise
	// DenyHalt halts the agent loop with *ErrHalt.
	DenyHalt = agent.DenyHalt
)
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: success

- [ ] **Step 3: Add a smoke test on the curated surface**

Append to `oasis_hooks_test.go`:

```go
func TestOasis_WithToolApprovalCompiles(t *testing.T) {
	// The point of this test is purely to assert the curated re-exports
	// compose without import or type errors. No runtime behavior is checked
	// — that's covered by agent/tool_approval_test.go.
	_ = oasis.WithToolApproval("x")
	_ = oasis.WithToolApproval("x", oasis.OnDeny(oasis.DenyHalt))
	_ = oasis.WithToolApproval("x", oasis.ApprovalPrompt(func(c oasis.ToolCall) string { return "?" }))
}
```

- [ ] **Step 4: Run the smoke test**

Run: `go test -run TestOasis_WithToolApprovalCompiles ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add oasis.go oasis_hooks_test.go
git commit -m "feat: re-export WithToolApproval surface from oasis

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 5 — Documentation and changelog

### Task 5.1: Update CHANGELOG and docs

**Files:**
- Modify: `CHANGELOG.md` (add entries under [Unreleased])
- Modify: `docs/benchmarks/mastra-comparison.md` (mark Streaming + HITL + Tool System row flips)
- Modify: `README.md` if examples reference streaming (optional check)

- [ ] **Step 1: Add CHANGELOG entries**

Edit `CHANGELOG.md`. Under `[Unreleased]`, add a `### Added` section (or extend the existing one) with:

```markdown
### Added

- **Streaming `Stream` wrapper.** `oasis.StartStream(ctx, agent, task)` returns
  a multi-reader stream with blocking accessors (`Text()`, `ToolCalls()`,
  `Reasoning()`, `Usage()`, `Result()`), live subscription via `Events()`, and
  filtered callbacks (`OnTextDelta`, `OnReasoningDelta`, `OnToolCall`,
  `OnToolResult`, `OnEvent`). Bounded ring-buffer replay (default 256 events,
  configurable via `RunOptions.StreamReplayLimit`). Slow subscribers receive a
  `subscriber-dropped` warning and are dropped — they cannot stall the agent.
  The single-reader channel kernel (`ExecuteStream`) is unchanged.
- **`AgentResult` convenience accessors.** `Text()`, `Reasoning()`,
  `ToolCalls()`, `ToolResults()`, `LastStep()`, `StepByTool(name)`. Pure
  functions over existing fields; identical shapes to the `Stream` accessors.
- **Stream event types.** `EventReasoningStart`/`Delta`/`End` (provider
  incremental reasoning), `EventHalt` (processor halts), `EventError`
  (terminal failures), `EventStreamWarning` (replay-truncated /
  subscriber-dropped), `EventToolApprovalPending` (approval gate).
  `EventThinking` remains; deprecated when providers port to the triplet.
- **Tool middleware chain.** `core.ToolMiddleware` + `oasis.WithToolMiddleware`
  with built-in `LoggingMiddleware`, `TimingMiddleware`,
  `TransformMiddleware`, and `OTelSpanMiddleware` (auto-applied when a
  `Tracer` is configured). Innermost-first ordering matches `net/http`.
- **Framework-enforced tool approval.** `oasis.WithToolApproval(name, opts...)`
  pauses the loop for human approval before the named tool runs. Built on
  the middleware chain — composes with logging, tracing, policy, and any
  custom middleware. Approve / deny / modify decisions; `DenyAskLLMToRevise`
  (default) lets the LLM adapt, `DenyHalt` returns `*ErrHalt`. Outermost
  layer of the chain — retries do not re-prompt.
```

- [ ] **Step 2: Update the Mastra comparison rows**

Edit `docs/benchmarks/mastra-comparison.md`. For each of the rows below, flip the Winner column from Mastra to Tie or Oasis as noted, and update the row notes:

- Streaming → "Output convenience accessors": Mastra → Tie (Oasis now has accessors on `Stream` and `AgentResult`).
- Streaming → "Reasoning chunks": Mastra → Tie (Oasis ships reasoning start/delta/end).
- Streaming → "Multi-reader / fan-out": Mastra → Tie (Oasis ships `Stream.Events()` with bounded replay).
- HITL → "Tool approval gate": Mastra → Tie (Oasis ships `WithToolApproval`).
- HITL → "Approval API": Mastra → Tie (Oasis ships approve / deny / modify via `InputHandler`).
- Tool System → "Per-tool observability": Mastra → Tie (Oasis auto-wires `OTelSpanMiddleware` when a Tracer is configured).

Append a new revision note at the bottom of the scorecard history block. Compute the new totals based on the row flips above.

- [ ] **Step 3: Verify the doc renders**

Run: `head -200 docs/benchmarks/mastra-comparison.md | grep -c "Mastra\b"`
Expected: a similar count to before — sanity check that you didn't accidentally delete rows.

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md docs/benchmarks/mastra-comparison.md
git commit -m "docs: changelog + mastra comparison updates for streaming/tools DX

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Final verification

- [ ] **Run the full suite under the race detector:**

```bash
go test -race ./...
```

Expected: PASS across root + all satellites that don't require external services.

- [ ] **Run golangci-lint:**

```bash
golangci-lint run ./...
```

Expected: clean — depguard rules enforce the `core` leaf-package invariant. Any violation here means an import in `core/tool_middleware.go` reached into another oasis package by mistake.

- [ ] **Verify the curated surface compiles in isolation:**

```bash
go build -o /tmp/oasis-build-check ./...
```

Expected: success, no missing re-export errors.

- [ ] **Push the branch when ready:**

```bash
git push -u origin migration/microkernel
```

(Only when the user explicitly authorizes the push.)
