package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
	"github.com/nevindra/oasis/processor"
	"github.com/nevindra/oasis/workflow"
)

// --- Suspend test helpers ---

// suspendingProcessor is a PostProcessor that suspends on a trigger tool name.
type suspendingProcessor struct {
	triggerTool string
	payload     json.RawMessage
}

func (p *suspendingProcessor) PostLLM(_ context.Context, resp *core.ChatResponse) error {
	for _, tc := range resp.ToolCalls {
		if tc.Name == p.triggerTool {
			return &errSuspend{payload: p.payload}
		}
	}
	return nil
}

// suspendingPreProcessor is a PreProcessor that always suspends.
type suspendingPreProcessor struct {
	payload json.RawMessage
}

func (p *suspendingPreProcessor) PreLLM(_ context.Context, _ *core.ChatRequest) error {
	return &errSuspend{payload: p.payload}
}

// suspendingPostToolProcessor is a PostToolProcessor that suspends on a trigger tool.
type suspendingPostToolProcessor struct {
	triggerTool string
	payload     json.RawMessage
}

func (p *suspendingPostToolProcessor) PostTool(_ context.Context, call core.ToolCall, _ *core.ToolResult) error {
	if call.Name == p.triggerTool {
		return &errSuspend{payload: p.payload}
	}
	return nil
}

// suspendProcessor is a PostProcessor that returns Suspend on every call.
type suspendProcessor struct{}

func (suspendProcessor) PostLLM(_ context.Context, _ *core.ChatResponse) error {
	return &errSuspend{payload: json.RawMessage(`{"action":"approve"}`)}
}

// collectEvents drains ch into a slice and returns it.
// The channel must be closed by the producer; this blocks until that happens.
func collectEvents(ch <-chan core.StreamEvent) []core.StreamEvent {
	var out []core.StreamEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

// findEvent returns the first event whose Type matches t, plus its index.
// Returns (-1, zero value) if not found.
func findEvent(evs []core.StreamEvent, t core.StreamEventType) (int, core.StreamEvent) {
	for i, ev := range evs {
		if ev.Type == t {
			return i, ev
		}
	}
	return -1, core.StreamEvent{}
}

// formattingProcessor is a PostProcessor that suspends with a custom format function.
type formattingProcessor struct {
	tag    string
	format func(json.RawMessage) string
}

func (p *formattingProcessor) PostLLM(_ context.Context, _ *core.ChatResponse) error {
	return &errSuspend{
		payload: json.RawMessage(`{"q":"approve?"}`),
		tag:     p.tag,
		format:  p.format,
	}
}

// typedSuspendingPreProcessor uses a typed protocol to suspend via PreLLM.
// It suspends on every call. Use for tests that don't need the resumed run
// to reach the LLM (budget, TTL, payload-from, tag-mismatch checks).
type typedSuspendingPreProcessor[Req, Resp any] struct {
	protocol SuspendProtocol[Req, Resp]
	payload  Req
}

func (p *typedSuspendingPreProcessor[Req, Resp]) PreLLM(_ context.Context, _ *core.ChatRequest) error {
	return p.protocol.Suspend(p.payload)
}

// typedSuspendingPostProcessor uses a typed protocol to suspend via PostLLM.
// It suspends on every call after an LLM response. Use for tests that need
// the resumed run to reach the LLM (e.g. to verify onChat captures).
type typedSuspendingPostProcessor[Req, Resp any] struct {
	protocol SuspendProtocol[Req, Resp]
	payload  Req
}

func (p *typedSuspendingPostProcessor[Req, Resp]) PostLLM(_ context.Context, _ *core.ChatResponse) error {
	return p.protocol.Suspend(p.payload)
}

// --- ErrSuspended / lifecycle ---

func TestRunLoopPostProcessorSuspend(t *testing.T) {
	provider := &mockProvider{
		responses: []core.ChatResponse{
			{Content: "", ToolCalls: []core.ToolCall{{ID: "1", Name: "dangerous_action", Args: json.RawMessage(`{}`)}}},
		},
	}

	chain := processor.NewChain()
	chain.AddPost(&suspendingProcessor{
		triggerTool: "dangerous_action",
		payload:     json.RawMessage(`{"action": "approve_dangerous_action"}`),
	})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Tools:      []core.ToolDefinition{{Name: "dangerous_action", Description: "test"}},
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, tc core.ToolCall) DispatchResult { return DispatchResult{Content: "ok"} },
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
		responses: []core.ChatResponse{
			// First call: LLM wants to call dangerous tool.
			{Content: "", ToolCalls: []core.ToolCall{{ID: "1", Name: "delete", Args: json.RawMessage(`{}`)}}},
			// After resume: LLM sees human input and responds.
			{Content: "Action completed with approval"},
		},
	}

	chain := processor.NewChain()
	chain.AddPost(&suspendingProcessor{
		triggerTool: "delete",
		payload:     json.RawMessage(`{"confirm": "delete?"}`),
	})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Tools:      []core.ToolDefinition{{Name: "delete", Description: "test"}},
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, tc core.ToolCall) DispatchResult { return DispatchResult{Content: "deleted"} },
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
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "danger", Args: json.RawMessage(`{}`)}}},
		},
	}

	chain := processor.NewChain()
	chain.AddPost(&suspendingProcessor{triggerTool: "danger", payload: json.RawMessage(`{}`)})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Tools:      []core.ToolDefinition{{Name: "danger", Description: "test"}},
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, tc core.ToolCall) DispatchResult { return DispatchResult{Content: "ok"} },
	}

	ch := make(chan core.StreamEvent, 10)
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
		responses: []core.ChatResponse{{Content: "should not reach"}},
	}

	chain := processor.NewChain()
	chain.AddPre(&suspendingPreProcessor{
		payload: json.RawMessage(`{"gate": "pre"}`),
	})

	cfg := LoopConfig{
		Name:       "test-pre",
		Provider:   provider,
		Tools:      []core.ToolDefinition{{Name: "some_tool", Description: "test"}},
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, tc core.ToolCall) DispatchResult { return DispatchResult{Content: "ok"} },
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
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "risky_tool", Args: json.RawMessage(`{}`)}}},
		},
	}

	chain := processor.NewChain()
	chain.AddPostTool(&suspendingPostToolProcessor{
		triggerTool: "risky_tool",
		payload:     json.RawMessage(`{"gate": "post_tool"}`),
	})

	cfg := LoopConfig{
		Name:       "test-posttool",
		Provider:   provider,
		Tools:      []core.ToolDefinition{{Name: "risky_tool", Description: "test"}},
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, tc core.ToolCall) DispatchResult { return DispatchResult{Content: "executed"} },
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

// --- Resume data ---

func TestResumeDataNotPresent(t *testing.T) {
	var gotData json.RawMessage
	var gotOK bool
	wf, err := workflow.New("test", "test resume data not present",
		workflow.Step("check", func(_ context.Context, wCtx *workflow.WorkflowContext) error {
			gotData, gotOK = ResumeData(wCtx)
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wf.Execute(context.Background(), core.AgentTask{Input: "test"}); err != nil {
		t.Fatal(err)
	}
	if gotOK {
		t.Error("ResumeData should return false when no resume data")
	}
	if gotData != nil {
		t.Error("ResumeData should return nil when no resume data")
	}
}

func TestResumeDataPresent(t *testing.T) {
	var gotData json.RawMessage
	var gotOK bool
	wf, err := workflow.New("test", "test resume data present",
		workflow.Step("check", func(_ context.Context, wCtx *workflow.WorkflowContext) error {
			wCtx.Set("_resume_data", json.RawMessage(`{"approved": true}`))
			gotData, gotOK = ResumeData(wCtx)
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wf.Execute(context.Background(), core.AgentTask{Input: "test"}); err != nil {
		t.Fatal(err)
	}
	if !gotOK {
		t.Error("ResumeData should return true when resume data is set")
	}
	if string(gotData) != `{"approved": true}` {
		t.Errorf("ResumeData = %s", gotData)
	}
}

func TestStepSuspendedStatus(t *testing.T) {
	if StepSuspended != "suspended" {
		t.Errorf("StepSuspended = %q, want %q", StepSuspended, "suspended")
	}
}

// --- TTL + budgets ---

func TestSuspendBudgetExceeded(t *testing.T) {
	// Provider returns a final text response (no tool calls), triggering
	// the PostProcessor hook each time. The processor suspends every time.
	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{Content: "response1"},
			{Content: "response2"},
			{Content: "response3"},
		},
	}

	agent := New("suspender", "Suspends a lot", provider,
		WithProcessors(Processors{Post: []core.PostProcessor{suspendProcessor{}}}),
		WithLimits(Limits{MaxSuspendSnapshots: 2, MaxSuspendBytes: 1 << 30}), // max 2 snapshots, generous byte limit
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

func TestTypedSuspendRespectsTTL(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	provider := &mockProvider{responses: []core.ChatResponse{{Content: ""}}}
	chain := processor.NewChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	suspended.WithSuspendTTL(20 * time.Millisecond)
	time.Sleep(60 * time.Millisecond)

	_, err = p.Resume(suspended, context.Background(), apResp{Approved: true})
	if err == nil {
		t.Fatal("expected resume to fail after TTL, got nil")
	}
	if !strings.Contains(err.Error(), "closure is nil") {
		t.Errorf("error %q does not look like a released-after-TTL error", err.Error())
	}
}

func TestTypedSuspendRespectsBudget(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	// Budget = 1 snapshot only.
	var count, bytesUsed int64
	var mu sync.Mutex

	provider := &mockProvider{responses: []core.ChatResponse{{Content: ""}, {Content: ""}, {Content: ""}}}
	chain := processor.NewChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		Name:         "test",
		Provider:     provider,
		Processors:   chain,
		Config:       Config{MaxIter: 5, MaxSuspendSnapshots: 1},
		Mem:          &memory.AgentMemory{},
		Dispatch:     func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
		SuspendCount: &count,
		SuspendBytes: &bytesUsed,
		SuspendMu:    &mu,
	}

	// First suspend lands inside the budget.
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}
	if count != 1 {
		t.Errorf("count after first suspend = %d, want 1", count)
	}

	// Second suspend should be over budget — checkSuspendLoop returns nil and
	// the original processor error propagates. Behavior should match the
	// existing untyped budget test.
	_, err = runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	if err == nil {
		t.Fatal("expected over-budget error, got nil")
	}
	var second *ErrSuspended
	if errors.As(err, &second) {
		t.Errorf("expected non-ErrSuspended over-budget error, got ErrSuspended")
	}
}

// --- Snapshot isolation ---

func TestSuspendSnapshotIsolation(t *testing.T) {
	// Verify that mutations to the original messages after checkSuspendLoop
	// do NOT affect the snapshot captured inside the resume closure.
	// We test this by constructing the scenario checkSuspendLoop handles:
	// deep-copying messages with ToolCalls, Args, Metadata, and Attachments.

	original := []core.ChatMessage{
		{
			Role:    "assistant",
			Content: "call tool",
			ToolCalls: []core.ToolCall{
				{ID: "1", Name: "search", Args: json.RawMessage(`{"q":"test"}`)},
			},
			Metadata: json.RawMessage(`{"trace":"abc"}`),
		},
		{
			Role:    "tool",
			Content: "result data",
			Attachments: []core.Attachment{
				{MimeType: "image/png", Data: []byte{0xFF, 0xD8}},
			},
		},
	}

	// Simulate the deep-copy logic from checkSuspendLoop.
	snapshot := make([]core.ChatMessage, len(original))
	for i, m := range original {
		snapshot[i] = m
		if len(m.ToolCalls) > 0 {
			snapshot[i].ToolCalls = make([]core.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				snapshot[i].ToolCalls[j] = tc
				if len(tc.Args) > 0 {
					snapshot[i].ToolCalls[j].Args = make(json.RawMessage, len(tc.Args))
					copy(snapshot[i].ToolCalls[j].Args, tc.Args)
				}
			}
		}
		if len(m.Attachments) > 0 {
			snapshot[i].Attachments = make([]core.Attachment, len(m.Attachments))
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
	original[1].Attachments = append(original[1].Attachments, core.Attachment{MimeType: "text/plain"})

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

// --- estimateSnapshotSize ---

func TestEstimateSnapshotSize(t *testing.T) {
	messages := []core.ChatMessage{
		{Content: "hello"},                                                          // 5 bytes
		{Content: "world", Metadata: json.RawMessage(`{"k":"v"}`)},                 // 5 + 9 = 14
		{
			Content: "",
			ToolCalls: []core.ToolCall{
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

// --- Events / stream accessors ---

func TestEventToolCallSuspendedFires(t *testing.T) {
	payload := json.RawMessage(`{"prompt":"approve?"}`)
	provider := &mockProvider{
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "tc1", Name: "transfer_money", Args: json.RawMessage(`{"amount":100}`)}}},
		},
	}
	chain := processor.NewChain()
	chain.AddPostTool(&suspendingPostToolProcessor{
		triggerTool: "transfer_money",
		payload:     payload,
	})

	cfg := LoopConfig{
		Name:     "test",
		Provider: provider,
		Tools:    []core.ToolDefinition{{Name: "transfer_money", Description: "move funds"}},
		Processors: chain,
		Config:   Config{MaxIter: 5},
		Mem:      &memory.AgentMemory{},
		Dispatch: func(_ context.Context, tc core.ToolCall) DispatchResult { return DispatchResult{Content: "ok"} },
	}

	ch := make(chan core.StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	evs := collectEvents(ch)

	idx, ev := findEvent(evs, core.EventToolCallSuspended)
	if idx < 0 {
		t.Fatalf("core.EventToolCallSuspended not found in %d events", len(evs))
	}
	if ev.ID != "tc1" {
		t.Errorf("ID = %q, want %q", ev.ID, "tc1")
	}
	if ev.Name != "transfer_money" {
		t.Errorf("Name = %q, want %q", ev.Name, "transfer_money")
	}
	if string(ev.Args) != `{"amount":100}` {
		t.Errorf("Args = %s, want {\"amount\":100}", ev.Args)
	}
	if ev.Protocol != "" {
		t.Errorf("Protocol = %q, want empty (untyped Suspend)", ev.Protocol)
	}
	if string(ev.SuspendPayload) != `{"prompt":"approve?"}` {
		t.Errorf("SuspendPayload = %s, want {\"prompt\":\"approve?\"}", ev.SuspendPayload)
	}
}

// --------------------------------------------------------------------------
// B. EventStepSuspended fires in workflow — lives in workflow/exec_test.go
// (see TestWorkflowStepSuspendedEventFires there)
// --------------------------------------------------------------------------

func TestEventProcessorSuspendedFiresPreLLM(t *testing.T) {
	payload := json.RawMessage(`{"gate":"pre_check"}`)
	provider := &mockProvider{
		responses: []core.ChatResponse{{Content: "never reached"}},
	}
	chain := processor.NewChain()
	chain.AddPre(&suspendingPreProcessor{payload: payload})

	cfg := LoopConfig{
		Name:       "test-pre",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}

	ch := make(chan core.StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	evs := collectEvents(ch)

	idx, ev := findEvent(evs, core.EventProcessorSuspended)
	if idx < 0 {
		t.Fatalf("core.EventProcessorSuspended not found in %d events", len(evs))
	}
	if ev.Content != "pre" {
		t.Errorf("Content = %q, want %q", ev.Content, "pre")
	}
	if ev.Protocol != "" {
		t.Errorf("Protocol = %q, want empty (untyped Suspend)", ev.Protocol)
	}
	if string(ev.SuspendPayload) != `{"gate":"pre_check"}` {
		t.Errorf("SuspendPayload = %s, want {\"gate\":\"pre_check\"}", ev.SuspendPayload)
	}
}

func TestEventProcessorSuspendedFiresPostLLM(t *testing.T) {
	provider := &mockProvider{
		responses: []core.ChatResponse{
			// PostLLM suspends when it sees a tool call.
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "risky", Args: json.RawMessage(`{}`)}}},
		},
	}
	chain := processor.NewChain()
	chain.AddPost(&suspendingProcessor{
		triggerTool: "risky",
		payload:     json.RawMessage(`{"confirm":"yes?"}`),
	})

	cfg := LoopConfig{
		Name:     "test-post",
		Provider: provider,
		Tools:    []core.ToolDefinition{{Name: "risky", Description: "risky op"}},
		Processors: chain,
		Config:   Config{MaxIter: 5},
		Mem:      &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}

	ch := make(chan core.StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	evs := collectEvents(ch)

	idx, ev := findEvent(evs, core.EventProcessorSuspended)
	if idx < 0 {
		t.Fatalf("core.EventProcessorSuspended not found in %d events", len(evs))
	}
	if ev.Content != "post" {
		t.Errorf("Content = %q, want %q", ev.Content, "post")
	}
	if ev.Protocol != "" {
		t.Errorf("Protocol = %q, want empty (untyped Suspend)", ev.Protocol)
	}
}

// Note: EventRunStart is emitted by LLMAgent above runLoop, not by runLoop
// itself. The ordering test therefore starts from core.EventIterationStart.

func TestEventOrderingOnToolSuspend(t *testing.T) {
	provider := &mockProvider{
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "t1", Name: "risky_op", Args: json.RawMessage(`{}`)}}},
		},
	}
	chain := processor.NewChain()
	chain.AddPostTool(&suspendingPostToolProcessor{
		triggerTool: "risky_op",
		payload:     json.RawMessage(`{"ask":"ok?"}`),
	})

	cfg := LoopConfig{
		Name:     "test-order",
		Provider: provider,
		Tools:    []core.ToolDefinition{{Name: "risky_op", Description: "test"}},
		Processors: chain,
		Config:   Config{MaxIter: 5},
		Mem:      &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{Content: "done"} },
	}

	ch := make(chan core.StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	evs := collectEvents(ch)

	indexOf := func(typ core.StreamEventType) int {
		i, _ := findEvent(evs, typ)
		return i
	}

	iIterStart := indexOf(core.EventIterationStart)
	iSuspended := indexOf(core.EventToolCallSuspended)
	iIterFinish := indexOf(core.EventIterationFinish)
	iRunFinish := indexOf(core.EventRunFinish)

	if iIterStart < 0 {
		t.Fatal("core.EventIterationStart not found")
	}
	if iSuspended < 0 {
		t.Fatal("core.EventToolCallSuspended not found")
	}
	if iIterFinish < 0 {
		t.Fatal("core.EventIterationFinish not found")
	}
	if iRunFinish < 0 {
		t.Fatal("core.EventRunFinish not found")
	}

	// Strict ordering: IterationStart < ToolCallSuspended < IterationFinish < RunFinish.
	if !(iIterStart < iSuspended) {
		t.Errorf("want core.EventIterationStart (%d) < core.EventToolCallSuspended (%d)", iIterStart, iSuspended)
	}
	if !(iSuspended < iIterFinish) {
		t.Errorf("want core.EventToolCallSuspended (%d) < core.EventIterationFinish (%d)", iSuspended, iIterFinish)
	}
	if !(iIterFinish < iRunFinish) {
		t.Errorf("want core.EventIterationFinish (%d) < core.EventRunFinish (%d)", iIterFinish, iRunFinish)
	}

	// IterationFinish and RunFinish must carry core.FinishSuspended.
	_, iterFinishEv := findEvent(evs, core.EventIterationFinish)
	if iterFinishEv.FinishReason != core.FinishSuspended {
		t.Errorf("core.EventIterationFinish.FinishReason = %q, want %q", iterFinishEv.FinishReason, core.FinishSuspended)
	}
	_, runFinishEv := findEvent(evs, core.EventRunFinish)
	if runFinishEv.FinishReason != core.FinishSuspended {
		t.Errorf("core.EventRunFinish.FinishReason = %q, want %q", runFinishEv.FinishReason, core.FinishSuspended)
	}
}

type evTestReq struct{ Action string }
type evTestResp struct{ OK bool }

func TestTypedProtocolPropagatesToEvents(t *testing.T) {
	protocol := NewSuspendProtocol[evTestReq, evTestResp]("approve_v1")

	pp := &typedSuspendingPreProcessor[evTestReq, evTestResp]{
		protocol: protocol,
		payload:  evTestReq{Action: "go"},
	}

	provider := &mockProvider{responses: []core.ChatResponse{{Content: "done"}}}
	chain := processor.NewChain()
	chain.AddPre(pp)

	cfg := LoopConfig{
		Name:       "test-typed",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}

	ch := make(chan core.StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	evs := collectEvents(ch)

	// core.EventProcessorSuspended must carry the typed protocol tag.
	idx, procEv := findEvent(evs, core.EventProcessorSuspended)
	if idx < 0 {
		t.Fatal("core.EventProcessorSuspended not found")
	}
	if procEv.Protocol != "approve_v1" {
		t.Errorf("core.EventProcessorSuspended.Protocol = %q, want %q", procEv.Protocol, "approve_v1")
	}

	// core.EventRunFinish must also carry the protocol tag.
	idx, runFinishEv := findEvent(evs, core.EventRunFinish)
	if idx < 0 {
		t.Fatal("core.EventRunFinish not found")
	}
	if runFinishEv.Protocol != "approve_v1" {
		t.Errorf("core.EventRunFinish.Protocol = %q, want %q", runFinishEv.Protocol, "approve_v1")
	}
}

func TestUntypedSuspendHasEmptyProtocolEverywhere(t *testing.T) {
	provider := &mockProvider{
		responses: []core.ChatResponse{{Content: "never"}},
	}
	chain := processor.NewChain()
	chain.AddPre(&suspendingPreProcessor{payload: json.RawMessage(`{"x":1}`)})

	cfg := LoopConfig{
		Name:       "test-untyped",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}

	ch := make(chan core.StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	evs := collectEvents(ch)

	for _, ev := range evs {
		switch ev.Type {
		case core.EventProcessorSuspended, core.EventToolCallSuspended, core.EventRunFinish:
			if ev.Protocol != "" {
				t.Errorf("event %q has Protocol = %q, want empty", ev.Type, ev.Protocol)
			}
		}
	}
}

func TestIterationTraceFinishReasonOnSuspend(t *testing.T) {
	provider := &mockProvider{
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "t1", Name: "op", Args: json.RawMessage(`{}`)}}},
		},
	}
	chain := processor.NewChain()
	chain.AddPostTool(&suspendingPostToolProcessor{
		triggerTool: "op",
		payload:     json.RawMessage(`{"ask":"confirm"}`),
	})

	cfg := LoopConfig{
		Name:     "test-iter-trace",
		Provider: provider,
		Tools:    []core.ToolDefinition{{Name: "op", Description: "test op"}},
		Processors: chain,
		Config:   Config{MaxIter: 5},
		Mem:      &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{Content: "ok"} },
	}

	ch := make(chan core.StreamEvent, 64)
	res, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	// Verify the result's iteration trace records the suspend.
	if len(res.Iterations) == 0 {
		t.Fatal("AgentResult.Iterations is empty; expected at least one trace")
	}
	last := res.Iterations[len(res.Iterations)-1]
	if last.FinishReason != core.FinishSuspended {
		t.Errorf("Iterations[last].FinishReason = %q, want %q", last.FinishReason, core.FinishSuspended)
	}

	// Cross-check the stream event carries the same reason.
	evs := collectEvents(ch)
	_, iterFinishEv := findEvent(evs, core.EventIterationFinish)
	if iterFinishEv.Type == "" {
		t.Fatal("core.EventIterationFinish not found in stream")
	}
	if iterFinishEv.FinishReason != core.FinishSuspended {
		t.Errorf("core.EventIterationFinish.FinishReason = %q, want %q", iterFinishEv.FinishReason, core.FinishSuspended)
	}
}

func TestAgentResultSuspendedAccessors(t *testing.T) {
	// Suspended run.
	r := AgentResult{FinishReason: core.FinishSuspended, SuspendProtocol: "foo"}
	if !r.Suspended() {
		t.Error("Suspended() = false, want true for core.FinishSuspended")
	}
	if r.SuspendedProtocol() != "foo" {
		t.Errorf("SuspendedProtocol() = %q, want %q", r.SuspendedProtocol(), "foo")
	}

	// Non-suspended run.
	r2 := AgentResult{FinishReason: core.FinishStop}
	if r2.Suspended() {
		t.Error("Suspended() = true, want false for FinishStop")
	}
	if r2.SuspendedProtocol() != "" {
		t.Errorf("SuspendedProtocol() = %q, want empty", r2.SuspendedProtocol())
	}
}

func TestStreamSuspendedAccessors(t *testing.T) {
	// Build a minimal Agent that returns a suspended AgentResult.
	suspendedResult := AgentResult{
		FinishReason:    core.FinishSuspended,
		SuspendProtocol: "",
	}

	ag := &emitterAgent{
		final: suspendedResult,
	}

	stream := Subscribe(context.Background(), ag, AgentTask{Input: "go"})
	// Wait for completion.
	<-stream.Done()

	if !stream.Suspended() {
		t.Error("Stream.Suspended() = false, want true")
	}
	if stream.SuspendedProtocol() != "" {
		t.Errorf("Stream.SuspendedProtocol() = %q, want empty", stream.SuspendedProtocol())
	}
}

func TestStreamSuspendedAccessorsTyped(t *testing.T) {
	suspendedResult := AgentResult{
		FinishReason:    core.FinishSuspended,
		SuspendProtocol: "my_protocol",
	}

	ag := &emitterAgent{final: suspendedResult}
	stream := Subscribe(context.Background(), ag, AgentTask{Input: "go"})
	<-stream.Done()

	if !stream.Suspended() {
		t.Error("Stream.Suspended() = false, want true")
	}
	if stream.SuspendedProtocol() != "my_protocol" {
		t.Errorf("Stream.SuspendedProtocol() = %q, want %q", stream.SuspendedProtocol(), "my_protocol")
	}
}

func TestStreamEventJSONOmitempty(t *testing.T) {
	// Plain event — no protocol/payload fields expected.
	ev := core.StreamEvent{Type: core.EventTextDelta, Content: "hi"}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if bytes.Contains(b, []byte(`"protocol"`)) {
		t.Errorf("unexpected \"protocol\" key in %s", b)
	}
	if bytes.Contains(b, []byte(`"suspend_payload"`)) {
		t.Errorf("unexpected \"suspend_payload\" key in %s", b)
	}

	// Suspend event — both fields must appear.
	ev2 := core.StreamEvent{
		Type:           core.EventToolCallSuspended,
		Protocol:       "tag",
		SuspendPayload: json.RawMessage(`{"x":1}`),
	}
	b2, err := json.Marshal(ev2)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if !bytes.Contains(b2, []byte(`"protocol":"tag"`)) {
		t.Errorf("expected \"protocol\":\"tag\" in %s", b2)
	}
	if !bytes.Contains(b2, []byte(`"suspend_payload"`)) {
		t.Errorf("expected \"suspend_payload\" key in %s", b2)
	}
}

func TestChannelClosesAfterRunFinish(t *testing.T) {
	provider := &mockProvider{
		responses: []core.ChatResponse{{Content: "nope"}},
	}
	chain := processor.NewChain()
	chain.AddPre(&suspendingPreProcessor{payload: json.RawMessage(`{}`)})

	cfg := LoopConfig{
		Name:       "test-close",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}

	ch := make(chan core.StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	// Drain all events — the for-range loop exits only when ch is closed.
	for range ch {
	}

	// Verify channel is actually closed (non-blocking read should get zero, false).
	_, ok := <-ch
	if ok {
		t.Error("channel should be closed after run finishes, but read succeeded")
	}
}

// --- Typed protocol construction + format ---

type apReq struct{ Amount float64 }
type apResp struct{ Approved bool }

func TestUntypedSuspendHasEmptyTag(t *testing.T) {
	provider := &mockProvider{
		responses: []core.ChatResponse{{Content: "done"}},
	}

	chain := processor.NewChain()
	chain.AddPre(&suspendingPreProcessor{
		payload: json.RawMessage(`{"prompt": "ok?"}`),
	})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}

	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}
	if suspended.tag != "" {
		t.Errorf("untyped suspend produced tag = %q, want empty string", suspended.tag)
	}
}

// TestSuspendFormatFnInjectsCustomMessage verifies that when errSuspend.format
// is set, the resume closure uses it instead of the default "Human input: <bytes>".
func TestSuspendFormatFnInjectsCustomMessage(t *testing.T) {
	var captured []core.ChatMessage
	provider := &mockProvider{
		responses: []core.ChatResponse{
			{Content: "first"},  // before suspend
			{Content: "second"}, // after resume — capture happens before this call
		},
		onChat: func(req *core.ChatRequest) { captured = append([]core.ChatMessage(nil), req.Messages...) },
	}

	chain := processor.NewChain()
	chain.AddPost(&formattingProcessor{
		tag: "approve_v1",
		format: func(data json.RawMessage) string {
			return "CUSTOM(" + string(data) + ")"
		},
	})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}

	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	// Resume and look at the last message that hit the provider.
	// Resume may return another *ErrSuspended because formattingProcessor
	// always fires on PostLLM — that's fine; we only care about the captured
	// messages to verify the formatter ran.
	_, err = suspended.Resume(context.Background(), json.RawMessage(`{"ok":true}`))
	var resumedSuspension *ErrSuspended
	if err != nil && !errors.As(err, &resumedSuspension) {
		t.Fatalf("Resume() error = %v", err)
	}

	if len(captured) == 0 {
		t.Fatalf("provider never re-invoked after resume")
	}
	last := captured[len(captured)-1]
	want := `CUSTOM({"ok":true})`
	if last.Content != want {
		t.Errorf("resume message = %q, want %q", last.Content, want)
	}
}

func TestSuspendProtocolConstruction(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")
	if p.Name() != "approve_transfer" {
		t.Errorf("Name() = %q, want %q", p.Name(), "approve_transfer")
	}
}

func TestSuspendProtocolWithRenderResume(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer").
		WithRenderResume(func(r apResp) string {
			if r.Approved {
				return "approved"
			}
			return "denied"
		})

	// formatBytes is an unexported method — see Step 3.3.
	if got := p.formatBytes(json.RawMessage(`{"Approved":true}`)); got != "approved" {
		t.Errorf("formatBytes(approved) = %q, want %q", got, "approved")
	}
	if got := p.formatBytes(json.RawMessage(`{"Approved":false}`)); got != "denied" {
		t.Errorf("formatBytes(denied) = %q, want %q", got, "denied")
	}
}

func TestSuspendProtocolDefaultFormat(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")
	// No WithRenderResume → default formatter produces tagged JSON.
	got := p.formatBytes(json.RawMessage(`{"Approved":true}`))
	want := "Human resumed `approve_transfer`: {\"Approved\":true}"
	if got != want {
		t.Errorf("default formatBytes = %q, want %q", got, want)
	}
}

func TestSuspendProtocolSuspendReturnsTaggedSentinel(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	err := p.Suspend(apReq{Amount: 5000})
	if err == nil {
		t.Fatal("Suspend returned nil error")
	}

	var sus *errSuspend
	if !errors.As(err, &sus) {
		t.Fatalf("Suspend returned %T, want *errSuspend", err)
	}
	if sus.tag != "approve_transfer" {
		t.Errorf("tag = %q, want %q", sus.tag, "approve_transfer")
	}
	if sus.format == nil {
		t.Error("format func is nil; want non-nil from protocol")
	}

	// Payload bytes should be valid JSON of apReq.
	var got apReq
	if err := json.Unmarshal(sus.payload, &got); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if got.Amount != 5000 {
		t.Errorf("payload Amount = %v, want 5000", got.Amount)
	}
}

func TestSuspendProtocolSuspendMarshalFailure(t *testing.T) {
	// Use a Req that json.Marshal cannot encode (chan can't be marshaled).
	type bad struct{ Ch chan int }
	p := NewSuspendProtocol[bad, apResp]("bad")

	err := p.Suspend(bad{Ch: make(chan int)})
	if err == nil {
		t.Fatal("expected marshal error, got nil")
	}
	// Must NOT be an *errSuspend — marshal failed before construction.
	var sus *errSuspend
	if errors.As(err, &sus) {
		t.Error("got *errSuspend on marshal failure; expected a plain error")
	}
}

// --- Typed protocol Resume / PayloadFrom ---

func TestPayloadFromReturnsTypedReq(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	// Construct an ErrSuspended directly via the engine path.
	provider := &mockProvider{responses: []core.ChatResponse{{Content: ""}}}
	chain := processor.NewChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 7500}})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	got, err := p.PayloadFrom(suspended)
	if err != nil {
		t.Fatalf("PayloadFrom error = %v", err)
	}
	if got.Amount != 7500 {
		t.Errorf("Amount = %v, want 7500", got.Amount)
	}
}

func TestPayloadFromNilErr(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")
	_, err := p.PayloadFrom(nil)
	if err == nil {
		t.Fatal("expected error on nil suspension")
	}
}

func TestPayloadFromTagMismatch(t *testing.T) {
	pA := NewSuspendProtocol[apReq, apResp]("protocol_A")
	pB := NewSuspendProtocol[apReq, apResp]("protocol_B")

	// Suspend via A, query via B.
	provider := &mockProvider{responses: []core.ChatResponse{{Content: ""}}}
	chain := processor.NewChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: pA, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	_, err = pB.PayloadFrom(suspended)
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "protocol_A") || !strings.Contains(msg, "protocol_B") {
		t.Errorf("error %q does not contain both protocol names", msg)
	}
}

func TestResumeAppliesRenderResume(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer").
		WithRenderResume(func(r apResp) string {
			if r.Approved {
				return "Human approved the transfer."
			}
			return "Human declined the transfer."
		})

	var captured []core.ChatMessage
	provider := &mockProvider{
		responses: []core.ChatResponse{{Content: "first"}, {Content: "second"}},
		onChat:    func(req *core.ChatRequest) { captured = append([]core.ChatMessage(nil), req.Messages...) },
	}
	chain := processor.NewChain()
	// Use PostProcessor so the LLM call happens before suspension.
	// onChat fires on each ChatStream call; captured will contain messages
	// from the resumed run (second ChatStream call), including the formatted message.
	chain.AddPost(&typedSuspendingPostProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 100}})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	// Resume may return another *ErrSuspended because the PostProcessor fires
	// again after the second LLM call. That's expected — we only care that
	// the resumed run reached the LLM (onChat was called) and that the
	// formatted message was injected.
	_, err = p.Resume(suspended, context.Background(), apResp{Approved: true})
	var resumedSuspension *ErrSuspended
	if err != nil && !errors.As(err, &resumedSuspension) {
		t.Fatalf("Resume error = %v", err)
	}
	if len(captured) == 0 {
		t.Fatal("provider never re-invoked after Resume")
	}
	last := captured[len(captured)-1]
	if last.Content != "Human approved the transfer." {
		t.Errorf("resume message = %q, want %q", last.Content, "Human approved the transfer.")
	}
}

func TestResumeTagMismatch(t *testing.T) {
	pA := NewSuspendProtocol[apReq, apResp]("protocol_A")
	pB := NewSuspendProtocol[apReq, apResp]("protocol_B")

	// Use PostProcessor so that pA.Resume can complete successfully (the LLM
	// call happens after the PostLLM suspension, so the resumed run reaches
	// the LLM without re-suspending).
	provider := &mockProvider{responses: []core.ChatResponse{{Content: ""}, {Content: "ok"}}}
	chain := processor.NewChain()
	chain.AddPost(&typedSuspendingPostProcessor[apReq, apResp]{protocol: pA, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	_, err = pB.Resume(suspended, context.Background(), apResp{Approved: true})
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "protocol_A") || !strings.Contains(err.Error(), "protocol_B") {
		t.Errorf("error %q does not contain both protocol names", err.Error())
	}

	// The original suspended should still be resumable via the correct protocol.
	// (The resumed run may re-suspend via PostLLM — that's OK; we only verify
	// that pA.Resume doesn't return a protocol-mismatch or unexpected error.)
	_, err = pA.Resume(suspended, context.Background(), apResp{Approved: true})
	var pAResumeSuspension *ErrSuspended
	if err != nil && !errors.As(err, &pAResumeSuspension) {
		t.Errorf("Resume via correct protocol failed: %v", err)
	}
}

func TestResumeStreamDeliversEvents(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	provider := &mockProvider{responses: []core.ChatResponse{{Content: ""}, {Content: "done"}}}
	chain := processor.NewChain()
	// Use PostProcessor so the resumed run reaches the LLM and the engine
	// closes resumeCh after ResumeStream returns.
	chain.AddPost(&typedSuspendingPostProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}
	ch := make(chan core.StreamEvent, 16)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)
	// Drain the initial run's channel (runLoop already closed it).
	for range ch {
	}
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	resumeCh := make(chan core.StreamEvent, 16)
	_, err = p.ResumeStream(suspended, context.Background(), apResp{Approved: true}, resumeCh)
	// The PostProcessor fires again after the resumed LLM call, producing
	// another *ErrSuspended. Accept that as OK — the key assertion is that
	// the channel is (or will be) closed by the engine.
	var resumeSuspension *ErrSuspended
	if err != nil && !errors.As(err, &resumeSuspension) {
		t.Fatalf("ResumeStream error = %v", err)
	}
	// Drain until the engine closes resumeCh.
	for range resumeCh {
	}
}

// --- Untyped-protocol propagation ---

// TestPayloadFromOnUntypedSuspend ensures the runtime tag check rejects a
// query via a protocol when the suspension came from the untyped path.
func TestPayloadFromOnUntypedSuspend(t *testing.T) {
	provider := &mockProvider{responses: []core.ChatResponse{{Content: ""}}}
	chain := processor.NewChain()
	chain.AddPre(&suspendingPreProcessor{payload: json.RawMessage(`{"Amount": 1}`)})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")
	_, err = p.PayloadFrom(suspended)
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	// Untyped tag is "<untyped>" in the error message.
	if !strings.Contains(err.Error(), "<untyped>") || !strings.Contains(err.Error(), "approve_transfer") {
		t.Errorf("error %q missing expected descriptors", err.Error())
	}
}

// TestUntypedResumeOnTypedSuspendStillFormats verifies that bypassing the
// protocol and calling (*ErrSuspended).Resume directly on a typed-protocol
// suspension still runs the protocol's formatter (the formatter is captured
// in the closure at suspend time, not at resume time).
func TestUntypedResumeOnTypedSuspendStillFormats(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer").
		WithRenderResume(func(r apResp) string { return "FORMATTED" })

	var captured []core.ChatMessage
	provider := &mockProvider{
		responses: []core.ChatResponse{{Content: ""}, {Content: ""}},
		onChat:    func(req *core.ChatRequest) { captured = append([]core.ChatMessage(nil), req.Messages...) },
	}
	chain := processor.NewChain()
	// Use PostProcessor so the resumed run reaches ChatStream (onChat fires).
	chain.AddPost(&typedSuspendingPostProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	// Call the untyped method directly — protocol formatter should still apply.
	// The PostProcessor fires again after the second LLM call; accept *ErrSuspended.
	_, err = suspended.Resume(context.Background(), json.RawMessage(`{"Approved":true}`))
	var untypedResumeSuspension *ErrSuspended
	if err != nil && !errors.As(err, &untypedResumeSuspension) {
		t.Fatalf("Resume error = %v", err)
	}
	if len(captured) == 0 {
		t.Fatal("provider never re-invoked after Resume")
	}
	last := captured[len(captured)-1]
	if last.Content != "FORMATTED" {
		t.Errorf("resume message = %q, want %q", last.Content, "FORMATTED")
	}
}

// TestResumeIsSingleUse mirrors the existing single-use guarantee for the
// untyped path: a second Resume call after a successful first returns the
// "closure is nil" error.
func TestResumeIsSingleUse(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	provider := &mockProvider{responses: []core.ChatResponse{{Content: ""}, {Content: "done"}}}
	chain := processor.NewChain()
	// Use PostProcessor: resumed run reaches LLM and returns successfully.
	chain.AddPost(&typedSuspendingPostProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		Name:       "test",
		Provider:   provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem:        &memory.AgentMemory{},
		Dispatch:   func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
	}
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	// First Resume: PostProcessor fires after resumed LLM call — may return
	// another *ErrSuspended. The important thing is the resume closure was
	// consumed (nil'd out), so the second call must fail.
	_, err = p.Resume(suspended, context.Background(), apResp{Approved: true})
	var firstResumeSuspension *ErrSuspended
	if err != nil && !errors.As(err, &firstResumeSuspension) {
		t.Fatalf("first Resume failed: %v", err)
	}
	_, err = p.Resume(suspended, context.Background(), apResp{Approved: true})
	if err == nil {
		t.Fatal("second Resume should fail with closure-is-nil")
	}
	if !strings.Contains(err.Error(), "closure is nil") {
		t.Errorf("second Resume error = %q, expected 'closure is nil'", err.Error())
	}
}
