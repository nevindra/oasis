package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nevindra/oasis/core"
)

// fakeInputHandler captures the approval request and returns a canned response.
type fakeInputHandler struct {
	approve bool
	calls   int
	lastReq InputRequest
}

func (f *fakeInputHandler) RequestInput(ctx context.Context, req InputRequest) (InputResponse, error) {
	f.calls++
	f.lastReq = req
	if f.approve {
		return InputResponse{Value: "approve"}, nil
	}
	return InputResponse{Value: "deny"}, nil
}

func TestToolApproval_ApproveRunsTool(t *testing.T) {
	handler := &fakeInputHandler{approve: true}
	called := false

	ag := NewLLMAgent("test", "", &callbackProvider{},
		WithTools(&recordingTool{called: &called}),
		WithInputHandler(handler),
		WithToolApproval("rec"),
	)

	result, err := ag.tools.Execute(context.Background(), "rec", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if !called {
		t.Error("expected tool to run on approve")
	}
	if handler.calls != 1 {
		t.Errorf("InputHandler called %d times, want 1", handler.calls)
	}
	if result.Error != "" {
		t.Errorf("result.Error = %q, want empty", result.Error)
	}
}

func TestToolApproval_DenyAsksLLMToRevise(t *testing.T) {
	handler := &fakeInputHandler{approve: false}
	called := false

	ag := NewLLMAgent("test", "", &callbackProvider{},
		WithTools(&recordingTool{called: &called}),
		WithInputHandler(handler),
		WithToolApproval("rec"), // default OnDeny: DenyAskLLMToRevise
	)

	result, err := ag.tools.Execute(context.Background(), "rec", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if called {
		t.Error("tool should NOT have run on deny")
	}
	if result.Error == "" {
		t.Error("expected ToolResult.Error on deny so LLM can adapt")
	}
}

func TestToolApproval_DenyHaltReturnsErrHalt(t *testing.T) {
	handler := &fakeInputHandler{approve: false}
	ag := NewLLMAgent("test", "", &callbackProvider{},
		WithTools(&recordingTool{called: new(bool)}),
		WithInputHandler(handler),
		WithToolApproval("rec", OnDeny(DenyHalt)),
	)

	_, err := ag.tools.Execute(context.Background(), "rec", json.RawMessage(`{}`))

	var halt *core.ErrHalt
	if !errors.As(err, &halt) {
		t.Errorf("err = %v (%T), want *core.ErrHalt", err, err)
	}
}

func TestToolApproval_NonGuardedToolNotAffected(t *testing.T) {
	handler := &fakeInputHandler{approve: false}
	called := false
	ag := NewLLMAgent("test", "", &callbackProvider{},
		WithTools(&recordingTool{called: &called}),
		WithInputHandler(handler),
		// No WithToolApproval — tool runs without prompting.
	)
	_, err := ag.tools.Execute(context.Background(), "rec", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if !called {
		t.Error("non-guarded tool should run without approval")
	}
	if handler.calls != 0 {
		t.Errorf("InputHandler called %d times, want 0 (no approval configured)", handler.calls)
	}
}

func TestToolApproval_EmitsPendingEvent(t *testing.T) {
	handler := &fakeInputHandler{approve: true}
	ag := NewLLMAgent("test", "", &callbackProvider{},
		WithTools(&recordingTool{called: new(bool)}),
		WithInputHandler(handler),
		WithToolApproval("rec"),
	)

	ch := make(chan core.StreamEvent, 8)
	ctx := contextWithStreamSink(context.Background(), ch)

	// Call the registered tool through the registry; approval wrapper should
	// read the sink from ctx and emit the pending event.
	resultCh := make(chan struct{}, 1)
	go func() {
		_, _ = ag.tools.Execute(ctx, "rec", json.RawMessage(`{}`))
		close(ch)
		resultCh <- struct{}{}
	}()

	var got []core.StreamEventType
	for ev := range ch {
		got = append(got, ev.Type)
	}
	<-resultCh

	found := false
	for _, ty := range got {
		if ty == core.EventToolApprovalPending {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected EventToolApprovalPending in stream, got: %v", got)
	}
}
