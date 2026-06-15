package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

// secretTool returns a payload containing a "secret" the transform must redact.
type secretTool struct{}

func (secretTool) Name() string { return "lookup" }
func (secretTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{Name: "lookup", Description: "look up"}
}
func (secretTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (core.ToolResult, error) {
	return core.TextResult("SECRET-TOKEN"), nil
}

// recordingResultStore captures every Put payload so the test can assert the
// Transcript sink's persisted content.
type recordingResultStore struct{ puts []string }

func (s *recordingResultStore) Put(_ context.Context, content string) (string, error) {
	s.puts = append(s.puts, content)
	return "id", nil
}
func (s *recordingResultStore) Get(_ context.Context, _ string, _, _ int) (string, int, error) {
	return "", 0, nil
}

func redact(_ string, r core.ToolResult) core.ToolResult {
	return core.ToolResult{Content: "REDACTED"}
}

func TestToolTransform_PerSinkIsolation(t *testing.T) {
	var lastReq core.ChatRequest
	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "lookup", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
		onChat: func(r *core.ChatRequest) { lastReq = *r },
	}
	store := &recordingResultStore{}

	a := New("t", "d", provider,
		WithTools(secretTool{}),
		WithToolConfig(ToolConfig{
			ResultStore: store,
			Transforms: map[string]core.ToolTransform{
				"lookup": {
					Display:    &core.SinkTransform{Result: redact},
					Transcript: &core.SinkTransform{Result: redact},
					// Model nil → LLM sees the real token
				},
			},
		}),
	)

	ch := make(chan core.StreamEvent, 32)
	result, err := a.Execute(context.Background(), AgentTask{Input: "go"}, core.WithStream(ch))
	if err != nil {
		t.Fatal(err)
	}

	// Display sink: the streamed tool-call-result must be redacted.
	var displayContent string
	for ev := range ch {
		if ev.Type == core.EventToolCallResult {
			displayContent = ev.Content
		}
	}
	if displayContent != "REDACTED" {
		t.Errorf("display content = %q, want REDACTED", displayContent)
	}

	// Transcript sink: step trace + store must be redacted.
	var traceOut string
	for _, s := range result.Steps {
		if s.Name == "lookup" {
			traceOut = s.RawOutput
		}
	}
	if traceOut != "REDACTED" {
		t.Errorf("transcript trace = %q, want REDACTED", traceOut)
	}
	if len(store.puts) != 1 || store.puts[0] != "REDACTED" {
		t.Errorf("store puts = %v, want [REDACTED]", store.puts)
	}

	// Model sink: the LLM's tool-result message must keep the real token.
	var modelSawToken bool
	for _, m := range lastReq.Messages {
		if m.Role == core.RoleTool && m.Content == "SECRET-TOKEN" {
			modelSawToken = true
		}
	}
	if !modelSawToken {
		t.Error("model sink should receive the unredacted SECRET-TOKEN")
	}
}

func TestToolTransform_DisplayFailClosedOnPanic(t *testing.T) {
	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "lookup", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
	}
	a := New("t", "d", provider,
		WithTools(secretTool{}),
		WithToolConfig(ToolConfig{
			Transforms: map[string]core.ToolTransform{
				"lookup": {Display: &core.SinkTransform{
					Result: func(string, core.ToolResult) core.ToolResult { panic("boom") },
				}},
			},
		}),
	)

	ch := make(chan core.StreamEvent, 32)
	if _, err := a.Execute(context.Background(), AgentTask{Input: "go"}, core.WithStream(ch)); err != nil {
		t.Fatal(err)
	}
	for ev := range ch {
		if ev.Type == core.EventToolCallResult {
			if ev.Content == "SECRET-TOKEN" {
				t.Fatal("fail-closed violated: raw secret leaked to display on panic")
			}
			if ev.Content != redactionFailed {
				t.Errorf("display content = %q, want placeholder", ev.Content)
			}
		}
	}
}
