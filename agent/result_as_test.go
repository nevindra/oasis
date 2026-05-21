package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

type Report struct {
	Title    string   `json:"title"`
	Sections []string `json:"sections"`
}

func TestResultObjectAsTyped(t *testing.T) {
	r := AgentResult{Object: json.RawMessage(`{"title":"Q3","sections":["x","y"]}`)}
	got, err := ResultObjectAs[Report](r)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Title != "Q3" || len(got.Sections) != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestResultObjectAsEmpty(t *testing.T) {
	r := AgentResult{}
	_, err := ResultObjectAs[Report](r)
	if err == nil {
		t.Error("expected error on empty Object")
	}
}

func TestStreamObjectAsTyped(t *testing.T) {
	provider := newFnProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `{"title":"Q3","sections":[`}
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `"intro"`}
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: `,"summary"]}`}
		close(ch)
		return core.ChatResponse{
			Content:      `{"title":"Q3","sections":["intro","summary"]}`,
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
	stream := StartStream(context.Background(), a, AgentTask{Input: "x"})

	var snapshots []Report
	for partial := range StreamObjectAs[Report](stream) {
		snapshots = append(snapshots, partial)
	}
	if len(snapshots) == 0 {
		t.Fatal("expected at least one snapshot")
	}
	// Final snapshot should have the full report.
	last := snapshots[len(snapshots)-1]
	if last.Title != "Q3" || len(last.Sections) != 2 {
		t.Errorf("final snapshot: %+v", last)
	}
}
