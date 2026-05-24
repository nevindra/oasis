package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nevindra/oasis/core"
)

// These tests cover the LLMAgent integration with PreProcessor / PostProcessor /
// PostToolProcessor hooks. Pure ProcessorChain (now processor.Chain) unit tests
// live alongside the implementation in github.com/nevindra/oasis/processor.

// uppercaseProcessor is a PostProcessor that prefixes the response content.
type uppercaseProcessor struct{}

func (p *uppercaseProcessor) PostLLM(_ context.Context, resp *core.ChatResponse) error {
	resp.Content = "[modified] " + resp.Content
	return nil
}

// redactToolProcessor is a PostToolProcessor that prefixes tool results.
type redactToolProcessor struct{}

func (p *redactToolProcessor) PostTool(_ context.Context, _ core.ToolCall, result *core.ToolResult) error {
	result.Content = core.TextContent("[redacted] " + string(result.Content))
	return nil
}

// haltProcessor halts execution with a canned response at any phase.
type haltProcessor struct {
	response string
}

func (p *haltProcessor) PreLLM(_ context.Context, _ *core.ChatRequest) error {
	return &core.ErrHalt{Response: p.response}
}

func (p *haltProcessor) PostLLM(_ context.Context, _ *core.ChatResponse) error {
	return &core.ErrHalt{Response: p.response}
}

func (p *haltProcessor) PostTool(_ context.Context, _ core.ToolCall, _ *core.ToolResult) error {
	return &core.ErrHalt{Response: p.response}
}

// errors used in defensive assertions below
var _ = errors.New

func TestLLMAgentPreProcessorHalt(t *testing.T) {
	provider := &mockProvider{
		name:      "test",
		responses: []core.ChatResponse{{Content: "should not reach"}},
	}

	agent := New("guarded", "Guarded agent", provider,
		WithProcessors(Processors{Pre: []core.PreProcessor{&haltProcessor{response: "blocked by guardrail"}}}),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "attack"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "blocked by guardrail" {
		t.Errorf("Output = %q, want %q", result.Output, "blocked by guardrail")
	}
}

func TestLLMAgentPostProcessorModifies(t *testing.T) {
	provider := &mockProvider{
		name:      "test",
		responses: []core.ChatResponse{{Content: "raw response"}},
	}

	agent := New("modified", "Modified agent", provider,
		WithProcessors(Processors{Post: []core.PostProcessor{&uppercaseProcessor{}}}),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "[modified] raw response" {
		t.Errorf("Output = %q, want %q", result.Output, "[modified] raw response")
	}
}

func TestLLMAgentPostToolProcessorModifies(t *testing.T) {
	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
	}

	agent := New("redacted", "Redacted agent", provider,
		WithTools(mockTool{}),
		WithProcessors(Processors{PostTool: []core.PostToolProcessor{&redactToolProcessor{}}}),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "greet"})
	if err != nil {
		t.Fatal(err)
	}
	// Agent should complete — the redaction happens on tool results in message history
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}
}

// TestPostToolProcessor_OnIterationCompleteReceivesMutatedContent verifies that
// the OnIterationComplete snapshot delivers the post-processed (mutated) tool
// result, not the raw dispatcher output.
func TestPostToolProcessor_OnIterationCompleteReceivesMutatedContent(t *testing.T) {
	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
	}

	var capturedContent string
	hook := func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error) {
		if len(snap.ToolResults) > 0 {
			capturedContent = string(snap.ToolResults[0].Content)
		}
		return Continue(), nil
	}

	a := New("snap-redact", "Snapshot redact agent", provider,
		WithTools(mockTool{}),
		WithProcessors(Processors{PostTool: []core.PostToolProcessor{&redactToolProcessor{}}}),
		WithHooks(Hooks{OnIterationComplete: hook}),
	)

	_, err := a.Execute(context.Background(), AgentTask{Input: "greet"})
	if err != nil {
		t.Fatal(err)
	}

	// result.Content is a JSON-encoded string (e.g. `"[redacted] ..."`);
	// check that the encoded form contains the redaction marker.
	const wantMarker = "[redacted]"
	if !strings.Contains(capturedContent, wantMarker) {
		t.Errorf("OnIterationComplete snap.ToolResults[0].Content = %q, want it to contain %q (post-processed content not delivered to hook)", capturedContent, wantMarker)
	}
}
