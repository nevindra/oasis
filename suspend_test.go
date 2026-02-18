package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestSuspendReturnsErrSuspend(t *testing.T) {
	payload := json.RawMessage(`{"action": "approve"}`)
	err := Suspend(payload)
	if err == nil {
		t.Fatal("Suspend should return non-nil error")
	}

	var s *errSuspend
	if !errors.As(err, &s) {
		t.Fatalf("expected errSuspend, got %T", err)
	}
	if string(s.payload) != `{"action": "approve"}` {
		t.Errorf("payload = %s, want %s", s.payload, `{"action": "approve"}`)
	}
}

func TestErrSuspendedError(t *testing.T) {
	e := &ErrSuspended{Step: "approval"}
	if e.Error() != `suspended at step "approval"` {
		t.Errorf("Error() = %q", e.Error())
	}
}

func TestErrSuspendedResume(t *testing.T) {
	called := false
	e := &ErrSuspended{
		Step:    "test",
		Payload: json.RawMessage(`{}`),
		resume: func(ctx context.Context, data json.RawMessage) (AgentResult, error) {
			called = true
			return AgentResult{Output: string(data)}, nil
		},
	}

	result, err := e.Resume(context.Background(), json.RawMessage(`{"ok":true}`))
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if !called {
		t.Error("resume func was not called")
	}
	if result.Output != `{"ok":true}` {
		t.Errorf("Output = %q", result.Output)
	}
}

func TestResumeDataNotPresent(t *testing.T) {
	wCtx := newWorkflowContext(AgentTask{Input: "test"})
	data, ok := ResumeData(wCtx)
	if ok {
		t.Error("ResumeData should return false when no resume data")
	}
	if data != nil {
		t.Error("ResumeData should return nil when no resume data")
	}
}

func TestResumeDataPresent(t *testing.T) {
	wCtx := newWorkflowContext(AgentTask{Input: "test"})
	wCtx.Set("_resume_data", json.RawMessage(`{"approved": true}`))

	data, ok := ResumeData(wCtx)
	if !ok {
		t.Error("ResumeData should return true when resume data is set")
	}
	if string(data) != `{"approved": true}` {
		t.Errorf("ResumeData = %s", data)
	}
}

func TestStepSuspendedStatus(t *testing.T) {
	if StepSuspended != "suspended" {
		t.Errorf("StepSuspended = %q, want %q", StepSuspended, "suspended")
	}
}

func TestWorkflowSuspendPayload(t *testing.T) {
	wf, _ := NewWorkflow("test", "test",
		Step("gate", func(_ context.Context, _ *WorkflowContext) error {
			return Suspend(json.RawMessage(`{"key": "value", "num": 42}`))
		}),
	)

	_, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(suspended.Payload, &payload); err != nil {
		t.Fatalf("failed to parse payload: %v", err)
	}
	if payload["key"] != "value" {
		t.Errorf("payload[key] = %v", payload["key"])
	}
	if payload["num"] != float64(42) {
		t.Errorf("payload[num] = %v", payload["num"])
	}
}

func TestWorkflowSuspendPreservesCompletedSteps(t *testing.T) {
	prepareCount := 0
	wf, _ := NewWorkflow("test", "test",
		Step("prepare", func(_ context.Context, wCtx *WorkflowContext) error {
			prepareCount++
			wCtx.Set("prepare.output", "done")
			return nil
		}),
		Step("gate", func(_ context.Context, wCtx *WorkflowContext) error {
			if _, ok := ResumeData(wCtx); ok {
				return nil
			}
			return Suspend(json.RawMessage(`{}`))
		}, After("prepare")),
	)

	_, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	_, err = suspended.Resume(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Resume error: %v", err)
	}

	// "prepare" should only have run once (not re-executed on resume).
	if prepareCount != 1 {
		t.Errorf("prepare ran %d times, want 1", prepareCount)
	}
}

func TestWorkflowMultiSuspend(t *testing.T) {
	// Each gate uses a call counter to distinguish first execution (suspend)
	// from resume, because ResumeData is workflow-global and gate2 would
	// otherwise see gate1's resume data.
	gate1Calls := 0
	gate2Calls := 0

	wf, _ := NewWorkflow("test", "test",
		Step("gate1", func(_ context.Context, wCtx *WorkflowContext) error {
			gate1Calls++
			if gate1Calls > 1 {
				wCtx.Set("gate1.output", "passed")
				return nil
			}
			return Suspend(json.RawMessage(`{"gate": 1}`))
		}),
		Step("gate2", func(_ context.Context, wCtx *WorkflowContext) error {
			gate2Calls++
			if gate2Calls > 1 {
				wCtx.Set("gate2.output", "passed")
				return nil
			}
			return Suspend(json.RawMessage(`{"gate": 2}`))
		}, After("gate1")),
	)

	// First suspend at gate1.
	_, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	var s1 *ErrSuspended
	if !errors.As(err, &s1) {
		t.Fatalf("expected first ErrSuspended, got %v", err)
	}
	if s1.Step != "gate1" {
		t.Errorf("first suspend step = %q, want gate1", s1.Step)
	}

	// Resume gate1 → gate2 suspends.
	_, err = s1.Resume(context.Background(), json.RawMessage(`{}`))
	var s2 *ErrSuspended
	if !errors.As(err, &s2) {
		t.Fatalf("expected second ErrSuspended, got %v", err)
	}
	if s2.Step != "gate2" {
		t.Errorf("second suspend step = %q, want gate2", s2.Step)
	}

	// Resume gate2 → workflow completes.
	result, err := s2.Resume(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("final Resume error: %v", err)
	}
	if result.Output != "passed" {
		t.Errorf("Output = %q, want %q", result.Output, "passed")
	}
}

func TestWorkflowSuspendResumeRejection(t *testing.T) {
	wf, _ := NewWorkflow("test", "test",
		Step("gate", func(_ context.Context, wCtx *WorkflowContext) error {
			if data, ok := ResumeData(wCtx); ok {
				var d struct {
					Approved bool `json:"approved"`
				}
				json.Unmarshal(data, &d)
				if !d.Approved {
					return fmt.Errorf("rejected")
				}
				return nil
			}
			return Suspend(json.RawMessage(`{}`))
		}),
	)

	_, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	var suspended *ErrSuspended
	errors.As(err, &suspended)

	_, err = suspended.Resume(context.Background(), json.RawMessage(`{"approved": false}`))
	if err == nil {
		t.Fatal("expected error on rejection")
	}

	var wfErr *WorkflowError
	if !errors.As(err, &wfErr) {
		t.Fatalf("expected WorkflowError, got %T: %v", err, err)
	}
}

func TestWorkflowSuspendNoCallbacks(t *testing.T) {
	onFinishCalled := false
	onErrorCalled := false

	wf, _ := NewWorkflow("test", "test",
		Step("gate", func(_ context.Context, _ *WorkflowContext) error {
			return Suspend(json.RawMessage(`{}`))
		}),
		WithOnFinish(func(_ WorkflowResult) { onFinishCalled = true }),
		WithOnError(func(_ string, _ error) { onErrorCalled = true }),
	)

	wf.Execute(context.Background(), AgentTask{Input: "go"})

	if onFinishCalled {
		t.Error("onFinish should not be called on suspend")
	}
	if onErrorCalled {
		t.Error("onError should not be called on suspend")
	}
}

func TestWorkflowSuspendContextCancellation(t *testing.T) {
	gateCalled := false

	wf, _ := NewWorkflow("test", "test",
		Step("gate", func(ctx context.Context, _ *WorkflowContext) error {
			// On resume, check the context first — a cancelled context
			// should prevent meaningful work.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			gateCalled = true
			return Suspend(json.RawMessage(`{}`))
		}),
	)

	_, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	var suspended *ErrSuspended
	errors.As(err, &suspended)

	// Reset to track the resume call.
	gateCalled = false

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// When the context is already cancelled, executeStep skips the step
	// before calling the step function (ctx.Err() check in executeStep).
	// The step is marked StepSkipped, which is not a failure, so err is nil.
	result, err := suspended.Resume(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The gate step function should NOT have been called.
	if gateCalled {
		t.Error("gate step should not execute with cancelled context")
	}

	// No output since the step was skipped.
	if result.Output != "" {
		t.Errorf("Output = %q, want empty", result.Output)
	}
}

func TestWorkflowSuspendAndResume(t *testing.T) {
	callCount := 0
	wf, err := NewWorkflow("test", "test workflow",
		Step("prepare", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("data", "prepared")
			return nil
		}),
		Step("gate", func(_ context.Context, wCtx *WorkflowContext) error {
			callCount++
			if data, ok := ResumeData(wCtx); ok {
				wCtx.Set("gate.output", "approved:"+string(data))
				return nil
			}
			return Suspend(json.RawMessage(`{"needs": "approval"}`))
		}, After("prepare")),
		Step("finish", func(_ context.Context, wCtx *WorkflowContext) error {
			v, _ := wCtx.Get("gate.output")
			wCtx.Set("finish.output", "done:"+v.(string))
			return nil
		}, After("gate")),
	)
	if err != nil {
		t.Fatalf("NewWorkflow: %v", err)
	}

	// First execution — should suspend at "gate".
	result, execErr := wf.Execute(context.Background(), AgentTask{Input: "go"})

	var suspended *ErrSuspended
	if !errors.As(execErr, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", execErr)
	}
	if suspended.Step != "gate" {
		t.Errorf("Step = %q, want %q", suspended.Step, "gate")
	}
	if string(suspended.Payload) != `{"needs": "approval"}` {
		t.Errorf("Payload = %s", suspended.Payload)
	}

	// Resume with approval data.
	result, execErr = suspended.Resume(context.Background(), json.RawMessage(`"yes"`))
	if execErr != nil {
		t.Fatalf("Resume returned error: %v", execErr)
	}

	// "finish" step should have run.
	if result.Output != `done:approved:"yes"` {
		t.Errorf("Output = %q", result.Output)
	}

	// "gate" should have been called twice: once for suspend, once for resume.
	if callCount != 2 {
		t.Errorf("gate called %d times, want 2", callCount)
	}
}
