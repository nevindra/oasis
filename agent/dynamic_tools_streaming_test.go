package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nevindra/oasis/core"
)

// dynProgressTool implements StreamingAnyTool for WithDynamicTools streaming tests.
type dynProgressTool struct{}

func (t dynProgressTool) Name() string { return "dyn_search" }

func (t dynProgressTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "dyn_search",
		Description: "Dynamic search with progress",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
	}
}

func (t dynProgressTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return core.TextResult("found 2 results"), nil
}

func (t dynProgressTool) ExecuteStream(_ context.Context, _ json.RawMessage, ch chan<- StreamEvent) (ToolResult, error) {
	for i := 1; i <= 2; i++ {
		ch <- StreamEvent{
			Type:    EventToolProgress,
			Name:    "dyn_search",
			Content: fmt.Sprintf(`{"step":%d}`, i),
		}
	}
	return core.TextResult("found 2 results"), nil
}

// TestDynamicToolsStreamingToolEmitsProgress verifies that when WithDynamicTools
// returns a StreamingAnyTool implementation, EventToolProgress events flow
// through ExecuteStream (i.e. the streaming executor is wired, not hard-coded nil).
func TestDynamicToolsStreamingToolEmitsProgress(t *testing.T) {
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "dyn_search", Args: json.RawMessage(`{"q":"test"}`)}}},
			{Content: "done"},
		},
	}

	agent := NewLLMAgent("dyn", "Dynamic streaming", provider,
		WithDynamicTools(func(_ context.Context, _ AgentTask) []AnyTool {
			return []AnyTool{dynProgressTool{}}
		}),
	)

	ch := make(chan StreamEvent, 32)
	result, err := agent.ExecuteStream(context.Background(), AgentTask{Input: "search"}, ch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}

	var progressEvents []StreamEvent
	for ev := range ch {
		if ev.Type == EventToolProgress {
			progressEvents = append(progressEvents, ev)
		}
	}
	if len(progressEvents) != 2 {
		t.Fatalf("expected 2 tool-progress events from dynamic streaming tool, got %d", len(progressEvents))
	}
	if progressEvents[0].Name != "dyn_search" {
		t.Errorf("progress[0].Name = %q, want %q", progressEvents[0].Name, "dyn_search")
	}
}
