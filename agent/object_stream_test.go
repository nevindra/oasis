package agent

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestObjectDeltaEmitted(t *testing.T) {
	provider := newFnProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `{"title":`}
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `"Q3 Report","sections":[`}
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `"intro","summary"]}`}
		close(ch)
		return core.ChatResponse{
			Content:      `{"title":"Q3 Report","sections":["intro","summary"]}`,
			FinishReason: core.FinishStop,
		}, nil
	})

	schema := core.NewResponseSchema("Report", &core.SchemaObject{
		Type: "object",
		Properties: map[string]*core.SchemaObject{
			"title":    {Type: "string"},
			"sections": {Type: "array", Items: &core.SchemaObject{Type: "string"}},
		},
	})

	a := NewLLMAgent("t", "test", provider, WithResponseSchema(schema))

	ch := make(chan core.StreamEvent, 64)
	go func() { _, _ = a.ExecuteStream(context.Background(), AgentTask{Input: "x"}, ch) }()

	deltas, finish := 0, 0
	for ev := range ch {
		if ev.Type == core.EventObjectDelta {
			deltas++
		}
		if ev.Type == core.EventObjectFinish {
			finish++
		}
	}
	if deltas < 1 {
		t.Errorf("expected >=1 EventObjectDelta, got %d", deltas)
	}
	if finish != 1 {
		t.Errorf("expected exactly 1 EventObjectFinish, got %d", finish)
	}
}

func TestElementDeltaForTopLevelArray(t *testing.T) {
	provider := newFnProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `[{"name":"a"},`}
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `{"name":"b"},`}
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `{"name":"c"}]`}
		close(ch)
		return core.ChatResponse{
			Content:      `[{"name":"a"},{"name":"b"},{"name":"c"}]`,
			FinishReason: core.FinishStop,
		}, nil
	})

	schema := core.NewResponseSchema("items", &core.SchemaObject{
		Type:  "array",
		Items: &core.SchemaObject{Type: "object"},
	})

	a := NewLLMAgent("t", "test", provider, WithResponseSchema(schema))

	ch := make(chan core.StreamEvent, 64)
	go func() { _, _ = a.ExecuteStream(context.Background(), AgentTask{Input: "x"}, ch) }()

	elems := 0
	for ev := range ch {
		if ev.Type == core.EventElementDelta {
			elems++
		}
	}
	if elems != 3 {
		t.Errorf("EventElementDelta count = %d, want 3", elems)
	}
}
