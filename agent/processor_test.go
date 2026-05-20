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

func (p *uppercaseProcessor) PostLLM(_ context.Context, resp *ChatResponse) error {
	resp.Content = "[modified] " + resp.Content
	return nil
}

// redactToolProcessor is a PostToolProcessor that prefixes tool results.
type redactToolProcessor struct{}

func (p *redactToolProcessor) PostTool(_ context.Context, _ ToolCall, result *ToolResult) error {
	result.Content = core.TextContent("[redacted] " + string(result.Content))
	return nil
}

// haltProcessor halts execution with a canned response at any phase.
type haltProcessor struct {
	response string
}

func (p *haltProcessor) PreLLM(_ context.Context, _ *ChatRequest) error {
	return &ErrHalt{Response: p.response}
}

func (p *haltProcessor) PostLLM(_ context.Context, _ *ChatResponse) error {
	return &ErrHalt{Response: p.response}
}

func (p *haltProcessor) PostTool(_ context.Context, _ ToolCall, _ *ToolResult) error {
	return &ErrHalt{Response: p.response}
}

// errors used in defensive assertions below
var _ = errors.New

func TestLLMAgentPreProcessorHalt(t *testing.T) {
	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "should not reach"}},
	}

	agent := NewLLMAgent("guarded", "Guarded agent", provider,
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
		responses: []ChatResponse{{Content: "raw response"}},
	}

	agent := NewLLMAgent("modified", "Modified agent", provider,
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
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
	}

	agent := NewLLMAgent("redacted", "Redacted agent", provider,
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
