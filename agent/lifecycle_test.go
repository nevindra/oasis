package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

// fnProvider is a minimal Provider backed by a function. Used in lifecycle
// tests to avoid dependency on the shared callbackProvider (testhelpers_test.go)
// which uses a fixed-response style and on mockProvider which sends
// EventTextDelta inside ChatStream.
type fnProvider struct {
	fn func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error)
}

func (c *fnProvider) Name() string { return "fn" }
func (c *fnProvider) ChatStream(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	return c.fn(ctx, req, ch)
}

func newFnProvider(fn func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error)) *fnProvider {
	return &fnProvider{fn: fn}
}

// fnTool is a minimal AnyTool backed by a function.
type fnTool struct {
	name string
	fn   func(ctx context.Context, args json.RawMessage) (core.ToolResult, error)
}

func (t *fnTool) Name() string { return t.name }
func (t *fnTool) Definition() ToolDefinition {
	return ToolDefinition{Name: t.name, Description: t.name}
}
func (t *fnTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	return t.fn(ctx, args)
}

func newFnTool(name string, fn func(ctx context.Context, args json.RawMessage) (core.ToolResult, error)) *fnTool {
	return &fnTool{name: name, fn: fn}
}

func TestLifecycleEnvelopeRunStart(t *testing.T) {
	// Use a callbackProvider configured to return a simple "ok" response with
	// no tool calls.
	provider := newFnProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{Content: "ok", FinishReason: core.FinishStop}, nil
	})
	a := NewLLMAgent("t", "test", provider)

	ch := make(chan core.StreamEvent, 64)
	go func() {
		_, _ = a.ExecuteStream(context.Background(), AgentTask{Input: "hello"}, ch)
	}()

	got := []core.StreamEventType{}
	for ev := range ch {
		got = append(got, ev.Type)
	}

	// First event must be run-start, last must be run-finish. No
	// EventInputReceived or EventProcessingStart should appear.
	if len(got) == 0 || got[0] != core.EventRunStart {
		t.Errorf("first event = %v, want EventRunStart; full: %v", func() core.StreamEventType {
			if len(got) > 0 {
				return got[0]
			}
			return "<empty>"
		}(), got)
	}
	if len(got) == 0 || got[len(got)-1] != core.EventRunFinish {
		t.Errorf("last event = %v, want EventRunFinish; full: %v", func() core.StreamEventType {
			if len(got) > 0 {
				return got[len(got)-1]
			}
			return "<empty>"
		}(), got)
	}
	for _, ev := range got {
		if ev == core.EventInputReceived || ev == core.EventProcessingStart {
			t.Errorf("deprecated event %v still emitted", ev)
		}
	}
}

func TestLifecycleEnvelopeIterations(t *testing.T) {
	// Provider returns a tool call on iteration 0, then "done" on iteration 1.
	iter := 0
	provider := newFnProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		i := iter
		iter++
		if i == 0 {
			return core.ChatResponse{
				ToolCalls:    []core.ToolCall{{ID: "1", Name: "noop", Args: []byte(`{}`)}},
				FinishReason: core.FinishToolCalls,
			}, nil
		}
		return core.ChatResponse{Content: "done", FinishReason: core.FinishStop}, nil
	})
	noop := newFnTool("noop", func(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
		return core.ToolResult{Content: []byte(`"ok"`)}, nil
	})
	a := NewLLMAgent("t", "test", provider, WithTools(noop))

	ch := make(chan core.StreamEvent, 64)
	go func() { _, _ = a.ExecuteStream(context.Background(), AgentTask{Input: "x"}, ch) }()

	starts, finishes := 0, 0
	for ev := range ch {
		if ev.Type == core.EventIterationStart {
			starts++
		}
		if ev.Type == core.EventIterationFinish {
			finishes++
		}
	}
	if starts != 2 || finishes != 2 {
		t.Errorf("starts=%d finishes=%d, want 2/2", starts, finishes)
	}
}
