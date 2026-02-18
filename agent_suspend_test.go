package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// suspendingProcessor is a PostProcessor that suspends on a trigger tool name.
type suspendingProcessor struct {
	triggerTool string
	payload     json.RawMessage
}

func (p *suspendingProcessor) PostLLM(_ context.Context, resp *ChatResponse) error {
	for _, tc := range resp.ToolCalls {
		if tc.Name == p.triggerTool {
			return Suspend(p.payload)
		}
	}
	return nil
}

// suspendingPreProcessor is a PreProcessor that always suspends.
type suspendingPreProcessor struct {
	payload json.RawMessage
}

func (p *suspendingPreProcessor) PreLLM(_ context.Context, _ *ChatRequest) error {
	return Suspend(p.payload)
}

// suspendingPostToolProcessor is a PostToolProcessor that suspends on a trigger tool.
type suspendingPostToolProcessor struct {
	triggerTool string
	payload     json.RawMessage
}

func (p *suspendingPostToolProcessor) PostTool(_ context.Context, call ToolCall, _ *ToolResult) error {
	if call.Name == p.triggerTool {
		return Suspend(p.payload)
	}
	return nil
}

func TestRunLoopPostProcessorSuspend(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			{Content: "", ToolCalls: []ToolCall{{ID: "1", Name: "dangerous_action", Args: json.RawMessage(`{}`)}}},
		},
	}

	chain := NewProcessorChain()
	chain.Add(&suspendingProcessor{
		triggerTool: "dangerous_action",
		payload:     json.RawMessage(`{"action": "approve_dangerous_action"}`),
	})

	cfg := loopConfig{
		name:       "test",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "dangerous_action", Description: "test"}},
		processors: chain,
		maxIter:    5,
		mem:        &agentMemory{},
		dispatch:   func(_ context.Context, tc ToolCall) (string, Usage) { return "ok", Usage{} },
	}

	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}
	if suspended.Step != "test" {
		t.Errorf("Step = %q, want %q", suspended.Step, "test")
	}
	if string(suspended.Payload) != `{"action": "approve_dangerous_action"}` {
		t.Errorf("Payload = %s", suspended.Payload)
	}
}

func TestRunLoopSuspendResume(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			// First call: LLM wants to call dangerous tool.
			{Content: "", ToolCalls: []ToolCall{{ID: "1", Name: "delete", Args: json.RawMessage(`{}`)}}},
			// After resume: LLM sees human input and responds.
			{Content: "Action completed with approval"},
		},
	}

	chain := NewProcessorChain()
	chain.Add(&suspendingProcessor{
		triggerTool: "delete",
		payload:     json.RawMessage(`{"confirm": "delete?"}`),
	})

	cfg := loopConfig{
		name:       "test",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "delete", Description: "test"}},
		processors: chain,
		maxIter:    5,
		mem:        &agentMemory{},
		dispatch:   func(_ context.Context, tc ToolCall) (string, Usage) { return "deleted", Usage{} },
	}

	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "delete item"}, nil)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	result, err := suspended.Resume(context.Background(), json.RawMessage(`"approved"`))
	if err != nil {
		t.Fatalf("Resume error: %v", err)
	}
	if result.Output != "Action completed with approval" {
		t.Errorf("Output = %q", result.Output)
	}
}

func TestRunLoopSuspendClosesStreamChannel(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "danger", Args: json.RawMessage(`{}`)}}},
		},
	}

	chain := NewProcessorChain()
	chain.Add(&suspendingProcessor{triggerTool: "danger", payload: json.RawMessage(`{}`)})

	cfg := loopConfig{
		name:       "test",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "danger", Description: "test"}},
		processors: chain,
		maxIter:    5,
		mem:        &agentMemory{},
		dispatch:   func(_ context.Context, tc ToolCall) (string, Usage) { return "ok", Usage{} },
	}

	ch := make(chan string, 10)
	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, ch)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	// Channel should be closed.
	_, open := <-ch
	if open {
		t.Error("channel should be closed on suspend")
	}
}

func TestRunLoopPreProcessorSuspend(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{{Content: "should not reach"}},
	}

	chain := NewProcessorChain()
	chain.Add(&suspendingPreProcessor{
		payload: json.RawMessage(`{"gate": "pre"}`),
	})

	cfg := loopConfig{
		name:       "test-pre",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "some_tool", Description: "test"}},
		processors: chain,
		maxIter:    5,
		mem:        &agentMemory{},
		dispatch:   func(_ context.Context, tc ToolCall) (string, Usage) { return "ok", Usage{} },
	}

	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}
	if suspended.Step != "test-pre" {
		t.Errorf("Step = %q, want %q", suspended.Step, "test-pre")
	}
	if string(suspended.Payload) != `{"gate": "pre"}` {
		t.Errorf("Payload = %s", suspended.Payload)
	}
}

func TestRunLoopPostToolProcessorSuspend(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "risky_tool", Args: json.RawMessage(`{}`)}}},
		},
	}

	chain := NewProcessorChain()
	chain.Add(&suspendingPostToolProcessor{
		triggerTool: "risky_tool",
		payload:     json.RawMessage(`{"gate": "post_tool"}`),
	})

	cfg := loopConfig{
		name:       "test-posttool",
		provider:   provider,
		tools:      []ToolDefinition{{Name: "risky_tool", Description: "test"}},
		processors: chain,
		maxIter:    5,
		mem:        &agentMemory{},
		dispatch:   func(_ context.Context, tc ToolCall) (string, Usage) { return "executed", Usage{} },
	}

	_, err := runLoop(context.Background(), cfg, AgentTask{Input: "go"}, nil)

	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}
	if suspended.Step != "test-posttool" {
		t.Errorf("Step = %q, want %q", suspended.Step, "test-posttool")
	}
	if string(suspended.Payload) != `{"gate": "post_tool"}` {
		t.Errorf("Payload = %s", suspended.Payload)
	}
}
