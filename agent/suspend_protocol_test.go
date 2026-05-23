package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
	"github.com/nevindra/oasis/processor"
)

// TestUntypedSuspendHasEmptyTag verifies the existing untyped Suspend path
// produces an ErrSuspended whose internal tag is empty. This guards against
// future protocol additions accidentally tagging the untyped path.
func TestUntypedSuspendHasEmptyTag(t *testing.T) {
	provider := &mockProvider{
		responses: []core.ChatResponse{{Content: "done"}},
	}

	chain := processor.NewChain()
	chain.AddPre(&suspendingPreProcessor{
		payload: json.RawMessage(`{"prompt": "ok?"}`),
	})

	cfg := LoopConfig{
		Name: "test",
		Provider: provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem: &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
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
		Name: "test",
		Provider: provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem: &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
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

type apReq struct{ Amount float64 }
type apResp struct{ Approved bool }

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

func TestPayloadFromReturnsTypedReq(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	// Construct an ErrSuspended directly via the engine path.
	provider := &mockProvider{responses: []core.ChatResponse{{Content: ""}}}
	chain := processor.NewChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 7500}})

	cfg := LoopConfig{
		Name: "test",
		Provider: provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem: &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
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
		Name: "test",
		Provider: provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem: &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
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
		Name: "test",
		Provider: provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem: &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
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
		Name: "test",
		Provider: provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem: &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
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
		Name: "test",
		Provider: provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem: &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
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

func TestTypedSuspendRespectsTTL(t *testing.T) {
	p := NewSuspendProtocol[apReq, apResp]("approve_transfer")

	provider := &mockProvider{responses: []core.ChatResponse{{Content: ""}}}
	chain := processor.NewChain()
	chain.AddPre(&typedSuspendingPreProcessor[apReq, apResp]{protocol: p, payload: apReq{Amount: 1}})

	cfg := LoopConfig{
		Name: "test",
		Provider: provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem: &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
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
		Name: "test",
		Provider: provider,
		Processors: chain,
		Config:       Config{MaxIter: 5, MaxSuspendSnapshots: 1},
		Mem: &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
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

// TestPayloadFromOnUntypedSuspend ensures the runtime tag check rejects a
// query via a protocol when the suspension came from the untyped path.
func TestPayloadFromOnUntypedSuspend(t *testing.T) {
	provider := &mockProvider{responses: []core.ChatResponse{{Content: ""}}}
	chain := processor.NewChain()
	chain.AddPre(&suspendingPreProcessor{payload: json.RawMessage(`{"Amount": 1}`)})

	cfg := LoopConfig{
		Name: "test",
		Provider: provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem: &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
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
		Name: "test",
		Provider: provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem: &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
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
		Name: "test",
		Provider: provider,
		Processors: chain,
		Config:     Config{MaxIter: 5},
		Mem: &memory.AgentMemory{},
		Dispatch: func(_ context.Context, _ core.ToolCall) DispatchResult { return DispatchResult{} },
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
