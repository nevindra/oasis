package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// --- test processors ---

// appendProcessor is a PreProcessor that appends a user message.
type appendProcessor struct {
	text string
}

func (p *appendProcessor) PreLLM(_ context.Context, req *ChatRequest) error {
	req.Messages = append(req.Messages, UserMessage(p.text))
	return nil
}

// uppercaseProcessor is a PostProcessor that uppercases the response content.
type uppercaseProcessor struct{}

func (p *uppercaseProcessor) PostLLM(_ context.Context, resp *ChatResponse) error {
	resp.Content = "[modified] " + resp.Content
	return nil
}

// redactToolProcessor is a PostToolProcessor that prefixes tool results.
type redactToolProcessor struct{}

func (p *redactToolProcessor) PostTool(_ context.Context, _ ToolCall, result *ToolResult) error {
	result.Content = "[redacted] " + result.Content
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

// errorProcessor returns a non-halt error.
type errorProcessor struct{}

func (p *errorProcessor) PreLLM(_ context.Context, _ *ChatRequest) error {
	return errors.New("infra failure")
}

// allPhasesProcessor implements all three interfaces, recording calls.
type allPhasesProcessor struct {
	preCalled     bool
	postCalled    bool
	toolCalled    bool
}

func (p *allPhasesProcessor) PreLLM(_ context.Context, _ *ChatRequest) error {
	p.preCalled = true
	return nil
}

func (p *allPhasesProcessor) PostLLM(_ context.Context, _ *ChatResponse) error {
	p.postCalled = true
	return nil
}

func (p *allPhasesProcessor) PostTool(_ context.Context, _ ToolCall, _ *ToolResult) error {
	p.toolCalled = true
	return nil
}

// --- ProcessorChain tests ---

func TestProcessorChainRunPreLLM(t *testing.T) {
	chain := NewProcessorChain()
	chain.Add(&appendProcessor{text: "first"})
	chain.Add(&appendProcessor{text: "second"})

	req := ChatRequest{Messages: []ChatMessage{UserMessage("hello")}}
	if err := chain.RunPreLLM(context.Background(), &req); err != nil {
		t.Fatal(err)
	}

	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(req.Messages))
	}
	if req.Messages[1].Content != "first" {
		t.Errorf("messages[1] = %q, want %q", req.Messages[1].Content, "first")
	}
	if req.Messages[2].Content != "second" {
		t.Errorf("messages[2] = %q, want %q", req.Messages[2].Content, "second")
	}
}

func TestProcessorChainRunPostLLM(t *testing.T) {
	chain := NewProcessorChain()
	chain.Add(&uppercaseProcessor{})

	resp := ChatResponse{Content: "hello"}
	if err := chain.RunPostLLM(context.Background(), &resp); err != nil {
		t.Fatal(err)
	}

	if resp.Content != "[modified] hello" {
		t.Errorf("content = %q, want %q", resp.Content, "[modified] hello")
	}
}

func TestProcessorChainRunPostTool(t *testing.T) {
	chain := NewProcessorChain()
	chain.Add(&redactToolProcessor{})

	tc := ToolCall{ID: "1", Name: "test", Args: json.RawMessage(`{}`)}
	result := ToolResult{Content: "secret data"}
	if err := chain.RunPostTool(context.Background(), tc, &result); err != nil {
		t.Fatal(err)
	}

	if result.Content != "[redacted] secret data" {
		t.Errorf("content = %q, want %q", result.Content, "[redacted] secret data")
	}
}

func TestProcessorChainHaltStopsChain(t *testing.T) {
	chain := NewProcessorChain()
	chain.Add(&haltProcessor{response: "blocked"})
	chain.Add(&appendProcessor{text: "should not run"})

	req := ChatRequest{Messages: []ChatMessage{UserMessage("hello")}}
	err := chain.RunPreLLM(context.Background(), &req)

	var halt *ErrHalt
	if !errors.As(err, &halt) {
		t.Fatalf("expected ErrHalt, got %v", err)
	}
	if halt.Response != "blocked" {
		t.Errorf("halt response = %q, want %q", halt.Response, "blocked")
	}
	// Second processor should not have run
	if len(req.Messages) != 1 {
		t.Errorf("expected 1 message (unchanged), got %d", len(req.Messages))
	}
}

func TestProcessorChainInfraError(t *testing.T) {
	chain := NewProcessorChain()
	chain.Add(&errorProcessor{})

	req := ChatRequest{Messages: []ChatMessage{UserMessage("hello")}}
	err := chain.RunPreLLM(context.Background(), &req)

	if err == nil {
		t.Fatal("expected error")
	}
	var halt *ErrHalt
	if errors.As(err, &halt) {
		t.Error("expected non-halt error")
	}
	if err.Error() != "infra failure" {
		t.Errorf("error = %q, want %q", err.Error(), "infra failure")
	}
}

func TestProcessorChainEmptyIsNoOp(t *testing.T) {
	chain := NewProcessorChain()

	req := ChatRequest{Messages: []ChatMessage{UserMessage("hello")}}
	if err := chain.RunPreLLM(context.Background(), &req); err != nil {
		t.Fatal(err)
	}

	resp := ChatResponse{Content: "hello"}
	if err := chain.RunPostLLM(context.Background(), &resp); err != nil {
		t.Fatal(err)
	}

	result := ToolResult{Content: "data"}
	if err := chain.RunPostTool(context.Background(), ToolCall{}, &result); err != nil {
		t.Fatal(err)
	}
}

func TestProcessorChainTypeAssertion(t *testing.T) {
	// appendProcessor only implements PreProcessor
	// RunPostLLM and RunPostTool should skip it without error
	chain := NewProcessorChain()
	chain.Add(&appendProcessor{text: "pre-only"})

	resp := ChatResponse{Content: "untouched"}
	if err := chain.RunPostLLM(context.Background(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Content != "untouched" {
		t.Errorf("content = %q, want %q", resp.Content, "untouched")
	}

	result := ToolResult{Content: "untouched"}
	if err := chain.RunPostTool(context.Background(), ToolCall{}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Content != "untouched" {
		t.Errorf("content = %q, want %q", result.Content, "untouched")
	}
}

func TestProcessorChainAllPhases(t *testing.T) {
	p := &allPhasesProcessor{}
	chain := NewProcessorChain()
	chain.Add(p)

	req := ChatRequest{Messages: []ChatMessage{UserMessage("hello")}}
	_ = chain.RunPreLLM(context.Background(), &req)

	resp := ChatResponse{Content: "hello"}
	_ = chain.RunPostLLM(context.Background(), &resp)

	result := ToolResult{Content: "data"}
	_ = chain.RunPostTool(context.Background(), ToolCall{}, &result)

	if !p.preCalled {
		t.Error("PreLLM was not called")
	}
	if !p.postCalled {
		t.Error("PostLLM was not called")
	}
	if !p.toolCalled {
		t.Error("PostTool was not called")
	}
}

func TestProcessorChainAddPanicsOnInvalidType(t *testing.T) {
	chain := NewProcessorChain()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for invalid processor type")
		}
	}()

	chain.Add("not a processor")
}

func TestProcessorChainLen(t *testing.T) {
	chain := NewProcessorChain()
	if chain.Len() != 0 {
		t.Errorf("Len() = %d, want 0", chain.Len())
	}

	chain.Add(&appendProcessor{text: "a"})
	chain.Add(&uppercaseProcessor{})
	if chain.Len() != 2 {
		t.Errorf("Len() = %d, want 2", chain.Len())
	}
}

// --- Integration: LLMAgent with processors ---

func TestLLMAgentPreProcessorHalt(t *testing.T) {
	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "should not reach"}},
	}

	agent := NewLLMAgent("guarded", "Guarded agent", provider,
		WithProcessors(&haltProcessor{response: "blocked by guardrail"}),
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
		WithProcessors(&uppercaseProcessor{}),
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
		WithProcessors(&redactToolProcessor{}),
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

func TestErrHaltMessage(t *testing.T) {
	err := &ErrHalt{Response: "test halt"}
	if err.Error() != "processor halted: test halt" {
		t.Errorf("Error() = %q, want %q", err.Error(), "processor halted: test halt")
	}
}
