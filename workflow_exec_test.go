package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// --- Sequential execution tests ---

func TestWorkflowSequential(t *testing.T) {
	var order []string

	wf, err := NewWorkflow("seq", "sequential test",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			order = append(order, "a")
			wCtx.Set("a.output", "from-a")
			return nil
		}),
		Step("b", func(_ context.Context, wCtx *WorkflowContext) error {
			order = append(order, "b")
			v, _ := wCtx.Get("a.output")
			wCtx.Set("b.output", fmt.Sprintf("from-b(%v)", v))
			return nil
		}, After("a")),
		Step("c", func(_ context.Context, wCtx *WorkflowContext) error {
			order = append(order, "c")
			v, _ := wCtx.Get("b.output")
			wCtx.Set("c.output", fmt.Sprintf("from-c(%v)", v))
			return nil
		}, After("b")),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "start"})
	if err != nil {
		t.Fatal(err)
	}

	// Verify order.
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Errorf("execution order = %v, want [a b c]", order)
	}

	// Verify output propagation.
	if result.Output != "from-c(from-b(from-a))" {
		t.Errorf("Output = %q, want %q", result.Output, "from-c(from-b(from-a))")
	}
}

// --- Parallel execution tests ---

func TestWorkflowParallel(t *testing.T) {
	var started atomic.Int32

	wf, err := NewWorkflow("par", "parallel test",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("a.output", "root")
			return nil
		}),
		Step("b", func(_ context.Context, wCtx *WorkflowContext) error {
			started.Add(1)
			time.Sleep(10 * time.Millisecond)
			wCtx.Set("b.output", "b-done")
			return nil
		}, After("a")),
		Step("c", func(_ context.Context, wCtx *WorkflowContext) error {
			started.Add(1)
			time.Sleep(10 * time.Millisecond)
			wCtx.Set("c.output", "c-done")
			return nil
		}, After("a")),
		Step("d", func(_ context.Context, wCtx *WorkflowContext) error {
			// d should only start after both b and c finished.
			if started.Load() != 2 {
				t.Error("d started before b and c completed")
			}
			wCtx.Set("d.output", "join-done")
			return nil
		}, After("b", "c")),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "join-done" {
		t.Errorf("Output = %q, want %q", result.Output, "join-done")
	}
}

// --- Conditional (When) tests ---

func TestWorkflowConditionalBranch(t *testing.T) {
	wf, err := NewWorkflow("cond", "conditional test",
		Step("init", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("type", "digital")
			return nil
		}),
		Step("physical", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("physical.output", "shipped")
			return nil
		}, After("init"), When(func(wCtx *WorkflowContext) bool {
			v, _ := wCtx.Get("type")
			return v == "physical"
		})),
		Step("digital", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("digital.output", "delivered")
			return nil
		}, After("init"), When(func(wCtx *WorkflowContext) bool {
			v, _ := wCtx.Get("type")
			return v == "digital"
		})),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "order"})
	if err != nil {
		t.Fatal(err)
	}

	// Only digital should have run.
	if result.Output != "delivered" {
		t.Errorf("Output = %q, want %q", result.Output, "delivered")
	}
}

func TestWorkflowSkippedByConditionDoesNotCascadeFailure(t *testing.T) {
	// When a step is skipped by When() condition, its dependents should still run.
	wf, err := NewWorkflow("cond-cascade", "condition cascade test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
		Step("b", func(_ context.Context, _ *WorkflowContext) error {
			return nil
		}, After("a"), When(func(_ *WorkflowContext) bool { return false })), // always skipped
		Step("c", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("c.output", "c-ran")
			return nil
		}, After("b")), // should still run because b was skipped by condition, not failure
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "c-ran" {
		t.Errorf("Output = %q, want %q", result.Output, "c-ran")
	}
}

// --- Failure cascade tests ---

func TestWorkflowFailFast(t *testing.T) {
	stepErr := errors.New("step b exploded")
	cRan := false

	wf, err := NewWorkflow("fail", "fail-fast test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
		Step("b", func(_ context.Context, _ *WorkflowContext) error {
			return stepErr
		}, After("a")),
		Step("c", func(_ context.Context, _ *WorkflowContext) error {
			cRan = true
			return nil
		}, After("b")),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wf.Execute(context.Background(), AgentTask{Input: "fail"})

	var wfErr *WorkflowError
	if !errors.As(err, &wfErr) {
		t.Fatalf("expected *WorkflowError, got %v", err)
	}
	if wfErr.StepName != "b" {
		t.Errorf("WorkflowError.StepName = %q, want %q", wfErr.StepName, "b")
	}
	if !errors.Is(err, stepErr) {
		t.Errorf("Unwrap chain should contain stepErr, got %v", wfErr.Err)
	}
	if cRan {
		t.Error("step c should not have run after b failed")
	}
}

func TestWorkflowFailureCascadesThroughMultipleLevels(t *testing.T) {
	// A -> B (fails) -> C -> D
	// C and D should both be skipped.
	dRan := false
	cRan := false

	wf, err := NewWorkflow("cascade", "failure cascade test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
		Step("b", func(_ context.Context, _ *WorkflowContext) error {
			return errors.New("b failed")
		}, After("a")),
		Step("c", func(_ context.Context, _ *WorkflowContext) error {
			cRan = true
			return nil
		}, After("b")),
		Step("d", func(_ context.Context, _ *WorkflowContext) error {
			dRan = true
			return nil
		}, After("c")),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wf.Execute(context.Background(), AgentTask{Input: "test"})

	var wfErr *WorkflowError
	if !errors.As(err, &wfErr) {
		t.Fatalf("expected *WorkflowError, got %v", err)
	}
	if cRan {
		t.Error("step c should not have run (upstream b failed)")
	}
	if dRan {
		t.Error("step d should not have run (upstream b->c cascade)")
	}
}

// --- Retry tests ---

func TestWorkflowRetry(t *testing.T) {
	attempts := 0

	wf, err := NewWorkflow("retry", "retry test",
		Step("flaky", func(_ context.Context, wCtx *WorkflowContext) error {
			attempts++
			if attempts < 3 {
				return errors.New("transient error")
			}
			wCtx.Set("flaky.output", "recovered")
			return nil
		}, Retry(2, time.Millisecond)),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if result.Output != "recovered" {
		t.Errorf("Output = %q, want %q", result.Output, "recovered")
	}
}

func TestWorkflowRetryExhausted(t *testing.T) {
	attempts := 0

	wf, err := NewWorkflow("retry-fail", "retry exhausted test",
		Step("always-fail", func(_ context.Context, _ *WorkflowContext) error {
			attempts++
			return errors.New("permanent error")
		}, Retry(2, time.Millisecond)),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wf.Execute(context.Background(), AgentTask{Input: "go"})
	// 1 initial + 2 retries = 3.
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	var wfErr *WorkflowError
	if !errors.As(err, &wfErr) {
		t.Fatalf("expected *WorkflowError, got %v", err)
	}
}

func TestWorkflowDefaultRetry(t *testing.T) {
	attempts := 0

	wf, err := NewWorkflow("default-retry", "default retry test",
		Step("flaky", func(_ context.Context, wCtx *WorkflowContext) error {
			attempts++
			if attempts < 2 {
				return errors.New("transient")
			}
			wCtx.Set("flaky.output", "ok")
			return nil
		}),
		WithDefaultRetry(1, time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if result.Output != "ok" {
		t.Errorf("Output = %q, want %q", result.Output, "ok")
	}
}

// --- Callback tests ---

func TestWorkflowOnFinishCallback(t *testing.T) {
	var callbackResult WorkflowResult

	wf, err := NewWorkflow("callback", "callback test",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("a.output", "done")
			return nil
		}),
		WithOnFinish(func(r WorkflowResult) {
			callbackResult = r
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	wf.Execute(context.Background(), AgentTask{Input: "go"})

	if callbackResult.Status != StepSuccess {
		t.Errorf("callback status = %q, want %q", callbackResult.Status, StepSuccess)
	}
	if len(callbackResult.Steps) != 1 {
		t.Errorf("callback steps count = %d, want 1", len(callbackResult.Steps))
	}
}

func TestWorkflowOnErrorCallback(t *testing.T) {
	var errorStep string
	var errorErr error

	wf, err := NewWorkflow("error-cb", "error callback test",
		Step("fail", func(_ context.Context, _ *WorkflowContext) error {
			return errors.New("boom")
		}),
		WithOnError(func(step string, err error) {
			errorStep = step
			errorErr = err
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	wf.Execute(context.Background(), AgentTask{Input: "go"})

	if errorStep != "fail" {
		t.Errorf("onError step = %q, want %q", errorStep, "fail")
	}
	if errorErr == nil || errorErr.Error() != "boom" {
		t.Errorf("onError err = %v, want boom", errorErr)
	}
}

func TestWorkflowCallbackPanicRecovery(t *testing.T) {
	wf, err := NewWorkflow("panic-cb", "panic callback test",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("a.output", "ok")
			return nil
		}),
		WithOnFinish(func(_ WorkflowResult) {
			panic("callback exploded")
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Should not panic.
	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "ok" {
		t.Errorf("Output = %q, want %q", result.Output, "ok")
	}
}

// --- OutputTo tests ---

func TestWorkflowOutputTo(t *testing.T) {
	agent := &stubAgent{
		name: "echo",
		desc: "Echoes input",
		fn: func(task AgentTask) (AgentResult, error) {
			return AgentResult{Output: task.Input}, nil
		},
	}

	wf, err := NewWorkflow("output-to", "output-to test",
		AgentStep("a", agent, OutputTo("custom_key")),
		Step("b", func(_ context.Context, wCtx *WorkflowContext) error {
			v, ok := wCtx.Get("custom_key")
			if !ok {
				return errors.New("custom_key not found")
			}
			wCtx.Set("b.output", fmt.Sprintf("got: %v", v))
			return nil
		}, After("a")),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "got: hello" {
		t.Errorf("Output = %q, want %q", result.Output, "got: hello")
	}
}

// --- Context cancellation tests ---

func TestWorkflowContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bRan := false

	wf, err := NewWorkflow("cancel", "cancellation test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error {
			cancel() // cancel context during step a
			return nil
		}),
		Step("b", func(_ context.Context, _ *WorkflowContext) error {
			bRan = true
			return nil
		}, After("a")),
	)
	if err != nil {
		t.Fatal(err)
	}

	wf.Execute(ctx, AgentTask{Input: "go"})
	if bRan {
		t.Error("step b should not have run after context cancellation")
	}
}

// --- Integration: Workflow as agent in Network ---

func TestWorkflowAsNetworkAgent(t *testing.T) {
	wf, err := NewWorkflow("inner", "Inner workflow",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("a.output", "workflow result: "+wCtx.Input())
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Use workflow as a subagent in a Network.
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_inner",
				Args: json.RawMessage(`{"task":"do the thing"}`),
			}}},
			{Content: "network got workflow result"},
		},
	}

	network := NewNetwork("outer", "Outer network", router, WithAgents(wf))
	result, err := network.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "network got workflow result" {
		t.Errorf("Output = %q, want %q", result.Output, "network got workflow result")
	}
}
