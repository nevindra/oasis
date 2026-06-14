package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nevindra/oasis/core"
)

// --- Task 2.1: ErrSuspended.Resume / ResumeStream ---

// TestErrSuspendedResume suspends a workflow, recovers *ErrSuspended via
// errors.As, calls Resume with human data, and asserts the workflow completes
// with the resumed step's output.
func TestErrSuspendedResume(t *testing.T) {
	wf, err := New("resume", "resume test",
		Step("gate", func(_ context.Context, wCtx *WorkflowContext) error {
			if data, ok := ResumeData(wCtx); ok {
				wCtx.Set("gate.output", "approved:"+string(data))
				return nil
			}
			return Suspend([]byte(`{"reason":"needs_approval"}`))
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, execErr := wf.Execute(context.Background(), core.AgentTask{Input: "go"})

	var suspended *ErrSuspended
	if !errors.As(execErr, &suspended) {
		t.Fatalf("expected *ErrSuspended, got %v", execErr)
	}
	if suspended.Step != "gate" {
		t.Errorf("Step = %q, want %q", suspended.Step, "gate")
	}
	if string(suspended.Payload) != `{"reason":"needs_approval"}` {
		t.Errorf("Payload = %s", suspended.Payload)
	}

	result, err := suspended.Resume(context.Background(), json.RawMessage(`"yes"`))
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if result.Output != `approved:"yes"` {
		t.Errorf("Output = %q, want %q", result.Output, `approved:"yes"`)
	}
}

// ResumeData lives in the agent package; the workflow package stores resume
// data under resumeDataKey. Provide a local accessor mirroring that contract.
func ResumeData(wCtx *WorkflowContext) (json.RawMessage, bool) {
	if wCtx == nil {
		return nil, false
	}
	v, ok := wCtx.Get(resumeDataKey)
	if !ok {
		return nil, false
	}
	data, ok := v.(json.RawMessage)
	return data, ok
}

// TestErrSuspendedResumeStream resumes a suspended workflow with streaming and
// asserts the workflow completes and the channel is closed.
func TestErrSuspendedResumeStream(t *testing.T) {
	wf, err := New("resume-stream", "resume stream test",
		Step("gate", func(_ context.Context, wCtx *WorkflowContext) error {
			if data, ok := ResumeData(wCtx); ok {
				wCtx.Set("gate.output", "approved:"+string(data))
				return nil
			}
			return Suspend([]byte(`{"reason":"approval"}`))
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, execErr := wf.Execute(context.Background(), core.AgentTask{Input: "go"})

	var suspended *ErrSuspended
	if !errors.As(execErr, &suspended) {
		t.Fatalf("expected *ErrSuspended, got %v", execErr)
	}

	ch := make(chan core.StreamEvent, 32)
	result, err := suspended.ResumeStream(context.Background(), json.RawMessage(`"ok"`), ch)
	if err != nil {
		t.Fatalf("ResumeStream returned error: %v", err)
	}
	if result.Output != `approved:"ok"` {
		t.Errorf("Output = %q, want %q", result.Output, `approved:"ok"`)
	}

	// Channel must be closed by ResumeStream — the range terminates only if
	// the producer closed ch. (Step/finish events are emitted but optional.)
	for range ch {
	}
}

// TestErrSuspendedDoubleResume asserts the second Resume call returns an error
// rather than re-executing (sync.Once guard).
func TestErrSuspendedDoubleResume(t *testing.T) {
	wf, err := New("double-resume", "double resume test",
		Step("gate", func(_ context.Context, wCtx *WorkflowContext) error {
			if data, ok := ResumeData(wCtx); ok {
				wCtx.Set("gate.output", "approved:"+string(data))
				return nil
			}
			return Suspend([]byte(`{}`))
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, execErr := wf.Execute(context.Background(), core.AgentTask{Input: "go"})
	var suspended *ErrSuspended
	if !errors.As(execErr, &suspended) {
		t.Fatalf("expected *ErrSuspended, got %v", execErr)
	}

	if _, err := suspended.Resume(context.Background(), json.RawMessage(`"first"`)); err != nil {
		t.Fatalf("first Resume error: %v", err)
	}
	if _, err := suspended.Resume(context.Background(), json.RawMessage(`"second"`)); err == nil {
		t.Fatal("second Resume should return an error (single-use guard)")
	}
}

// --- Task 2.2: ErrOverridesUnsupported sentinel ---

// runOverrideStub implements core.RunOverrides for triggering the override
// rejection path in Execute.
type runOverrideStub struct{}

func (runOverrideStub) IsRunOverrides() {}

func withOverrideStub() core.RunOption {
	return func(c *core.RunConfig) { c.Overrides = runOverrideStub{} }
}

// TestExecuteOverridesUnsupported asserts that passing per-call overrides
// returns the exported ErrOverridesUnsupported sentinel (matchable via
// errors.Is) and closes any provided stream.
func TestExecuteOverridesUnsupported(t *testing.T) {
	wf, err := New("override", "override test",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("a.output", "done")
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	ch := make(chan core.StreamEvent, 1)
	_, execErr := wf.Execute(context.Background(), core.AgentTask{Input: "go"},
		withOverrideStub(), core.WithStream(ch))

	if !errors.Is(execErr, ErrOverridesUnsupported) {
		t.Fatalf("expected ErrOverridesUnsupported, got %v", execErr)
	}

	// Stream must be closed even on the rejection path.
	if _, open := <-ch; open {
		t.Error("stream channel should be closed on override rejection")
	}
}
