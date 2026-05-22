package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevindra/oasis"
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

func TestOversizeToolResultStored(t *testing.T) {
	bigOutput := strings.Repeat("x", 200_000)
	tool := newFakeTool("big_tool", bigOutput)
	provider := newToolCallThenTextProvider("big_tool")

	store := core.NewInMemoryToolResultStore()
	a := oasis.NewLLMAgent("test", "", provider,
		oasis.WithTools(tool),
		oasis.WithToolResultStore(store),
		oasis.WithLimits(oasis.Limits{MaxToolResultLen: 100_000}),
	)

	_, err := a.Execute(context.Background(), oasis.AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}

	// The second LLM call should have received a message containing the paging marker.
	if len(provider.captured) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(provider.captured))
	}
	secondReq := provider.captured[1]
	foundMarker := false
	for _, msg := range secondReq.Messages {
		if strings.Contains(msg.Content, "Use read_full_result(id=") {
			foundMarker = true
			break
		}
	}
	if !foundMarker {
		t.Error("expected paging marker in tool result message sent to LLM, got none")
	}
}

func TestNoStoreFallsBackToLegacyMarker(t *testing.T) {
	bigOutput := strings.Repeat("y", 200_000)
	tool := newFakeTool("big_tool", bigOutput)
	provider := newToolCallThenTextProvider("big_tool")

	a := oasis.NewLLMAgent("test", "", provider,
		oasis.WithTools(tool),
		oasis.WithToolResultStore(nil), // explicit opt-out
		oasis.WithLimits(oasis.Limits{MaxToolResultLen: 100_000}),
	)

	_, _ = a.Execute(context.Background(), oasis.AgentTask{Input: "go"})

	if len(provider.captured) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(provider.captured))
	}
	secondReq := provider.captured[1]
	foundLegacy := false
	for _, msg := range secondReq.Messages {
		if strings.Contains(msg.Content, "[output truncated") {
			foundLegacy = true
			break
		}
	}
	if !foundLegacy {
		t.Error("expected legacy truncation marker when store is nil")
	}
}
