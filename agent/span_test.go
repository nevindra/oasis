package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/nevindra/oasis/core"
)

// recordingSpan captures span name, attributes, and ended state for assertions.
type recordingSpan struct {
	name  string
	attrs []core.SpanAttr
	ended bool
}

func (s *recordingSpan) SetAttr(attrs ...core.SpanAttr) { s.attrs = append(s.attrs, attrs...) }
func (s *recordingSpan) End()                           { s.ended = true }
func (s *recordingSpan) Event(name string, attrs ...core.SpanAttr) {}
func (s *recordingSpan) Error(err error)                {}

// recordingTracer is a test double for core.Tracer that records all spans started.
type recordingTracer struct {
	mu    sync.Mutex
	spans []*recordingSpan
}

func (t *recordingTracer) Start(ctx context.Context, name string, attrs ...core.SpanAttr) (context.Context, core.Span) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sp := &recordingSpan{name: name, attrs: append([]core.SpanAttr(nil), attrs...)}
	t.spans = append(t.spans, sp)
	return ctx, sp
}

func (t *recordingTracer) names() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.spans))
	for i, s := range t.spans {
		out[i] = s.name
	}
	return out
}

// Task 4.1 — agent.iteration span is created for each iteration.
func TestIterationSpanCreated(t *testing.T) {
	tracer := &recordingTracer{}
	provider := newFnProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{Content: "ok", FinishReason: core.FinishStop}, nil
	})
	a := NewLLMAgent("t", "test", provider, WithTracer(tracer))
	_, _ = a.Execute(context.Background(), AgentTask{Input: "x"})

	names := tracer.names()
	want := []string{"agent.execute", "agent.iteration"}
	for _, w := range want {
		found := false
		for _, n := range names {
			if n == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected span %q, got %v", w, names)
		}
	}
}

// Task 4.2 — llm.generate span is created for each LLM call.
func TestLLMGenerateSpanCreated(t *testing.T) {
	tracer := &recordingTracer{}
	provider := newFnProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{Content: "ok", FinishReason: core.FinishStop}, nil
	})
	a := NewLLMAgent("t", "test", provider, WithTracer(tracer))
	_, _ = a.Execute(context.Background(), AgentTask{Input: "x"})

	names := tracer.names()
	found := false
	for _, n := range names {
		if n == "llm.generate" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected llm.generate span, got %v", names)
	}
}
