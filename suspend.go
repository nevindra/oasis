package oasis

import (
	"context"
	"encoding/json"
	"fmt"
)

// errSuspend is the internal sentinel returned by step functions to signal
// that execution should pause for external input. The workflow/network engine
// catches it and converts to ErrSuspended with resume capabilities.
type errSuspend struct {
	payload json.RawMessage
}

func (e *errSuspend) Error() string { return "suspend" }

// Suspend returns an error that signals the workflow or network engine to
// pause execution. The payload provides context for the human (what they
// need to decide, what data to show).
func Suspend(payload json.RawMessage) error {
	return &errSuspend{payload: payload}
}

// ErrSuspended is returned by Execute() when a workflow step or network
// processor suspends execution to await external input.
// Inspect Payload for context, then call Resume() with the human's response.
type ErrSuspended struct {
	// Step is the name of the step or processor hook that suspended.
	Step string
	// Payload carries context for the human (what to show, what to decide).
	Payload json.RawMessage
	// resume is the closure that continues execution with human input.
	resume func(ctx context.Context, data json.RawMessage) (AgentResult, error)
}

func (e *ErrSuspended) Error() string {
	return fmt.Sprintf("suspended at step %q", e.Step)
}

// Resume continues execution with the human's response data.
// The data is made available to the step via ResumeData().
// Resume is single-use: calling it more than once is undefined behavior.
// Returns an error if called on an ErrSuspended not produced by the engine.
func (e *ErrSuspended) Resume(ctx context.Context, data json.RawMessage) (AgentResult, error) {
	if e.resume == nil {
		return AgentResult{}, fmt.Errorf("ErrSuspended: resume closure is nil (constructed outside engine)")
	}
	return e.resume(ctx, data)
}

// StepSuspended indicates a step that paused execution to await external input.
const StepSuspended StepStatus = "suspended"

// resumeDataKey is the reserved WorkflowContext key for resume data.
const resumeDataKey = "_resume_data"

// ResumeData retrieves resume data from the WorkflowContext.
// Returns the data and true if this step is being resumed, or nil and false
// on first execution. Safe to call with a nil WorkflowContext (returns nil, false).
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
