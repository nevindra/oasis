package agent

import (
	"context"
	"encoding/json"
	"errors"
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
		WithPreProcessors(&haltProcessor{response: "blocked by guardrail"}),
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
		WithPostProcessors(&uppercaseProcessor{}),
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
		WithPostToolProcessors(&redactToolProcessor{}),
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
