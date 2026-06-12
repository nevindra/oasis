package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestIterationDecision_Continue(t *testing.T) {
	d := Continue()
	if d.IsStop() || d.IsInject() {
		t.Fatalf("Continue: should not be Stop or Inject")
	}
}

func TestIterationDecision_Stop(t *testing.T) {
	result := core.AgentResult{Output: "done"}
	d := Stop(result)
	if !d.IsStop() {
		t.Fatalf("Stop: IsStop() = false, want true")
	}
	if d.Result().Output != "done" {
		t.Fatalf("Stop: Result().Output = %q, want %q", d.Result().Output, "done")
	}
}

func TestIterationDecision_InjectFeedback(t *testing.T) {
	d := InjectFeedback("retry")
	if !d.IsInject() {
		t.Fatalf("InjectFeedback: IsInject() = false, want true")
	}
	if len(d.Msgs()) != 1 {
		t.Fatalf("InjectFeedback: len(Msgs()) = %d, want 1", len(d.Msgs()))
	}
	if d.Msgs()[0].Role != core.RoleUser {
		t.Fatalf("InjectFeedback: Msgs()[0].Role = %v, want RoleUser", d.Msgs()[0].Role)
	}
}

func TestIterationDecision_InjectMessages(t *testing.T) {
	msg := core.ChatMessage{Role: core.RoleAssistant, Content: "hello"}
	d := InjectMessages(msg)
	if !d.IsInject() {
		t.Fatalf("InjectMessages: IsInject() = false, want true")
	}
	if len(d.Msgs()) != 1 || d.Msgs()[0].Role != core.RoleAssistant {
		t.Fatalf("InjectMessages: got %+v", d.Msgs())
	}
}

func TestErrorDecision_Propagate(t *testing.T) {
	d := Propagate()
	if !d.IsPropagate() {
		t.Fatalf("Propagate: IsPropagate() = false, want true")
	}
}

func TestErrorDecision_Retry(t *testing.T) {
	d := Retry()
	if !d.IsRetry() {
		t.Fatalf("Retry: IsRetry() = false, want true")
	}
}

func TestErrorDecision_RetryWithFeedback(t *testing.T) {
	d := RetryWithFeedback("try again with this hint")
	if !d.IsRetry() {
		t.Fatalf("RetryWithFeedback: IsRetry() = false, want true")
	}
	if d.Feedback() != "try again with this hint" {
		t.Fatalf("RetryWithFeedback: Feedback() = %q", d.Feedback())
	}
}

func TestErrorDecision_HaltDecision(t *testing.T) {
	result := core.AgentResult{Output: "partial"}
	d := HaltDecision(result)
	if !d.IsHalt() {
		t.Fatalf("HaltDecision: IsHalt() = false, want true")
	}
	if d.Result().Output != "partial" {
		t.Fatalf("HaltDecision: Result().Output = %q", d.Result().Output)
	}
}

func TestWithHooks_PrepareStep(t *testing.T) {
	called := false
	fn := func(ctx context.Context, iter int, ctrl *StepControl) error {
		called = true
		return nil
	}
	cfg := BuildConfig([]AgentOption{WithHooks(Hooks{PrepareStep: fn})})
	if cfg.PrepareStep == nil {
		t.Fatalf("WithHooks: cfg.PrepareStep is nil")
	}
	_ = cfg.PrepareStep(context.Background(), 0, &StepControl{})
	if !called {
		t.Fatalf("WithHooks: PrepareStep was not stored")
	}
}

func TestWithHooks_OnIterationComplete(t *testing.T) {
	fn := func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error) {
		return Stop(core.AgentResult{Output: "stopped"}), nil
	}
	cfg := BuildConfig([]AgentOption{WithHooks(Hooks{OnIterationComplete: fn})})
	if cfg.OnIterationComplete == nil {
		t.Fatalf("WithHooks: OnIterationComplete not stored")
	}
}

func TestWithHooks_OnError(t *testing.T) {
	fn := func(ctx context.Context, iter int, err error) (ErrorDecision, error) {
		return Retry(), nil
	}
	cfg := BuildConfig([]AgentOption{WithHooks(Hooks{OnError: fn})})
	if cfg.OnError == nil {
		t.Fatalf("WithHooks: OnError not stored")
	}
}

func TestPrepareStep_MutatesRequest(t *testing.T) {
	called := false
	hook := func(ctx context.Context, iter int, ctrl *StepControl) error {
		called = true
		ctrl.Request.Messages = append(ctrl.Request.Messages, core.ChatMessage{
			Role:    core.RoleSystem,
			Content: "extra system note",
		})
		return nil
	}

	captured := &capturedRequestProvider{}
	a := New("a", "d", captured, WithHooks(Hooks{PrepareStep: hook}))
	_, err := a.Execute(context.Background(), core.AgentTask{Input: "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !called {
		t.Fatalf("PrepareStep: hook not called")
	}
	last := captured.last()
	found := false
	for _, m := range last.Messages {
		if m.Role == core.RoleSystem && m.Content == "extra system note" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("PrepareStep: mutation did not reach LLM. Messages: %v", last.Messages)
	}
}

func TestPrepareStep_ErrorFailsRun(t *testing.T) {
	wantErr := errors.New("hook failed")
	hook := func(ctx context.Context, iter int, ctrl *StepControl) error {
		return wantErr
	}
	provider := &capturedRequestProvider{}
	a := New("a", "d", provider, WithHooks(Hooks{PrepareStep: hook}))
	_, err := a.Execute(context.Background(), core.AgentTask{Input: "x"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute: err = %v, want wrapping %v", err, wantErr)
	}
}

func TestPrepareStep_OverrideModel(t *testing.T) {
	defaultProvider := &capturedRequestProvider{name: "default"}
	overrideProvider := &capturedRequestProvider{name: "override"}

	hook := func(ctx context.Context, iter int, ctrl *StepControl) error {
		ctrl.Model = overrideProvider
		return nil
	}
	a := New("a", "d", defaultProvider, WithHooks(Hooks{PrepareStep: hook}))
	_, err := a.Execute(context.Background(), core.AgentTask{Input: "x"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if defaultProvider.callCount() != 0 {
		t.Fatalf("default provider was called %d times; want 0", defaultProvider.callCount())
	}
	if overrideProvider.callCount() == 0 {
		t.Fatalf("override provider was not called")
	}
}

func TestOnError_Propagate(t *testing.T) {
	wantErr := errors.New("llm boom")
	provider := &flakyProvider{errFn: func() error { return wantErr }}
	hook := func(ctx context.Context, iter int, err error) (ErrorDecision, error) {
		return Propagate(), nil
	}
	a := New("a", "d", provider, WithHooks(Hooks{OnError: hook}))
	_, err := a.Execute(context.Background(), core.AgentTask{Input: "x"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute: err = %v, want wrapping %v", err, wantErr)
	}
}

func TestOnError_Retry(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	provider := &flakyProvider{errFn: func() error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return errors.New("transient")
		}
		return nil
	}}
	hook := func(ctx context.Context, iter int, err error) (ErrorDecision, error) {
		return Retry(), nil
	}
	a := New("a", "d", provider, WithHooks(Hooks{OnError: hook}))
	_, err := a.Execute(context.Background(), core.AgentTask{Input: "x"})
	if err != nil {
		t.Fatalf("Execute: err = %v, want nil after retry", err)
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
}

func TestOnError_RetryWithFeedback(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	provider := &flakyProvider{errFn: func() error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return errors.New("invalid tool name")
		}
		return nil
	}}
	hook := func(ctx context.Context, iter int, err error) (ErrorDecision, error) {
		return RetryWithFeedback("use one of: search, calc"), nil
	}
	a := New("a", "d", provider, WithHooks(Hooks{OnError: hook}))
	_, err := a.Execute(context.Background(), core.AgentTask{Input: "x"})
	if err != nil {
		t.Fatalf("Execute: err = %v", err)
	}
	captured := provider.last()
	found := false
	for _, m := range captured.Messages {
		if m.Role == core.RoleUser && m.Content == "use one of: search, calc" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("RetryWithFeedback: message not in retry history. Messages: %v", captured.Messages)
	}
}

func TestOnError_HaltDecision(t *testing.T) {
	wantErr := errors.New("boom")
	provider := &flakyProvider{errFn: func() error { return wantErr }}
	hook := func(ctx context.Context, iter int, err error) (ErrorDecision, error) {
		return HaltDecision(core.AgentResult{Output: "graceful end"}), nil
	}
	a := New("a", "d", provider, WithHooks(Hooks{OnError: hook}))
	result, err := a.Execute(context.Background(), core.AgentTask{Input: "x"})
	if err != nil {
		t.Fatalf("Execute: err = %v, want nil after HaltDecision", err)
	}
	if result.Output != "graceful end" {
		t.Fatalf("Output = %q, want %q", result.Output, "graceful end")
	}
}

func TestOnError_HookErrorPropagates(t *testing.T) {
	hookErr := errors.New("hook failed")
	provider := &flakyProvider{errFn: func() error { return errors.New("original") }}
	hook := func(ctx context.Context, iter int, err error) (ErrorDecision, error) {
		return ErrorDecision{}, hookErr
	}
	a := New("a", "d", provider, WithHooks(Hooks{OnError: hook}))
	_, err := a.Execute(context.Background(), core.AgentTask{Input: "x"})
	if !errors.Is(err, hookErr) {
		t.Fatalf("Execute: err = %v, want wrapping hook err %v", err, hookErr)
	}
}

// --- OnIterationComplete wiring tests ---

func TestOnIterationComplete_Continue(t *testing.T) {
	calls := 0
	hook := func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error) {
		calls++
		return Continue(), nil
	}
	provider := &twoIterProvider{}
	a := New("a", "d", provider,
		WithTools(mockTool{}),
		WithHooks(Hooks{OnIterationComplete: hook}))
	_, err := a.Execute(context.Background(), core.AgentTask{Input: "x"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls != 2 {
		t.Fatalf("hook called %d times, want 2", calls)
	}
}

func TestOnIterationComplete_Stop(t *testing.T) {
	hook := func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error) {
		return Stop(core.AgentResult{Output: "early stop"}), nil
	}
	provider := &twoIterProvider{}
	a := New("a", "d", provider,
		WithTools(mockTool{}),
		WithHooks(Hooks{OnIterationComplete: hook}))
	result, err := a.Execute(context.Background(), core.AgentTask{Input: "x"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "early stop" {
		t.Fatalf("Output = %q, want %q", result.Output, "early stop")
	}
	// Provider should only be called ONCE — Stop fires after iter 0.
	if provider.callCount() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.callCount())
	}
}

func TestOnIterationComplete_InjectFeedback(t *testing.T) {
	called := 0
	hook := func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error) {
		called++
		if called == 1 {
			return InjectFeedback("please reconsider"), nil
		}
		return Continue(), nil
	}
	provider := &twoIterProvider{}
	a := New("a", "d", provider,
		WithTools(mockTool{}),
		WithHooks(Hooks{OnIterationComplete: hook}))
	_, err := a.Execute(context.Background(), core.AgentTask{Input: "x"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// The second iteration's ChatRequest should contain the injected message.
	last := provider.last()
	found := false
	for _, m := range last.Messages {
		if m.Role == core.RoleUser && m.Content == "please reconsider" {
			found = true
		}
	}
	if !found {
		t.Fatalf("InjectFeedback: message not visible to LLM in next iter. Messages: %v", last.Messages)
	}
}

func TestOnIterationComplete_HookErrorPropagates(t *testing.T) {
	hookErr := errors.New("hook failed")
	hook := func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error) {
		return IterationDecision{}, hookErr
	}
	provider := &twoIterProvider{}
	a := New("a", "d", provider,
		WithTools(mockTool{}),
		WithHooks(Hooks{OnIterationComplete: hook}))
	_, err := a.Execute(context.Background(), core.AgentTask{Input: "x"})
	if !errors.Is(err, hookErr) {
		t.Fatalf("Execute: err = %v, want wrapping hook err %v", err, hookErr)
	}
}

// TestOnIterationComplete_SnapshotTraceRingBufferEviction guards the snapshot's
// Trace against the ring-buffer eviction hazard in state.steps.
//
// With MaxSteps=1 the step ring holds a single slot, but the iteration emits two
// parallel tool calls — so the second append evicts the first and len(steps)
// stays at 1. The previous back-index form (state.steps[len-len(ToolCalls)] =
// state.steps[1-2] = state.steps[-1]) panicked here. The snapshot's Trace must
// instead be the FIRST tool call of this iteration ("greet"), captured forward
// as traces are built rather than recovered by indexing the ring.
func TestOnIterationComplete_SnapshotTraceRingBufferEviction(t *testing.T) {
	// First iteration: two parallel calls with distinct names so we can assert
	// which one lands in the snapshot Trace. Second: plain text to terminate.
	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{
				{ID: "tc1", Name: "greet", Args: json.RawMessage(`{}`)},
				{ID: "tc2", Name: "calc", Args: json.RawMessage(`{}`)},
			}},
			{Content: "done"},
		},
	}

	var gotTrace StepTrace
	hookCalls := 0
	hook := func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error) {
		hookCalls++
		if iter == 0 {
			gotTrace = snap.Trace
		}
		return Continue(), nil
	}

	a := New("a", "d", provider,
		WithTools(mockTool{}, mockToolCalc{}),
		WithLimits(Limits{MaxSteps: 1}), // ring of size 1 → eviction within the iteration
		WithHooks(Hooks{OnIterationComplete: hook}))

	// (a) No panic: a panic in the loop goroutine would surface as a failed run
	// or crash the test binary.
	_, err := a.Execute(context.Background(), core.AgentTask{Input: "go"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if hookCalls == 0 {
		t.Fatalf("OnIterationComplete never fired")
	}

	// (b) The snapshot Trace is the FIRST tool call of the iteration, not the
	// evicted/overwritten ring slot.
	if gotTrace.Name != "greet" {
		t.Fatalf("snapshot Trace.Name = %q, want %q (first tool call of the iteration)", gotTrace.Name, "greet")
	}
}
