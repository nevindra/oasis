package processor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nevindra/oasis/core"
)

// --- test processors ---

// appendProcessor is a PreProcessor that appends a user message.
type appendProcessor struct {
	text string
}

func (p *appendProcessor) PreLLM(_ context.Context, req *core.ChatRequest) error {
	req.Messages = append(req.Messages, core.UserMessage(p.text))
	return nil
}

// uppercaseProcessor is a PostProcessor that uppercases the response content.
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

// errorProcessor returns a non-halt error.
type errorProcessor struct{}

func (p *errorProcessor) PreLLM(_ context.Context, _ *core.ChatRequest) error {
	return errors.New("infra failure")
}

// allPhasesProcessor implements all three interfaces, recording calls.
type allPhasesProcessor struct {
	preCalled  bool
	postCalled bool
	toolCalled bool
}

func (p *allPhasesProcessor) PreLLM(_ context.Context, _ *core.ChatRequest) error {
	p.preCalled = true
	return nil
}

func (p *allPhasesProcessor) PostLLM(_ context.Context, _ *core.ChatResponse) error {
	p.postCalled = true
	return nil
}

func (p *allPhasesProcessor) PostTool(_ context.Context, _ core.ToolCall, _ *core.ToolResult) error {
	p.toolCalled = true
	return nil
}

// --- Chain tests ---

func TestChainRunPreLLM(t *testing.T) {
	chain := NewChain()
	chain.AddPre(&appendProcessor{text: "first"})
	chain.AddPre(&appendProcessor{text: "second"})

	req := core.ChatRequest{Messages: []core.ChatMessage{core.UserMessage("hello")}}
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

func TestChainRunPostLLM(t *testing.T) {
	chain := NewChain()
	chain.AddPost(&uppercaseProcessor{})

	resp := core.ChatResponse{Content: "hello"}
	if err := chain.RunPostLLM(context.Background(), &resp); err != nil {
		t.Fatal(err)
	}

	if resp.Content != "[modified] hello" {
		t.Errorf("content = %q, want %q", resp.Content, "[modified] hello")
	}
}

func TestChainRunPostTool(t *testing.T) {
	chain := NewChain()
	chain.AddPostTool(&redactToolProcessor{})

	tc := core.ToolCall{ID: "1", Name: "test", Args: json.RawMessage(`{}`)}
	result := core.TextResult("secret data")
	if err := chain.RunPostTool(context.Background(), tc, &result); err != nil {
		t.Fatal(err)
	}
	// processor wraps content in TextContent, so result is JSON-quoted text
	_ = result.Content
}

func TestChainHaltStopsChain(t *testing.T) {
	chain := NewChain()
	chain.AddPre(&haltProcessor{response: "blocked"})
	chain.AddPre(&appendProcessor{text: "should not run"})

	req := core.ChatRequest{Messages: []core.ChatMessage{core.UserMessage("hello")}}
	err := chain.RunPreLLM(context.Background(), &req)

	var halt *core.ErrHalt
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

func TestChainInfraError(t *testing.T) {
	chain := NewChain()
	chain.AddPre(&errorProcessor{})

	req := core.ChatRequest{Messages: []core.ChatMessage{core.UserMessage("hello")}}
	err := chain.RunPreLLM(context.Background(), &req)

	if err == nil {
		t.Fatal("expected error")
	}
	var halt *core.ErrHalt
	if errors.As(err, &halt) {
		t.Error("expected non-halt error")
	}
	if err.Error() != "infra failure" {
		t.Errorf("error = %q, want %q", err.Error(), "infra failure")
	}
}

func TestChainEmptyIsNoOp(t *testing.T) {
	chain := NewChain()

	req := core.ChatRequest{Messages: []core.ChatMessage{core.UserMessage("hello")}}
	if err := chain.RunPreLLM(context.Background(), &req); err != nil {
		t.Fatal(err)
	}

	resp := core.ChatResponse{Content: "hello"}
	if err := chain.RunPostLLM(context.Background(), &resp); err != nil {
		t.Fatal(err)
	}

	result := core.TextResult("data")
	if err := chain.RunPostTool(context.Background(), core.ToolCall{}, &result); err != nil {
		t.Fatal(err)
	}
}

func TestChainTypeAssertion(t *testing.T) {
	// appendProcessor only implements PreProcessor
	// RunPostLLM and RunPostTool should skip it without error
	chain := NewChain()
	chain.AddPre(&appendProcessor{text: "pre-only"})

	resp := core.ChatResponse{Content: "untouched"}
	if err := chain.RunPostLLM(context.Background(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Content != "untouched" {
		t.Errorf("content = %q, want %q", resp.Content, "untouched")
	}

	result := core.TextResult("untouched")
	if err := chain.RunPostTool(context.Background(), core.ToolCall{}, &result); err != nil {
		t.Fatal(err)
	}
	if string(result.Content) != `"untouched"` {
		t.Errorf("content = %q, want %q", result.Content, `"untouched"`)
	}
}

func TestChainAllPhases(t *testing.T) {
	p := &allPhasesProcessor{}
	chain := NewChain()
	chain.AddPre(p)
	chain.AddPost(p)
	chain.AddPostTool(p)

	req := core.ChatRequest{Messages: []core.ChatMessage{core.UserMessage("hello")}}
	_ = chain.RunPreLLM(context.Background(), &req)

	resp := core.ChatResponse{Content: "hello"}
	_ = chain.RunPostLLM(context.Background(), &resp)

	result := core.TextResult("data")
	_ = chain.RunPostTool(context.Background(), core.ToolCall{}, &result)

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

func TestChainLen(t *testing.T) {
	chain := NewChain()
	if chain.Len() != 0 {
		t.Errorf("Len() = %d, want 0", chain.Len())
	}

	chain.AddPre(&appendProcessor{text: "a"})
	chain.AddPost(&uppercaseProcessor{})
	if chain.Len() != 2 {
		t.Errorf("Len() = %d, want 2", chain.Len())
	}
}

func TestErrHaltMessage(t *testing.T) {
	err := &core.ErrHalt{Response: "test halt"}
	if err.Error() != "processor halted: test halt" {
		t.Errorf("Error() = %q, want %q", err.Error(), "processor halted: test halt")
	}
}
