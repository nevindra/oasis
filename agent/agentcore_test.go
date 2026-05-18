package agent

import (
	"context"
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
		WithMaxIter(42),
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
	if c.LLMProvider != p {
		t.Error("provider not wired")
	}
	if c.systemPrompt != "test prompt" {
		t.Errorf("systemPrompt = %q, want %q", c.systemPrompt, "test prompt")
	}
	if c.MaxIter != 42 {
		t.Errorf("maxIter = %d, want 42", c.MaxIter)
	}
	if c.GenParams == nil || c.GenParams.Temperature == nil || *c.GenParams.Temperature != 0.7 {
		t.Error("generationParams.Temperature not wired")
	}
	if c.Tools == nil {
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

	if c.MaxIter != defaultMaxIter {
		t.Errorf("maxIter = %d, want default %d", c.MaxIter, defaultMaxIter)
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
	c.Handler = &mockInputHandler{response: InputResponse{Value: "ok"}}
	c.PlanExecution = true
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
			{Type: EventTextDelta, Content: "visible"},
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

	// EventInputReceived should be filtered out.
	for _, ev := range forwarded {
		if ev.Type == EventInputReceived {
			t.Error("EventInputReceived should be filtered from forwarded events")
		}
	}
	if len(forwarded) != 1 {
		t.Errorf("forwarded %d events, want 1 (only text-delta)", len(forwarded))
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
