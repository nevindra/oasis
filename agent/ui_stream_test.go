package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestEventUIComponent_EmittedAfterToolResult(t *testing.T) {
	flightTool := core.RawTool("show_flights", "shows flights",
		json.RawMessage(`{"type":"object"}`),
		func(_ context.Context, _ json.RawMessage) (core.ToolResult, error) {
			return core.UIResult("FlightCard", map[string]int{"count": 2}), nil
		})

	provider := &scriptedProvider{responses: []core.ChatResponse{
		{ToolCalls: []core.ToolCall{{ID: "1", Name: "show_flights", Args: json.RawMessage(`{}`)}}},
		{Content: "done"},
	}}

	a := New("ui", "ui agent", provider, WithTools(flightTool))

	ch := make(chan core.StreamEvent, 64)
	if _, err := a.Execute(context.Background(), AgentTask{Input: "flights"}, core.WithStream(ch)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var events []core.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	resultIdx, uiIdx := -1, -1
	for i, ev := range events {
		switch ev.Type {
		case core.EventToolCallResult:
			if ev.ID == "1" {
				resultIdx = i
			}
		case core.EventUIComponent:
			uiIdx = i
		}
	}
	if uiIdx == -1 {
		t.Fatal("no EventUIComponent emitted")
	}
	if resultIdx == -1 || uiIdx < resultIdx {
		t.Fatalf("EventUIComponent (idx %d) must follow EventToolCallResult (idx %d)", uiIdx, resultIdx)
	}

	ui := events[uiIdx]
	if ui.ID != "1" {
		t.Fatalf("UI event ID = %q, want 1", ui.ID)
	}
	if ui.Name != "FlightCard" {
		t.Fatalf("UI event Name = %q, want FlightCard", ui.Name)
	}
	if string(ui.Object) != `{"count":2}` {
		t.Fatalf("UI event Object = %s, want {\"count\":2}", ui.Object)
	}
}
