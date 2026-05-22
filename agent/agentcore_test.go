package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/oasis/history"
)

// ptrA is a local pointer helper for this test file (avoids import of oasis root pkg).
func ptrA[T any](v T) *T { return &v }

// --- initCore tests ---

func TestInitCoreWiresAllFields(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithPrompt("test prompt"),
		WithLimits(Limits{MaxIter: 42}),
		WithGeneration(Generation{Temperature: ptrA(0.7)}),
	})

	var c AgentCore
	p := &mockProvider{name: "test"}
	InitCore(&c, "myagent", "does stuff", p, cfg)

	if c.name != "myagent" {
		t.Errorf("name = %q, want %q", c.name, "myagent")
	}
	if c.description != "does stuff" {
		t.Errorf("description = %q, want %q", c.description, "does stuff")
	}
	if c.provider != p {
		t.Error("provider not wired")
	}
	if c.systemPrompt != "test prompt" {
		t.Errorf("systemPrompt = %q, want %q", c.systemPrompt, "test prompt")
	}
	if c.maxIter != 42 {
		t.Errorf("maxIter = %d, want 42", c.maxIter)
	}
	if c.genParams == nil || c.genParams.Temperature == nil || *c.genParams.Temperature != 0.7 {
		t.Error("generationParams.Temperature not wired")
	}
	if c.tools == nil {
		t.Error("tools registry not initialized")
	}
	if c.processors == nil {
		t.Error("processors chain not initialized")
	}
}

func TestInitCoreDefaultMaxIter(t *testing.T) {
	cfg := BuildConfig(nil)
	var c AgentCore
	InitCore(&c, "a", "d", &mockProvider{name: "p"}, cfg)

	if c.maxIter != defaultMaxIter {
		t.Errorf("maxIter = %d, want default %d", c.maxIter, defaultMaxIter)
	}
}

func TestDefaultMaxIterIs25(t *testing.T) {
	cfg := BuildConfig(nil)
	var c AgentCore
	InitCore(&c, "t", "", &mockProvider{name: "p"}, cfg)
	if c.maxIter != 25 {
		t.Errorf("expected defaultMaxIter 25, got %d", c.maxIter)
	}
}

func TestInitCoreMemoryFieldsWired(t *testing.T) {
	// Verifies that memory options wire through InitCore without panicking.
	// Deep field verification is done via integration tests in memory_test.go.
	store := &stubStore{}
	cfg := BuildConfig([]AgentOption{
		WithHistory(history.Store(store), history.MaxHistory(25), history.MaxTokens(5000)),
	})

	var c AgentCore
	// Should not panic.
	InitCore(&c, "a", "d", &mockProvider{name: "p"}, cfg)
	// Close should be safe after Init (even without any executions).
	if err := c.mem.Close(); err != nil {
		t.Errorf("mem.Close error: %v", err)
	}
}

// --- Shared method tests ---

func TestAgentCoreNameDescriptionClose(t *testing.T) {
	var c AgentCore
	InitCore(&c, "core", "core desc", &mockProvider{name: "p"}, BuildConfig(nil))

	if c.Name() != "core" {
		t.Errorf("Name() = %q, want %q", c.Name(), "core")
	}
	if c.Description() != "core desc" {
		t.Errorf("Description() = %q, want %q", c.Description(), "core desc")
	}
	// Close should not panic on zero-state memory.
	if err := c.Close(); err != nil {
		t.Errorf("Close error: %v", err)
	}
}

func TestCacheBuiltinToolDefs(t *testing.T) {
	var c AgentCore
	InitCore(&c, "a", "d", &mockProvider{name: "p"}, BuildConfig(nil))

	// No builtins configured: should return input unchanged.
	defs := c.CacheBuiltinToolDefs(nil)
	if len(defs) != 0 {
		t.Errorf("got %d defs, want 0", len(defs))
	}

	// With all builtins.
	c.inputHandler = &mockInputHandler{response: InputResponse{Value: "ok"}}
	c.planExecution = true
	defs = c.CacheBuiltinToolDefs([]ToolDefinition{{Name: "existing"}})
	if len(defs) != 3 { // existing + ask_user + execute_plan
		t.Errorf("got %d defs, want 3", len(defs))
	}
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"existing", "ask_user", "execute_plan"} {
		if !names[want] {
			t.Errorf("missing tool def %q", want)
		}
	}
}

func TestResolvePromptAndProvider(t *testing.T) {
	base := &mockProvider{name: "base"}
	override := &mockProvider{name: "override"}

	var c AgentCore
	InitCore(&c, "a", "d", base, BuildConfig([]AgentOption{
		WithPrompt("static prompt"),
	}))

	task := AgentTask{Input: "test"}

	// Static path.
	prompt, prov := c.ResolvePromptAndProvider(context.Background(), task)
	if prompt != "static prompt" {
		t.Errorf("prompt = %q, want %q", prompt, "static prompt")
	}
	if prov != base {
		t.Error("provider should be base")
	}

	// Dynamic overrides.
	c.dynamicPrompt = func(_ context.Context, _ AgentTask) string { return "dynamic prompt" }
	c.dynamicModel = func(_ context.Context, _ AgentTask) Provider { return override }

	prompt, prov = c.ResolvePromptAndProvider(context.Background(), task)
	if prompt != "dynamic prompt" {
		t.Errorf("prompt = %q, want %q", prompt, "dynamic prompt")
	}
	if prov != override {
		t.Error("provider should be override")
	}
}

func TestResolveDynamicToolsNil(t *testing.T) {
	var c AgentCore
	InitCore(&c, "a", "d", &mockProvider{name: "p"}, BuildConfig(nil))

	defs, exec := c.ResolveDynamicTools(context.Background(), AgentTask{})
	if defs != nil || exec != nil {
		t.Error("expected nil when dynamicTools not set")
	}
}

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
		events: []StreamEvent{
			{Type: EventTextDelta, Content: "a"},
			{Type: EventTextDelta, Content: "b"},
		},
		result: AgentResult{Output: "ab"},
	}

	ch := make(chan StreamEvent, 32)
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

	ch := make(chan StreamEvent, 32)
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
		events: []StreamEvent{
			{Type: EventInputReceived, Content: "should be filtered"},
			// New lifecycle envelope events should also be filtered.
			{Type: EventRunStart, Content: "should be filtered"},
			{Type: EventIterationStart, Name: "0"},
			{Type: EventTextDelta, Content: "visible"},
			{Type: EventIterationFinish, Name: "0"},
			{Type: EventRunFinish, Content: "should be filtered"},
		},
		result: AgentResult{Output: "done"},
	}

	ch := make(chan StreamEvent, 32)
	var forwarded []StreamEvent
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

	// EventInputReceived and the new lifecycle envelope events should be filtered out.
	for _, ev := range forwarded {
		if ev.Type == EventInputReceived ||
			ev.Type == EventRunStart || ev.Type == EventRunFinish ||
			ev.Type == EventIterationStart || ev.Type == EventIterationFinish {
			t.Errorf("envelope event %q should be filtered from forwarded events", ev.Type)
		}
	}
	if len(forwarded) != 1 {
		t.Errorf("forwarded %d events, want 1 (only text-delta); got: %v", len(forwarded), func() []StreamEventType {
			types := make([]StreamEventType, len(forwarded))
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

	ch := make(chan StreamEvent, 1) // Small buffer to force blocking.
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
		responses: []ChatResponse{{Content: "streamed hello"}},
	}
	a := NewLLMAgent("double-close-test", "regression", provider)

	ch := make(chan StreamEvent, 64)
	result, err := a.ExecuteStream(context.Background(), AgentTask{Input: "hi"}, ch)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if result.Output != "streamed hello" {
		t.Errorf("Output = %q, want %q", result.Output, "streamed hello")
	}

	// Drain to completion — any "close of closed channel" panic surfaces here
	// (or inside ExecuteStream above) if the fix is absent.
	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	// Sanity: we should have at least input-received + processing-start + text-delta.
	if len(events) < 3 {
		t.Errorf("expected ≥3 events, got %d", len(events))
	}
}

// --- startDrainTimeout tests ---

func TestStartDrainTimeoutDrainsChannel(t *testing.T) {
	ch := make(chan StreamEvent, 10)
	// Send some events before starting drain.
	ch <- StreamEvent{Type: EventTextDelta, Content: "a"}
	ch <- StreamEvent{Type: EventTextDelta, Content: "b"}
	close(ch)

	closed := make(chan struct{})
	safeClose := func() { close(closed) }

	startDrainTimeout(ch, safeClose, nopLogger, "test")

	// Channel is already closed, so drain should finish quickly
	// without hitting the timeout (safeClose should NOT be called).
	select {
	case <-closed:
		t.Error("safeClose should not be called when channel closes normally")
	case <-time.After(500 * time.Millisecond):
		// Good — drain completed without timeout.
	}
}

// --- safeAgentError tests ---

func TestSafeAgentError(t *testing.T) {
	err := safeAgentError("worker", "on fire")
	if !strings.Contains(err.Error(), "worker") || !strings.Contains(err.Error(), "on fire") {
		t.Errorf("error = %v, want to contain agent name and panic value", err)
	}
}

// --- Embedded agentCore promotes methods ---

func TestLLMAgentEmbedsAgentCore(t *testing.T) {
	a := NewLLMAgent("test", "desc", &mockProvider{name: "p"})
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

// blockingStreamAgent implements StreamingAgent and blocks until context is cancelled.
type blockingStreamAgent struct {
	name string
	desc string
}

func (b *blockingStreamAgent) Name() string        { return b.name }
func (b *blockingStreamAgent) Description() string { return b.desc }
func (b *blockingStreamAgent) Execute(ctx context.Context, _ AgentTask) (AgentResult, error) {
	<-ctx.Done()
	return AgentResult{}, ctx.Err()
}
func (b *blockingStreamAgent) ExecuteStream(ctx context.Context, _ AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	defer close(ch)
	<-ctx.Done()
	return AgentResult{}, ctx.Err()
}

// --- LLMAgent.Generation() tests ---

func TestLLMAgent_Generation_ReturnsCopy(t *testing.T) {
	temp := 0.5
	a := NewLLMAgent("a", "d", &mockProvider{name: "test"}, WithGeneration(Generation{
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
	a := NewLLMAgent("a", "d", &mockProvider{name: "test"})
	g := a.Generation()
	if g.Temperature != nil || g.TopP != nil || g.TopK != nil || g.MaxTokens != nil {
		t.Fatalf("Generation: unset returned %+v, want all-nil", g)
	}
}

// --- ExecuteWith / ExecuteStreamWith tests ---

// newExecuteTestAgent returns a minimal LLMAgent backed by a mock provider
// that deterministically returns "ok" for every call.
func newExecuteTestAgent(t *testing.T) *LLMAgent {
	t.Helper()
	return NewLLMAgent("test", "desc", &mockProvider{
		name:      "mock",
		responses: []ChatResponse{{Content: "ok"}},
	})
}

func TestExecuteWith_NilEquivalentToExecute(t *testing.T) {
	a := newExecuteTestAgent(t)
	r1, err := a.Execute(context.Background(), AgentTask{Input: "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Fresh agent with same mock for second call.
	a2 := newExecuteTestAgent(t)
	r2, err := a2.ExecuteWith(context.Background(), AgentTask{Input: "hello"}, nil)
	if err != nil {
		t.Fatalf("ExecuteWith(nil): %v", err)
	}
	if r1.Output != r2.Output {
		t.Fatalf("Execute(%q) != ExecuteWith(nil, %q)", r1.Output, r2.Output)
	}
}

func TestExecuteWith_EmptyEquivalentToExecute(t *testing.T) {
	a := newExecuteTestAgent(t)
	r1, err := a.Execute(context.Background(), AgentTask{Input: "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	a2 := newExecuteTestAgent(t)
	r2, err := a2.ExecuteWith(context.Background(), AgentTask{Input: "hello"}, &RunOptions{})
	if err != nil {
		t.Fatalf("ExecuteWith(&{}): %v", err)
	}
	if r1.Output != r2.Output {
		t.Fatalf("Execute(%q) != ExecuteWith(&{}, %q)", r1.Output, r2.Output)
	}
}

func TestExecuteWith_ValidationFails(t *testing.T) {
	a := newExecuteTestAgent(t)
	n := -1
	_, err := a.ExecuteWith(context.Background(), AgentTask{Input: "x"}, &RunOptions{Limits: &Limits{MaxIter: n}})
	if err == nil {
		t.Fatalf("ExecuteWith(Limits.MaxIter=-1): err = nil, want validation error")
	}
	var roErr *RunOptionsError
	if !errors.As(err, &roErr) {
		t.Fatalf("ExecuteWith(Limits.MaxIter=-1): err is not *RunOptionsError: %v", err)
	}
}

func TestExecuteStreamWith_NilEquivalentToExecuteStream(t *testing.T) {
	a := newExecuteTestAgent(t)
	ch1 := make(chan StreamEvent, 32)
	r1, err := a.ExecuteStream(context.Background(), AgentTask{Input: "hello"}, ch1)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	for range ch1 {
	}

	a2 := newExecuteTestAgent(t)
	ch2 := make(chan StreamEvent, 32)
	r2, err := a2.ExecuteStreamWith(context.Background(), AgentTask{Input: "hello"}, ch2, nil)
	if err != nil {
		t.Fatalf("ExecuteStreamWith(nil): %v", err)
	}
	for range ch2 {
	}

	if r1.Output != r2.Output {
		t.Fatalf("ExecuteStream(%q) != ExecuteStreamWith(nil, %q)", r1.Output, r2.Output)
	}
}

// TestAgentCore_LimitsGetterRoundTrips verifies that Limits() returns a copy
// of the agent's current limits that callers can mutate and pass back via
// RunOptions.Limits without affecting the agent.
func TestAgentCore_LimitsGetterRoundTrips(t *testing.T) {
	a := NewLLMAgent("t", "d", &mockProvider{name: "m", responses: []ChatResponse{{Content: "ok"}}},
		WithLimits(Limits{MaxIter: 7, MaxAttachmentBytes: 1234}))
	lim := a.Limits()
	if lim.MaxIter != 7 || lim.MaxAttachmentBytes != 1234 {
		t.Fatalf("Limits() did not reflect WithLimits: %+v", lim)
	}
	lim.MaxIter = 99
	// Agent's internal state must be untouched.
	if a.maxIter != 7 {
		t.Fatalf("mutating returned Limits affected agent: maxIter=%d, want 7", a.maxIter)
	}
}
