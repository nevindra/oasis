package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestIterationDecision_Continue(t *testing.T) {
	d := Continue()
	if d.action != decisionContinue {
		t.Fatalf("Continue: action = %v, want decisionContinue", d.action)
	}
}

func TestIterationDecision_Stop(t *testing.T) {
	result := core.AgentResult{Output: "done"}
	d := Stop(result)
	if d.action != decisionStop {
		t.Fatalf("Stop: action = %v, want decisionStop", d.action)
	}
	if d.result.Output != "done" {
		t.Fatalf("Stop: result.Output = %q, want %q", d.result.Output, "done")
	}
}

func TestIterationDecision_InjectFeedback(t *testing.T) {
	d := InjectFeedback("retry")
	if d.action != decisionInject {
		t.Fatalf("InjectFeedback: action = %v, want decisionInject", d.action)
	}
	if len(d.msgs) != 1 {
		t.Fatalf("InjectFeedback: len(msgs) = %d, want 1", len(d.msgs))
	}
	if d.msgs[0].Role != core.RoleUser {
		t.Fatalf("InjectFeedback: msgs[0].Role = %v, want RoleUser", d.msgs[0].Role)
	}
}

func TestIterationDecision_InjectMessages(t *testing.T) {
	msg := core.ChatMessage{Role: core.RoleAssistant, Content: "hello"}
	d := InjectMessages(msg)
	if d.action != decisionInject {
		t.Fatalf("InjectMessages: action = %v, want decisionInject", d.action)
	}
	if len(d.msgs) != 1 || d.msgs[0].Role != core.RoleAssistant {
		t.Fatalf("InjectMessages: got %+v", d.msgs)
	}
}

func TestErrorDecision_Propagate(t *testing.T) {
	d := Propagate()
	if d.action != errPropagate {
		t.Fatalf("Propagate: action = %v, want errPropagate", d.action)
	}
}

func TestErrorDecision_Retry(t *testing.T) {
	d := Retry()
	if d.action != errRetry {
		t.Fatalf("Retry: action = %v, want errRetry", d.action)
	}
}

func TestErrorDecision_RetryWithFeedback(t *testing.T) {
	d := RetryWithFeedback("try again with this hint")
	if d.action != errRetry {
		t.Fatalf("RetryWithFeedback: action = %v, want errRetry", d.action)
	}
	if d.feedback != "try again with this hint" {
		t.Fatalf("RetryWithFeedback: feedback = %q", d.feedback)
	}
}

func TestErrorDecision_HaltDecision(t *testing.T) {
	result := core.AgentResult{Output: "partial"}
	d := HaltDecision(result)
	if d.action != errHalt {
		t.Fatalf("HaltDecision: action = %v, want errHalt", d.action)
	}
	if d.result.Output != "partial" {
		t.Fatalf("HaltDecision: result.Output = %q", d.result.Output)
	}
}

func TestWithPrepareStep(t *testing.T) {
	called := false
	fn := func(ctx context.Context, iter int, ctrl *StepControl) error {
		called = true
		return nil
	}
	cfg := BuildConfig([]AgentOption{WithPrepareStep(fn)})
	if cfg.prepareStep == nil {
		t.Fatalf("WithPrepareStep: cfg.prepareStep is nil")
	}
	_ = cfg.prepareStep(context.Background(), 0, &StepControl{})
	if !called {
		t.Fatalf("WithPrepareStep: hook was not stored")
	}
}

func TestWithOnIterationComplete(t *testing.T) {
	fn := func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error) {
		return Stop(core.AgentResult{Output: "stopped"}), nil
	}
	cfg := BuildConfig([]AgentOption{WithOnIterationComplete(fn)})
	if cfg.onIterationComplete == nil {
		t.Fatalf("WithOnIterationComplete: hook not stored")
	}
}

func TestWithOnError(t *testing.T) {
	fn := func(ctx context.Context, iter int, err error) (ErrorDecision, error) {
		return Retry(), nil
	}
	cfg := BuildConfig([]AgentOption{WithOnError(fn)})
	if cfg.onError == nil {
		t.Fatalf("WithOnError: hook not stored")
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
	a := NewLLMAgent("a", "d", captured, WithPrepareStep(hook))
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
	a := NewLLMAgent("a", "d", provider, WithPrepareStep(hook))
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
	a := NewLLMAgent("a", "d", defaultProvider, WithPrepareStep(hook))
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
	a := NewLLMAgent("a", "d", provider, WithOnError(hook))
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
	a := NewLLMAgent("a", "d", provider, WithOnError(hook))
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
	a := NewLLMAgent("a", "d", provider, WithOnError(hook))
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
	a := NewLLMAgent("a", "d", provider, WithOnError(hook))
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
	a := NewLLMAgent("a", "d", provider, WithOnError(hook))
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
	a := NewLLMAgent("a", "d", provider,
		WithTools(mockTool{}),
		WithOnIterationComplete(hook))
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
	a := NewLLMAgent("a", "d", provider,
		WithTools(mockTool{}),
		WithOnIterationComplete(hook))
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
	a := NewLLMAgent("a", "d", provider,
		WithTools(mockTool{}),
		WithOnIterationComplete(hook))
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
	a := NewLLMAgent("a", "d", provider,
		WithTools(mockTool{}),
		WithOnIterationComplete(hook))
	_, err := a.Execute(context.Background(), core.AgentTask{Input: "x"})
	if !errors.Is(err, hookErr) {
		t.Fatalf("Execute: err = %v, want wrapping hook err %v", err, hookErr)
	}
}
