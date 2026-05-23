package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

// ptrA is a local pointer helper for this test file (avoids import of oasis root pkg).
func ptrA[T any](v T) *T { return &v }

// --- executeAgent tests ---

func TestExecuteAgentNonStreaming(t *testing.T) {
	agent := &stubAgent{
		name: "worker",
		desc: "test",
		fn: func(task AgentTask) (AgentResult, error) {
			return AgentResult{Output: "done: " + task.Input}, nil
		},
	}

	result, err := ExecuteAgent(context.Background(), agent, "worker",
		AgentTask{Input: "hello"}, nil, nopLogger)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done: hello" {
		t.Errorf("Output = %q, want %q", result.Output, "done: hello")
	}
}

func TestExecuteAgentNonStreamingPanic(t *testing.T) {
	agent := &stubAgent{
		name: "crasher",
		desc: "test",
		fn: func(_ AgentTask) (AgentResult, error) {
			panic("boom")
		},
	}

	result, err := ExecuteAgent(context.Background(), agent, "crasher",
		AgentTask{Input: "go"}, nil, nopLogger)
	if err == nil {
		t.Fatal("expected error from panic recovery")
	}
	if result.Output != "" {
		t.Errorf("Output should be empty, got %q", result.Output)
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Errorf("error should mention panic, got: %v", err)
	}
}

func TestExecuteAgentStreamingDelegation(t *testing.T) {
	streamer := &stubStreamingAgent{
		name: "streamer",
		desc: "test",
		events: []core.StreamEvent{
			{Type: core.EventTextDelta, Content: "a"},
			{Type: core.EventTextDelta, Content: "b"},
		},
		result: AgentResult{Output: "ab"},
	}

	ch := make(chan core.StreamEvent, 32)
	go func() {
		// Drain parent channel so forwarding doesn't block.
		for range ch {
		}
	}()

	result, err := ExecuteAgent(context.Background(), streamer, "streamer",
		AgentTask{Input: "go"}, ch, nopLogger)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "ab" {
		t.Errorf("Output = %q, want %q", result.Output, "ab")
	}
}

func TestExecuteAgentStreamingPanic(t *testing.T) {
	// Use the panicStreamingAgent from stream_test.go.
	panicker := &panicStreamingAgent{name: "crasher", desc: "test"}

	ch := make(chan core.StreamEvent, 32)
	go func() {
		for range ch {
		}
	}()

	result, err := ExecuteAgent(context.Background(), panicker, "crasher",
		AgentTask{Input: "go"}, ch, nopLogger)
	if err == nil {
		t.Fatal("expected error from streaming panic recovery")
	}
	if result.Output != "" {
		t.Errorf("Output should be empty, got %q", result.Output)
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Errorf("error should mention panic, got: %v", err)
	}
}

// --- forwardSubagentStream tests ---

func TestForwardSubagentStreamFiltersInputReceived(t *testing.T) {
	streamer := &stubStreamingAgent{
		name: "sub",
		desc: "test",
		events: []core.StreamEvent{
			{Type: core.EventInputReceived, Content: "should be filtered"},
			// New lifecycle envelope events should also be filtered.
			{Type: core.EventRunStart, Content: "should be filtered"},
			{Type: core.EventIterationStart, Name: "0"},
			{Type: core.EventTextDelta, Content: "visible"},
			{Type: core.EventIterationFinish, Name: "0"},
			{Type: core.EventRunFinish, Content: "should be filtered"},
		},
		result: AgentResult{Output: "done"},
	}

	ch := make(chan core.StreamEvent, 32)
	var forwarded []core.StreamEvent
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range ch {
			forwarded = append(forwarded, ev)
		}
	}()

	result, err := forwardSubagentStream(context.Background(), streamer, "sub",
		AgentTask{Input: "go"}, ch, nopLogger)
	close(ch)
	wg.Wait()

	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}

	// core.EventInputReceived and the new lifecycle envelope events should be filtered out.
	for _, ev := range forwarded {
		if ev.Type == core.EventInputReceived ||
			ev.Type == core.EventRunStart || ev.Type == core.EventRunFinish ||
			ev.Type == core.EventIterationStart || ev.Type == core.EventIterationFinish {
			t.Errorf("envelope event %q should be filtered from forwarded events", ev.Type)
		}
	}
	if len(forwarded) != 1 {
		t.Errorf("forwarded %d events, want 1 (only text-delta); got: %v", len(forwarded), func() []core.StreamEventType {
			types := make([]core.StreamEventType, len(forwarded))
			for i, e := range forwarded {
				types[i] = e.Type
			}
			return types
		}())
	}
}

func TestForwardSubagentStreamContextCancellation(t *testing.T) {
	// Subagent that blocks until context is cancelled.
	blocker := &blockingStreamAgent{name: "blocker", desc: "test"}

	ch := make(chan core.StreamEvent, 1) // Small buffer to force blocking.
	go func() {
		for range ch {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := forwardSubagentStream(ctx, blocker, "blocker",
		AgentTask{Input: "go"}, ch, nopLogger)
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
}

// --- onceClose tests ---

func TestOnceCloseIdempotent(t *testing.T) {
	ch := make(chan int, 1)
	closer := onceClose(ch)

	// First call should close the channel.
	closer()

	// Subsequent calls should not panic.
	closer()
	closer()

	// Verify channel is closed.
	_, ok := <-ch
	if ok {
		t.Error("channel should be closed")
	}
}

func TestOnceCloseConcurrent(t *testing.T) {
	ch := make(chan struct{})
	closer := onceClose(ch)

	// Hammer from multiple goroutines — should not panic.
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			closer()
		}()
	}
	wg.Wait()

	_, ok := <-ch
	if ok {
		t.Error("channel should be closed")
	}
}

// --- double-close regression test ---

// TestForwardSubagentStreamDoubleCloseSafe verifies that removing the recover()
// from onceClose does not cause a "close of closed channel" panic.
//
// Root cause: providers (including mockProvider) call defer close(ch) inside
// ChatStream. When loop.go passed ch directly to ChatStream on the no-tools
// streaming path, the provider closed ch first, then runLoop's safeCloseCh()
// tried to close it again via its own sync.Once — triggering the panic before
// the Once had a chance to mark itself done.
//
// The fix routes the no-tools and synthesis paths through an intermediate iterCh
// (mirroring the with-tools path), so providers never touch ch directly and
// safeCloseCh remains the sole closer.
func TestForwardSubagentStreamDoubleCloseSafe(t *testing.T) {
	// mockProvider.ChatStream does defer close(ch) — this is the bypass that
	// previously triggered the double-close when ch was passed directly.
	provider := &mockProvider{
		name:      "test",
		responses: []core.ChatResponse{{Content: "streamed hello"}},
	}
	a := New("double-close-test", "regression", provider)

	ch := make(chan core.StreamEvent, 64)
	result, err := a.Execute(context.Background(), AgentTask{Input: "hi"}, core.WithStream(ch))
	if err != nil {
		t.Fatalf("Execute(WithStream) error: %v", err)
	}
	if result.Output != "streamed hello" {
		t.Errorf("Output = %q, want %q", result.Output, "streamed hello")
	}

	// Drain to completion — any "close of closed channel" panic surfaces here
	// (or inside Execute above) if the fix is absent.
	var events []core.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	// Sanity: we should have at least input-received + processing-start + text-delta.
	if len(events) < 3 {
		t.Errorf("expected ≥3 events, got %d", len(events))
	}
}

// --- drainSubCh tests ---

func TestDrainSubChDrainsChannel(t *testing.T) {
	ch := make(chan core.StreamEvent, 10)
	ch <- core.StreamEvent{Type: core.EventTextDelta, Content: "a"}
	ch <- core.StreamEvent{Type: core.EventTextDelta, Content: "b"}
	close(ch)

	closed := false
	safeClose := func() { closed = true }

	// drainSubCh now runs synchronously (no orphan goroutine). With the
	// channel already closed it returns immediately without firing the
	// safety timeout.
	drainSubCh(ch, safeClose, nopLogger, "test")

	if closed {
		t.Error("safeClose should not be called when channel closes normally")
	}
}

// --- safeAgentError tests ---

func TestSafeAgentError(t *testing.T) {
	err := safeAgentError("worker", "on fire")
	if !strings.Contains(err.Error(), "worker") || !strings.Contains(err.Error(), "on fire") {
		t.Errorf("error = %v, want to contain agent name and panic value", err)
	}
}

// --- LLMAgent method-promotion tests (embedding runtime.Runtime) ---

func TestLLMAgentRuntimePromotion(t *testing.T) {
	a := New("test", "desc", &mockProvider{name: "p"})
	if a.Name() != "test" {
		t.Errorf("Name() = %q, want %q", a.Name(), "test")
	}
	if a.Description() != "desc" {
		t.Errorf("Description() = %q, want %q", a.Description(), "desc")
	}
	if err := a.Close(); err != nil { // Should not return error or panic.
		t.Errorf("Close error: %v", err)
	}
}

// --- test helpers (local to this file) ---

// blockingStreamAgent implements core.Agent and blocks until context is cancelled.
type blockingStreamAgent struct {
	name string
	desc string
}

func (b *blockingStreamAgent) Name() string        { return b.name }
func (b *blockingStreamAgent) Description() string { return b.desc }
func (b *blockingStreamAgent) Execute(ctx context.Context, _ AgentTask, opts ...RunOption) (AgentResult, error) {
	rcfg := core.ApplyRunOptions(opts...)
	if rcfg.Stream != nil {
		defer close(rcfg.Stream)
	}
	<-ctx.Done()
	return AgentResult{}, ctx.Err()
}

// --- LLMAgent.Generation() tests ---

func TestLLMAgent_Generation_ReturnsCopy(t *testing.T) {
	temp := 0.5
	a := New("a", "d", &mockProvider{name: "test"}, WithGeneration(Generation{
		Temperature: &temp,
	}))

	g := a.Generation()
	if g.Temperature == nil {
		t.Fatalf("Generation: Temperature is nil")
	}
	if *g.Temperature != 0.5 {
		t.Fatalf("Generation: Temperature = %v, want 0.5", *g.Temperature)
	}

	// Mutate the returned struct's referenced data; original must not change.
	*g.Temperature = 0.9
	g2 := a.Generation()
	if g2.Temperature == nil || *g2.Temperature != 0.5 {
		t.Fatalf("Generation: original mutated to %v, want unchanged at 0.5", *g2.Temperature)
	}
}

func TestLLMAgent_Generation_UnsetReturnsEmpty(t *testing.T) {
	a := New("a", "d", &mockProvider{name: "test"})
	g := a.Generation()
	if g.Temperature != nil || g.TopP != nil || g.TopK != nil || g.MaxTokens != nil {
		t.Fatalf("Generation: unset returned %+v, want all-nil", g)
	}
}

// --- Execute with RunOptions tests ---

// newExecuteTestAgent returns a minimal LLMAgent backed by a mock provider
// that deterministically returns "ok" for every call.
func newExecuteTestAgent(t *testing.T) *LLMAgent {
	t.Helper()
	return New("test", "desc", &mockProvider{
		name:      "mock",
		responses: []core.ChatResponse{{Content: "ok"}},
	})
}

func TestExecuteWith_NilEquivalentToExecute(t *testing.T) {
	a := newExecuteTestAgent(t)
	r1, err := a.Execute(context.Background(), AgentTask{Input: "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Fresh agent with same mock for second call; nil WithOverrides is a no-op.
	a2 := newExecuteTestAgent(t)
	r2, err := a2.Execute(context.Background(), AgentTask{Input: "hello"}, WithOverrides(nil))
	if err != nil {
		t.Fatalf("Execute(WithOverrides(nil)): %v", err)
	}
	if r1.Output != r2.Output {
		t.Fatalf("Execute(%q) != Execute(WithOverrides(nil), %q)", r1.Output, r2.Output)
	}
}

func TestExecuteWith_EmptyEquivalentToExecute(t *testing.T) {
	a := newExecuteTestAgent(t)
	r1, err := a.Execute(context.Background(), AgentTask{Input: "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	a2 := newExecuteTestAgent(t)
	r2, err := a2.Execute(context.Background(), AgentTask{Input: "hello"}, WithOverrides(&RunOptions{}))
	if err != nil {
		t.Fatalf("Execute(WithOverrides(&{})): %v", err)
	}
	if r1.Output != r2.Output {
		t.Fatalf("Execute(%q) != Execute(WithOverrides(&{}), %q)", r1.Output, r2.Output)
	}
}

func TestExecuteWith_ValidationFails(t *testing.T) {
	a := newExecuteTestAgent(t)
	n := -1
	_, err := a.Execute(context.Background(), AgentTask{Input: "x"}, WithOverrides(&RunOptions{Limits: &Limits{MaxIter: n}}))
	if err == nil {
		t.Fatalf("Execute(Limits.MaxIter=-1): err = nil, want validation error")
	}
	var roErr *RunOptionsError
	if !errors.As(err, &roErr) {
		t.Fatalf("Execute(Limits.MaxIter=-1): err is not *RunOptionsError: %v", err)
	}
}

func TestExecuteStreamWith_NilEquivalentToExecuteStream(t *testing.T) {
	a := newExecuteTestAgent(t)
	ch1 := make(chan core.StreamEvent, 32)
	r1, err := a.Execute(context.Background(), AgentTask{Input: "hello"}, core.WithStream(ch1))
	if err != nil {
		t.Fatalf("Execute(WithStream): %v", err)
	}
	for range ch1 {
	}

	a2 := newExecuteTestAgent(t)
	ch2 := make(chan core.StreamEvent, 32)
	r2, err := a2.Execute(context.Background(), AgentTask{Input: "hello"}, core.WithStream(ch2), WithOverrides(nil))
	if err != nil {
		t.Fatalf("Execute(WithStream, WithOverrides(nil)): %v", err)
	}
	for range ch2 {
	}

	if r1.Output != r2.Output {
		t.Fatalf("Execute(WithStream)(%q) != Execute(WithStream, WithOverrides(nil))(%q)", r1.Output, r2.Output)
	}
}

// TestRuntime_LimitsGetterRoundTrips verifies that Limits() returns a copy
// of the agent's current limits that callers can mutate and pass back via
// RunOptions.Limits without affecting the agent.
func TestRuntime_LimitsGetterRoundTrips(t *testing.T) {
	a := New("t", "d", &mockProvider{name: "m", responses: []core.ChatResponse{{Content: "ok"}}},
		WithLimits(Limits{MaxIter: 7, MaxAttachmentBytes: 1234}))
	lim := a.Limits()
	if lim.MaxIter != 7 || lim.MaxAttachmentBytes != 1234 {
		t.Fatalf("Limits() did not reflect WithLimits: %+v", lim)
	}
	lim.MaxIter = 99
	// Agent's internal state must be untouched.
	if a.MaxIter != 7 {
		t.Fatalf("mutating returned Limits affected agent: maxIter=%d, want 7", a.MaxIter)
	}
}
