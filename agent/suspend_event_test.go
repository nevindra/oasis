package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nevindra/oasis/memory"
)

// collectEvents drains ch into a slice and returns it.
// The channel must be closed by the producer; this blocks until that happens.
func collectEvents(ch <-chan StreamEvent) []StreamEvent {
	var out []StreamEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

// findEvent returns the first event whose Type matches t, plus its index.
// Returns (-1, zero value) if not found.
func findEvent(evs []StreamEvent, t StreamEventType) (int, StreamEvent) {
	for i, ev := range evs {
		if ev.Type == t {
			return i, ev
		}
	}
	return -1, StreamEvent{}
}

// --------------------------------------------------------------------------
// A. EventToolCallSuspended fires on PostTool suspend
// --------------------------------------------------------------------------

func TestEventToolCallSuspendedFires(t *testing.T) {
	payload := json.RawMessage(`{"prompt":"approve?"}`)
	provider := &mockProvider{
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "tc1", Name: "transfer_money", Args: json.RawMessage(`{"amount":100}`)}}},
		},
	}
	chain := NewProcessorChain()
	chain.AddPostTool(&suspendingPostToolProcessor{
		triggerTool: "transfer_money",
		payload:     payload,
	})

	cfg := LoopConfig{
		name:     "test",
		provider: provider,
		tools:    []ToolDefinition{{Name: "transfer_money", Description: "move funds"}},
		processors: chain,
		maxIter:  5,
		mem:      &memory.AgentMemory{},
		dispatch: func(_ context.Context, tc ToolCall) DispatchResult { return DispatchResult{Content: "ok"} },
	}

	ch := make(chan StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	evs := collectEvents(ch)

	idx, ev := findEvent(evs, EventToolCallSuspended)
	if idx < 0 {
		t.Fatalf("EventToolCallSuspended not found in %d events", len(evs))
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

// --------------------------------------------------------------------------
// C. EventProcessorSuspended fires on PreLLM suspend
// --------------------------------------------------------------------------

func TestEventProcessorSuspendedFiresPreLLM(t *testing.T) {
	payload := json.RawMessage(`{"gate":"pre_check"}`)
	provider := &mockProvider{
		responses: []ChatResponse{{Content: "never reached"}},
	}
	chain := NewProcessorChain()
	chain.AddPre(&suspendingPreProcessor{payload: payload})

	cfg := LoopConfig{
		name:       "test-pre",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}

	ch := make(chan StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	evs := collectEvents(ch)

	idx, ev := findEvent(evs, EventProcessorSuspended)
	if idx < 0 {
		t.Fatalf("EventProcessorSuspended not found in %d events", len(evs))
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

// --------------------------------------------------------------------------
// D. EventProcessorSuspended fires on PostLLM suspend
// --------------------------------------------------------------------------

func TestEventProcessorSuspendedFiresPostLLM(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			// PostLLM suspends when it sees a tool call.
			{ToolCalls: []ToolCall{{ID: "1", Name: "risky", Args: json.RawMessage(`{}`)}}},
		},
	}
	chain := NewProcessorChain()
	chain.AddPost(&suspendingProcessor{
		triggerTool: "risky",
		payload:     json.RawMessage(`{"confirm":"yes?"}`),
	})

	cfg := LoopConfig{
		name:       "test-post",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "risky", Description: "risky op"}},
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}

	ch := make(chan StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	evs := collectEvents(ch)

	idx, ev := findEvent(evs, EventProcessorSuspended)
	if idx < 0 {
		t.Fatalf("EventProcessorSuspended not found in %d events", len(evs))
	}
	if ev.Content != "post" {
		t.Errorf("Content = %q, want %q", ev.Content, "post")
	}
	if ev.Protocol != "" {
		t.Errorf("Protocol = %q, want empty (untyped Suspend)", ev.Protocol)
	}
}

// --------------------------------------------------------------------------
// E. Event ordering on tool suspend
// --------------------------------------------------------------------------

// Note: EventRunStart is emitted by LLMAgent above runLoop, not by runLoop
// itself. The ordering test therefore starts from EventIterationStart.

func TestEventOrderingOnToolSuspend(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "t1", Name: "risky_op", Args: json.RawMessage(`{}`)}}},
		},
	}
	chain := NewProcessorChain()
	chain.AddPostTool(&suspendingPostToolProcessor{
		triggerTool: "risky_op",
		payload:     json.RawMessage(`{"ask":"ok?"}`),
	})

	cfg := LoopConfig{
		name:       "test-order",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "risky_op", Description: "test"}},
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{Content: "done"} },
	}

	ch := make(chan StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	evs := collectEvents(ch)

	indexOf := func(typ StreamEventType) int {
		i, _ := findEvent(evs, typ)
		return i
	}

	iIterStart := indexOf(EventIterationStart)
	iSuspended := indexOf(EventToolCallSuspended)
	iIterFinish := indexOf(EventIterationFinish)
	iRunFinish := indexOf(EventRunFinish)

	if iIterStart < 0 {
		t.Fatal("EventIterationStart not found")
	}
	if iSuspended < 0 {
		t.Fatal("EventToolCallSuspended not found")
	}
	if iIterFinish < 0 {
		t.Fatal("EventIterationFinish not found")
	}
	if iRunFinish < 0 {
		t.Fatal("EventRunFinish not found")
	}

	// Strict ordering: IterationStart < ToolCallSuspended < IterationFinish < RunFinish.
	if !(iIterStart < iSuspended) {
		t.Errorf("want EventIterationStart (%d) < EventToolCallSuspended (%d)", iIterStart, iSuspended)
	}
	if !(iSuspended < iIterFinish) {
		t.Errorf("want EventToolCallSuspended (%d) < EventIterationFinish (%d)", iSuspended, iIterFinish)
	}
	if !(iIterFinish < iRunFinish) {
		t.Errorf("want EventIterationFinish (%d) < EventRunFinish (%d)", iIterFinish, iRunFinish)
	}

	// IterationFinish and RunFinish must carry FinishSuspended.
	_, iterFinishEv := findEvent(evs, EventIterationFinish)
	if iterFinishEv.FinishReason != FinishSuspended {
		t.Errorf("EventIterationFinish.FinishReason = %q, want %q", iterFinishEv.FinishReason, FinishSuspended)
	}
	_, runFinishEv := findEvent(evs, EventRunFinish)
	if runFinishEv.FinishReason != FinishSuspended {
		t.Errorf("EventRunFinish.FinishReason = %q, want %q", runFinishEv.FinishReason, FinishSuspended)
	}
}

// --------------------------------------------------------------------------
// F. Typed protocol tag propagates to events
// --------------------------------------------------------------------------

type evTestReq struct{ Action string }
type evTestResp struct{ OK bool }

func TestTypedProtocolPropagatesToEvents(t *testing.T) {
	protocol := NewSuspendProtocol[evTestReq, evTestResp]("approve_v1")

	pp := &typedSuspendingPreProcessor[evTestReq, evTestResp]{
		protocol: protocol,
		payload:  evTestReq{Action: "go"},
	}

	provider := &mockProvider{responses: []ChatResponse{{Content: "done"}}}
	chain := NewProcessorChain()
	chain.AddPre(pp)

	cfg := LoopConfig{
		name:       "test-typed",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}

	ch := make(chan StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	evs := collectEvents(ch)

	// EventProcessorSuspended must carry the typed protocol tag.
	idx, procEv := findEvent(evs, EventProcessorSuspended)
	if idx < 0 {
		t.Fatal("EventProcessorSuspended not found")
	}
	if procEv.Protocol != "approve_v1" {
		t.Errorf("EventProcessorSuspended.Protocol = %q, want %q", procEv.Protocol, "approve_v1")
	}

	// EventRunFinish must also carry the protocol tag.
	idx, runFinishEv := findEvent(evs, EventRunFinish)
	if idx < 0 {
		t.Fatal("EventRunFinish not found")
	}
	if runFinishEv.Protocol != "approve_v1" {
		t.Errorf("EventRunFinish.Protocol = %q, want %q", runFinishEv.Protocol, "approve_v1")
	}
}

// --------------------------------------------------------------------------
// G. Untyped Suspend has empty Protocol everywhere
// --------------------------------------------------------------------------

func TestUntypedSuspendHasEmptyProtocolEverywhere(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{{Content: "never"}},
	}
	chain := NewProcessorChain()
	chain.AddPre(&suspendingPreProcessor{payload: json.RawMessage(`{"x":1}`)})

	cfg := LoopConfig{
		name:       "test-untyped",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}

	ch := make(chan StreamEvent, 64)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	evs := collectEvents(ch)

	for _, ev := range evs {
		switch ev.Type {
		case EventProcessorSuspended, EventToolCallSuspended, EventRunFinish:
			if ev.Protocol != "" {
				t.Errorf("event %q has Protocol = %q, want empty", ev.Type, ev.Protocol)
			}
		}
	}
}

// --------------------------------------------------------------------------
// H. IterationTrace.FinishReason is FinishSuspended on suspend
// --------------------------------------------------------------------------

func TestIterationTraceFinishReasonOnSuspend(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "t1", Name: "op", Args: json.RawMessage(`{}`)}}},
		},
	}
	chain := NewProcessorChain()
	chain.AddPostTool(&suspendingPostToolProcessor{
		triggerTool: "op",
		payload:     json.RawMessage(`{"ask":"confirm"}`),
	})

	cfg := LoopConfig{
		name:       "test-iter-trace",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "op", Description: "test op"}},
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{Content: "ok"} },
	}

	ch := make(chan StreamEvent, 64)
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
	if last.FinishReason != FinishSuspended {
		t.Errorf("Iterations[last].FinishReason = %q, want %q", last.FinishReason, FinishSuspended)
	}

	// Cross-check the stream event carries the same reason.
	evs := collectEvents(ch)
	_, iterFinishEv := findEvent(evs, EventIterationFinish)
	if iterFinishEv.Type == "" {
		t.Fatal("EventIterationFinish not found in stream")
	}
	if iterFinishEv.FinishReason != FinishSuspended {
		t.Errorf("EventIterationFinish.FinishReason = %q, want %q", iterFinishEv.FinishReason, FinishSuspended)
	}
}

// --------------------------------------------------------------------------
// I. AgentResult.Suspended() and SuspendedProtocol() accessors
// --------------------------------------------------------------------------

func TestAgentResultSuspendedAccessors(t *testing.T) {
	// Suspended run.
	r := AgentResult{FinishReason: FinishSuspended, SuspendProtocol: "foo"}
	if !r.Suspended() {
		t.Error("Suspended() = false, want true for FinishSuspended")
	}
	if r.SuspendedProtocol() != "foo" {
		t.Errorf("SuspendedProtocol() = %q, want %q", r.SuspendedProtocol(), "foo")
	}

	// Non-suspended run.
	r2 := AgentResult{FinishReason: FinishStop}
	if r2.Suspended() {
		t.Error("Suspended() = true, want false for FinishStop")
	}
	if r2.SuspendedProtocol() != "" {
		t.Errorf("SuspendedProtocol() = %q, want empty", r2.SuspendedProtocol())
	}
}

// --------------------------------------------------------------------------
// J. Stream.Suspended() and Stream.SuspendedProtocol() accessors
// --------------------------------------------------------------------------

func TestStreamSuspendedAccessors(t *testing.T) {
	// Build a minimal StreamingAgent that returns a suspended AgentResult.
	suspendedResult := AgentResult{
		FinishReason:    FinishSuspended,
		SuspendProtocol: "",
	}

	ag := &emitterAgent{
		final: suspendedResult,
	}

	stream := StartStream(context.Background(), ag, AgentTask{Input: "go"})
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
		FinishReason:    FinishSuspended,
		SuspendProtocol: "my_protocol",
	}

	ag := &emitterAgent{final: suspendedResult}
	stream := StartStream(context.Background(), ag, AgentTask{Input: "go"})
	<-stream.Done()

	if !stream.Suspended() {
		t.Error("Stream.Suspended() = false, want true")
	}
	if stream.SuspendedProtocol() != "my_protocol" {
		t.Errorf("Stream.SuspendedProtocol() = %q, want %q", stream.SuspendedProtocol(), "my_protocol")
	}
}

// --------------------------------------------------------------------------
// K. StreamEvent JSON omitempty for Protocol and SuspendPayload
// --------------------------------------------------------------------------

func TestStreamEventJSONOmitempty(t *testing.T) {
	// Plain event — no protocol/payload fields expected.
	ev := StreamEvent{Type: EventTextDelta, Content: "hi"}
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
	ev2 := StreamEvent{
		Type:           EventToolCallSuspended,
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

// --------------------------------------------------------------------------
// L. Channel closes after run-finish on suspend
// --------------------------------------------------------------------------

func TestChannelClosesAfterRunFinish(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{{Content: "nope"}},
	}
	chain := NewProcessorChain()
	chain.AddPre(&suspendingPreProcessor{payload: json.RawMessage(`{}`)})

	cfg := LoopConfig{
		name:       "test-close",
		provider:   provider,
		processors: chain,
		maxIter:    5,
		mem:        &memory.AgentMemory{},
		dispatch:   func(_ context.Context, _ ToolCall) DispatchResult { return DispatchResult{} },
	}

	ch := make(chan StreamEvent, 64)
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
