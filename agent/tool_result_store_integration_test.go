package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

// configuredFakeTool is an AnyTool with a configurable name and fixed output.
type configuredFakeTool struct {
	name   string
	output string
}

func (t *configuredFakeTool) Name() string { return t.name }
func (t *configuredFakeTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{Name: t.name, Description: "test tool"}
}
func (t *configuredFakeTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (core.ToolResult, error) {
	return core.TextResult(t.output), nil
}

// newFakeTool returns an AnyTool whose Execute always returns the given output.
func newFakeTool(name, output string) core.AnyTool {
	return &configuredFakeTool{name: name, output: output}
}

// sequentialProvider returns responses in order: first response is returned on
// the first call, second on the second call, etc. After exhausting the list,
// the last response is repeated. Captures all requests for inspection.
type sequentialProvider struct {
	responses []core.ChatResponse
	calls     int
	captured  []core.ChatRequest
}

func (p *sequentialProvider) Name() string { return "sequential" }
func (p *sequentialProvider) ChatStream(_ context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	p.captured = append(p.captured, req)
	idx := p.calls
	if idx >= len(p.responses) {
		idx = len(p.responses) - 1
	}
	p.calls++
	return p.responses[idx], nil
}

// newFakeProviderReturning overrides the simpler version in testhelpers_external_test.go
// by returning a provider that calls big_tool first then returns text.
// Note: we define a separate constructor here to avoid collision.
func newToolCallThenTextProvider(toolName string) *sequentialProvider {
	return &sequentialProvider{
		responses: []core.ChatResponse{
			{
				// First call: request tool invocation.
				ToolCalls: []core.ToolCall{
					{ID: "tc1", Name: toolName, Args: json.RawMessage(`{}`)},
				},
			},
			{
				// Second call: return final text after seeing tool result.
				Content: "done",
			},
		},
	}
}

func TestOversizeToolResultChunked(t *testing.T) {
	// Verify that an oversize tool result is split into multiple tool-result
	// messages (transparent chunking) and the store receives the full payload.
	bigOutput := strings.Repeat("x", 200_000)
	tool := newFakeTool("big_tool", bigOutput)
	provider := newToolCallThenTextProvider("big_tool")

	store := core.NewInMemoryToolResultStore()
	a := agent.New("test", "", provider,
		agent.WithTools(tool),
		agent.WithToolConfig(agent.ToolConfig{ResultStore: store}),
		agent.WithLimits(agent.Limits{MaxToolResultLen: 100_000}),
	)

	_, err := a.Execute(context.Background(), core.AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}

	if len(provider.captured) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(provider.captured))
	}
	secondReq := provider.captured[1]

	// Collect all tool-result messages for the tool call.
	var toolMsgs []core.ChatMessage
	for _, msg := range secondReq.Messages {
		if msg.ToolCallID == "tc1" {
			toolMsgs = append(toolMsgs, msg)
		}
	}
	if len(toolMsgs) < 2 {
		t.Fatalf("expected >=2 tool-result chunks, got %d", len(toolMsgs))
	}

	// No read_full_result hints in any chunk.
	for i, msg := range toolMsgs {
		if strings.Contains(msg.Content, "read_full_result") {
			t.Errorf("chunk %d should not contain read_full_result hint", i)
		}
	}

	// Reassembled content equals original.
	var reassembled strings.Builder
	for _, msg := range toolMsgs {
		reassembled.WriteString(msg.Content)
	}
	if reassembled.String() != bigOutput {
		t.Error("reassembled chunks do not equal original content")
	}
}

func TestOversizeToolResultNoStoreStillChunks(t *testing.T) {
	// Without a store, chunking still applies transparently.
	bigOutput := strings.Repeat("y", 200_000)
	tool := newFakeTool("big_tool", bigOutput)
	provider := newToolCallThenTextProvider("big_tool")

	a := agent.New("test", "", provider,
		agent.WithTools(tool),
		agent.WithToolConfig(agent.ToolConfig{ResultStoreExplicit: true}), // explicit opt-out
		agent.WithLimits(agent.Limits{MaxToolResultLen: 100_000}),
	)

	_, _ = a.Execute(context.Background(), core.AgentTask{Input: "go"})

	if len(provider.captured) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(provider.captured))
	}
	secondReq := provider.captured[1]

	var toolMsgs []core.ChatMessage
	for _, msg := range secondReq.Messages {
		if msg.ToolCallID == "tc1" {
			toolMsgs = append(toolMsgs, msg)
		}
	}
	if len(toolMsgs) < 2 {
		t.Fatalf("expected >=2 tool-result chunks even without store, got %d", len(toolMsgs))
	}
}
