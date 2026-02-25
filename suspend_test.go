package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- Suspend test helpers ---

// suspendingProcessor is a PostProcessor that suspends on a trigger tool name.
type suspendingProcessor struct {
	triggerTool string
	payload     json.RawMessage
}

func (p *suspendingProcessor) PostLLM(_ context.Context, resp *ChatResponse) error {
	for _, tc := range resp.ToolCalls {
		if tc.Name == p.triggerTool {
			return Suspend(p.payload)
		}
	}
	return nil
}

// suspendingPreProcessor is a PreProcessor that always suspends.
type suspendingPreProcessor struct {
	payload json.RawMessage
}

func (p *suspendingPreProcessor) PreLLM(_ context.Context, _ *ChatRequest) error {
	return Suspend(p.payload)
}

// suspendingPostToolProcessor is a PostToolProcessor that suspends on a trigger tool.
type suspendingPostToolProcessor struct {
	triggerTool string
	payload     json.RawMessage
}

func (p *suspendingPostToolProcessor) PostTool(_ context.Context, call ToolCall, _ *ToolResult) error {
	if call.Name == p.triggerTool {
		return Suspend(p.payload)
	}
	return nil
}

// suspendProcessor is a PostProcessor that returns Suspend on every call.
type suspendProcessor struct{}

func (suspendProcessor) PostLLM(_ context.Context, _ *ChatResponse) error {
	return Suspend(json.RawMessage(`{"action":"approve"}`))
}

// --- runLoop suspend tests (from agent_suspend_test.go) ---

func TestRunLoopPostProcessorSuspend(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			{Content: "", ToolCalls: []ToolCall{{ID: "1", Name: "dangerous_action", Args: json.RawMessage(`{}`)}}},
		},
	}

	chain := NewProcessorChain()
	chain.Add(&suspendingProcessor{
		triggerTool: "dangerous_action",
		payload:     json.RawMessage(`{"action": "approve_dangerous_action"}`),
	})

	cfg := loopConfig{
		name:       "test",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "dangerous_action", Description: "test"}},
		processors: chain,
		maxIter:    5,
		mem:        &agentMemory{},
		dispatch:   func(_ context.Context, tc ToolCall) DispatchResult { return DispatchResult{Content: "ok"} },
	}

	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}
	if suspended.Step != "test" {
		t.Errorf("Step = %q, want %q", suspended.Step, "test")
	}
	if string(suspended.Payload) != `{"action": "approve_dangerous_action"}` {
		t.Errorf("Payload = %s", suspended.Payload)
	}
}

func TestRunLoopSuspendResume(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			// First call: LLM wants to call dangerous tool.
			{Content: "", ToolCalls: []ToolCall{{ID: "1", Name: "delete", Args: json.RawMessage(`{}`)}}},
			// After resume: LLM sees human input and responds.
			{Content: "Action completed with approval"},
		},
	}

	chain := NewProcessorChain()
	chain.Add(&suspendingProcessor{
		triggerTool: "delete",
		payload:     json.RawMessage(`{"confirm": "delete?"}`),
	})

	cfg := loopConfig{
		name:       "test",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "delete", Description: "test"}},
		processors: chain,
		maxIter:    5,
		mem:        &agentMemory{},
		dispatch:   func(_ context.Context, tc ToolCall) DispatchResult { return DispatchResult{Content: "deleted"} },
	}

	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "delete item"}, nil)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	result, err := suspended.Resume(context.Background(), json.RawMessage(`"approved"`))
	if err != nil {
		t.Fatalf("Resume error: %v", err)
	}
	if result.Output != "Action completed with approval" {
		t.Errorf("Output = %q", result.Output)
	}
}

func TestRunLoopSuspendClosesStreamChannel(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "danger", Args: json.RawMessage(`{}`)}}},
		},
	}

	chain := NewProcessorChain()
	chain.Add(&suspendingProcessor{triggerTool: "danger", payload: json.RawMessage(`{}`)})

	cfg := loopConfig{
		name:       "test",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "danger", Description: "test"}},
		processors: chain,
		maxIter:    5,
		mem:        &agentMemory{},
		dispatch:   func(_ context.Context, tc ToolCall) DispatchResult { return DispatchResult{Content: "ok"} },
	}

	ch := make(chan StreamEvent, 10)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	// Drain any lifecycle events and verify channel is closed.
	for range ch {
	}
}

func TestRunLoopPreProcessorSuspend(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{{Content: "should not reach"}},
	}

	chain := NewProcessorChain()
	chain.Add(&suspendingPreProcessor{
		payload: json.RawMessage(`{"gate": "pre"}`),
	})

	cfg := loopConfig{
		name:       "test-pre",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "some_tool", Description: "test"}},
		processors: chain,
		maxIter:    5,
		mem:        &agentMemory{},
		dispatch:   func(_ context.Context, tc ToolCall) DispatchResult { return DispatchResult{Content: "ok"} },
	}

	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}
	if suspended.Step != "test-pre" {
		t.Errorf("Step = %q, want %q", suspended.Step, "test-pre")
	}
	if string(suspended.Payload) != `{"gate": "pre"}` {
		t.Errorf("Payload = %s", suspended.Payload)
	}
}

func TestRunLoopPostToolProcessorSuspend(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "risky_tool", Args: json.RawMessage(`{}`)}}},
		},
	}

	chain := NewProcessorChain()
	chain.Add(&suspendingPostToolProcessor{
		triggerTool: "risky_tool",
		payload:     json.RawMessage(`{"gate": "post_tool"}`),
	})

	cfg := loopConfig{
		name:       "test-posttool",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "risky_tool", Description: "test"}},
		processors: chain,
		maxIter:    5,
		mem:        &agentMemory{},
		dispatch:   func(_ context.Context, tc ToolCall) DispatchResult { return DispatchResult{Content: "executed"} },
	}

	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}
	if suspended.Step != "test-posttool" {
		t.Errorf("Step = %q, want %q", suspended.Step, "test-posttool")
	}
	if string(suspended.Payload) != `{"gate": "post_tool"}` {
		t.Errorf("Payload = %s", suspended.Payload)
	}
}

// --- Suspend unit tests (from agent_test.go) ---

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

// --- Workflow suspend tests ---

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

// --- Suspend budget tests ---

func TestSuspendBudgetExceeded(t *testing.T) {
	// Provider returns a final text response (no tool calls), triggering
	// the PostProcessor hook each time. The processor suspends every time.
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{Content: "response1"},
			{Content: "response2"},
			{Content: "response3"},
		},
	}

	agent := NewLLMAgent("suspender", "Suspends a lot", provider,
		WithProcessors(suspendProcessor{}),
		WithSuspendBudget(2, 1<<30), // max 2 snapshots, generous byte limit
	)

	// First suspension — should succeed.
	_, err1 := agent.Execute(context.Background(), AgentTask{Input: "one"})
	var s1 *ErrSuspended
	if !errors.As(err1, &s1) {
		t.Fatalf("call 1: expected ErrSuspended, got %v", err1)
	}

	// Second suspension — should succeed (count=2 now).
	_, err2 := agent.Execute(context.Background(), AgentTask{Input: "two"})
	var s2 *ErrSuspended
	if !errors.As(err2, &s2) {
		t.Fatalf("call 2: expected ErrSuspended, got %v", err2)
	}

	// Third suspension — budget exceeded, should NOT be ErrSuspended.
	// It should propagate the underlying processor error instead.
	_, err3 := agent.Execute(context.Background(), AgentTask{Input: "three"})
	var s3 *ErrSuspended
	if errors.As(err3, &s3) {
		t.Fatal("call 3: should NOT get ErrSuspended when budget is exceeded")
	}
	// The error should be the raw errSuspend (not wrapped as ErrSuspended).
	if err3 == nil {
		t.Fatal("call 3: expected an error when budget is exceeded")
	}

	// Release first snapshot — frees one slot.
	s1.Release()

	// Fourth suspension — should succeed now (count back to 1).
	_, err4 := agent.Execute(context.Background(), AgentTask{Input: "four"})
	var s4 *ErrSuspended
	if !errors.As(err4, &s4) {
		t.Fatalf("call 4: expected ErrSuspended after release, got %v", err4)
	}
	s2.Release()
	s4.Release()
}

// --- TTL auto-release tests ---

func TestWithSuspendTTLAutoRelease(t *testing.T) {
	// Verify that WithSuspendTTL fires and nils out the resume closure,
	// preventing memory leaks from abandoned suspensions.
	resumed := make(chan struct{})
	e := &ErrSuspended{
		Step:    "test",
		Payload: json.RawMessage(`{}`),
		resume: func(_ context.Context, _ json.RawMessage) (AgentResult, error) {
			close(resumed)
			return AgentResult{Output: "should not happen"}, nil
		},
	}

	// Set a very short TTL.
	e.WithSuspendTTL(20 * time.Millisecond)

	// Wait for TTL to fire.
	time.Sleep(100 * time.Millisecond)

	// Resume should fail because the TTL released the closure.
	_, err := e.Resume(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Resume should fail after TTL expiry")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("error = %q, want mention of nil closure", err.Error())
	}

	// The resume func should never have been called.
	select {
	case <-resumed:
		t.Fatal("resume func should not be called by TTL — only Release()")
	default:
	}
}

func TestWithSuspendTTLOverridesPrevious(t *testing.T) {
	// Setting a new TTL should cancel the previous timer.
	e := &ErrSuspended{
		Step: "test",
		resume: func(_ context.Context, _ json.RawMessage) (AgentResult, error) {
			return AgentResult{Output: "ok"}, nil
		},
	}

	// Set a very short TTL that would fire quickly.
	e.WithSuspendTTL(10 * time.Millisecond)

	// Override with a much longer TTL before the first fires.
	e.WithSuspendTTL(10 * time.Second)

	// Wait long enough for the original TTL to have fired if not cancelled.
	time.Sleep(50 * time.Millisecond)

	// Resume should still work because we extended the TTL.
	result, err := e.Resume(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Resume should succeed after TTL override: %v", err)
	}
	if result.Output != "ok" {
		t.Errorf("Output = %q, want %q", result.Output, "ok")
	}
}

func TestWithSuspendTTLDecrementsBudget(t *testing.T) {
	// When TTL fires on a budgeted suspension, it should decrement
	// the suspend counter so new suspensions can proceed.
	var count atomic.Int64
	var bytes atomic.Int64
	count.Store(1)
	bytes.Store(100)

	e := &ErrSuspended{
		Step:         "test",
		snapshotSize: 100,
		resume: func(_ context.Context, _ json.RawMessage) (AgentResult, error) {
			return AgentResult{}, nil
		},
		onRelease: func(size int64) {
			count.Add(-1)
			bytes.Add(-size)
		},
	}

	e.WithSuspendTTL(10 * time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	if count.Load() != 0 {
		t.Errorf("suspendCount = %d, want 0 (TTL should decrement)", count.Load())
	}
	if bytes.Load() != 0 {
		t.Errorf("suspendBytes = %d, want 0 (TTL should decrement)", bytes.Load())
	}
}

// --- Snapshot isolation tests ---

func TestSuspendSnapshotIsolation(t *testing.T) {
	// Verify that mutations to the original messages after checkSuspendLoop
	// do NOT affect the snapshot captured inside the resume closure.
	// We test this by constructing the scenario checkSuspendLoop handles:
	// deep-copying messages with ToolCalls, Args, Metadata, and Attachments.

	original := []ChatMessage{
		{
			Role:    "assistant",
			Content: "call tool",
			ToolCalls: []ToolCall{
				{ID: "1", Name: "search", Args: json.RawMessage(`{"q":"test"}`)},
			},
			Metadata: json.RawMessage(`{"trace":"abc"}`),
		},
		{
			Role:    "tool",
			Content: "result data",
			Attachments: []Attachment{
				{MimeType: "image/png", Data: []byte{0xFF, 0xD8}},
			},
		},
	}

	// Simulate the deep-copy logic from checkSuspendLoop.
	snapshot := make([]ChatMessage, len(original))
	for i, m := range original {
		snapshot[i] = m
		if len(m.ToolCalls) > 0 {
			snapshot[i].ToolCalls = make([]ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				snapshot[i].ToolCalls[j] = tc
				if len(tc.Args) > 0 {
					snapshot[i].ToolCalls[j].Args = make(json.RawMessage, len(tc.Args))
					copy(snapshot[i].ToolCalls[j].Args, tc.Args)
				}
			}
		}
		if len(m.Attachments) > 0 {
			snapshot[i].Attachments = make([]Attachment, len(m.Attachments))
			copy(snapshot[i].Attachments, m.Attachments)
		}
		if len(m.Metadata) > 0 {
			snapshot[i].Metadata = make(json.RawMessage, len(m.Metadata))
			copy(snapshot[i].Metadata, m.Metadata)
		}
	}

	// Mutate the originals.
	original[0].Content = "MUTATED"
	original[0].ToolCalls[0].Args = json.RawMessage(`{"q":"MUTATED"}`)
	original[0].Metadata = json.RawMessage(`{"trace":"MUTATED"}`)
	original[1].Content = "MUTATED RESULT"
	original[1].Attachments = append(original[1].Attachments, Attachment{MimeType: "text/plain"})

	// Verify snapshot is unaffected.
	if snapshot[0].Content != "call tool" {
		t.Errorf("snapshot[0].Content = %q, want %q", snapshot[0].Content, "call tool")
	}
	if string(snapshot[0].ToolCalls[0].Args) != `{"q":"test"}` {
		t.Errorf("snapshot[0].ToolCalls[0].Args = %s, want %s", snapshot[0].ToolCalls[0].Args, `{"q":"test"}`)
	}
	if string(snapshot[0].Metadata) != `{"trace":"abc"}` {
		t.Errorf("snapshot[0].Metadata = %s, want %s", snapshot[0].Metadata, `{"trace":"abc"}`)
	}
	if snapshot[1].Content != "result data" {
		t.Errorf("snapshot[1].Content = %q, want %q", snapshot[1].Content, "result data")
	}
	if len(snapshot[1].Attachments) != 1 {
		t.Errorf("snapshot[1].Attachments len = %d, want 1 (append should not affect snapshot)", len(snapshot[1].Attachments))
	}
}

// --- Resume/Release edge cases ---

func TestResumeAfterReleaseReturnsError(t *testing.T) {
	e := &ErrSuspended{
		Step: "test",
		resume: func(_ context.Context, _ json.RawMessage) (AgentResult, error) {
			return AgentResult{Output: "should not happen"}, nil
		},
	}

	e.Release()

	_, err := e.Resume(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Resume after Release should return error")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("error = %q, want mention of nil closure", err.Error())
	}
}

func TestReleaseIdempotent(t *testing.T) {
	releaseCount := 0
	e := &ErrSuspended{
		Step:         "test",
		snapshotSize: 100,
		resume: func(_ context.Context, _ json.RawMessage) (AgentResult, error) {
			return AgentResult{}, nil
		},
		onRelease: func(_ int64) {
			releaseCount++
		},
	}

	e.Release()
	e.Release()
	e.Release()

	// onRelease should only be called once despite multiple Release() calls.
	if releaseCount != 1 {
		t.Errorf("onRelease called %d times, want 1 (should be idempotent)", releaseCount)
	}
}

func TestResumeNilsClosure(t *testing.T) {
	// After Resume, calling Resume again should fail (single-use).
	e := &ErrSuspended{
		Step: "test",
		resume: func(_ context.Context, _ json.RawMessage) (AgentResult, error) {
			return AgentResult{Output: "first"}, nil
		},
	}

	result, err := e.Resume(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("first Resume: %v", err)
	}
	if result.Output != "first" {
		t.Errorf("first Resume output = %q, want %q", result.Output, "first")
	}

	_, err = e.Resume(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("second Resume should fail (single-use)")
	}
}

func TestResumeDataNilContext(t *testing.T) {
	// ResumeData with nil WorkflowContext should return nil, false.
	data, ok := ResumeData(nil)
	if ok {
		t.Error("ResumeData(nil) should return false")
	}
	if data != nil {
		t.Error("ResumeData(nil) should return nil")
	}
}

// --- estimateSnapshotSize tests ---

func TestEstimateSnapshotSize(t *testing.T) {
	messages := []ChatMessage{
		{Content: "hello"},                             // 5 bytes
		{Content: "world", Metadata: json.RawMessage(`{"k":"v"}`)}, // 5 + 9 = 14
		{
			Content: "",
			ToolCalls: []ToolCall{
				{Args: json.RawMessage(`{"a":1}`), Metadata: json.RawMessage(`{"b":2}`)}, // 7 + 7 = 14
			},
		},
	}

	size := estimateSnapshotSize(messages)
	// 5 + 5 + 9 + 7 + 7 = 33
	if size != 33 {
		t.Errorf("estimateSnapshotSize = %d, want 33", size)
	}
}

func TestEstimateSnapshotSizeEmpty(t *testing.T) {
	size := estimateSnapshotSize(nil)
	if size != 0 {
		t.Errorf("estimateSnapshotSize(nil) = %d, want 0", size)
	}
}
