package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// mockInputHandler is a test InputHandler that returns canned responses.
type mockInputHandler struct {
	response InputResponse
	err      error
	received []InputRequest // records all requests for assertions
}

func (m *mockInputHandler) RequestInput(_ context.Context, req InputRequest) (InputResponse, error) {
	m.received = append(m.received, req)
	return m.response, m.err
}

// --- Core types tests ---

func TestInputHandlerFromContextMissing(t *testing.T) {
	ctx := context.Background()
	handler, ok := InputHandlerFromContext(ctx)
	if ok {
		t.Error("expected ok=false for empty context")
	}
	if handler != nil {
		t.Error("expected nil handler for empty context")
	}
}

func TestInputHandlerContextRoundTrip(t *testing.T) {
	h := &mockInputHandler{response: InputResponse{Value: "yes"}}
	ctx := WithInputHandlerContext(context.Background(), h)

	got, ok := InputHandlerFromContext(ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != h {
		t.Error("expected same handler instance")
	}
}

// --- LLMAgent ask_user tests ---

func TestLLMAgentAskUserToolAppearsWithHandler(t *testing.T) {
	// When InputHandler is set, ask_user tool should appear in tool defs
	handler := &mockInputHandler{response: InputResponse{Value: "42"}}
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			// LLM calls ask_user
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "ask_user",
				Args: json.RawMessage(`{"question":"What is the answer?"}`),
			}}},
			// LLM uses the answer
			{Content: "The answer is 42"},
		},
	}

	agent := NewLLMAgent("asker", "Asks questions", provider,
		WithInputHandler(handler),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "Find the answer"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "The answer is 42" {
		t.Errorf("Output = %q, want %q", result.Output, "The answer is 42")
	}

	// Verify handler received the question
	if len(handler.received) != 1 {
		t.Fatalf("handler received %d requests, want 1", len(handler.received))
	}
	if handler.received[0].Question != "What is the answer?" {
		t.Errorf("question = %q, want %q", handler.received[0].Question, "What is the answer?")
	}
}

func TestLLMAgentAskUserWithOptions(t *testing.T) {
	handler := &mockInputHandler{response: InputResponse{Value: "Yes"}}
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "ask_user",
				Args: json.RawMessage(`{"question":"Proceed?","options":["Yes","No"]}`),
			}}},
			{Content: "Proceeding"},
		},
	}

	agent := NewLLMAgent("confirmer", "Confirms", provider,
		WithInputHandler(handler),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "Do the thing"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "Proceeding" {
		t.Errorf("Output = %q, want %q", result.Output, "Proceeding")
	}

	// Verify options were passed through
	if len(handler.received[0].Options) != 2 {
		t.Fatalf("options = %v, want [Yes No]", handler.received[0].Options)
	}
}

func TestLLMAgentAskUserHandlerError(t *testing.T) {
	// When ask_user handler fails, the error is converted to a tool result
	// string (consistent with Network.dispatch behavior). The LLM sees the
	// error and can respond accordingly.
	handler := &mockInputHandler{err: errors.New("timeout")}
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "ask_user",
				Args: json.RawMessage(`{"question":"hello?"}`),
			}}},
			{Content: "could not reach user"},
		},
	}

	agent := NewLLMAgent("asker", "Asks", provider,
		WithInputHandler(handler),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "ask"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "could not reach user" {
		t.Errorf("Output = %q, want %q", result.Output, "could not reach user")
	}
}

func TestLLMAgentNoHandlerNoAskUser(t *testing.T) {
	// Without InputHandler, ask_user should NOT be available.
	// LLM somehow calls ask_user anyway — should be treated as unknown tool.
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "ask_user",
				Args: json.RawMessage(`{"question":"hello?"}`),
			}}},
			{Content: "handled gracefully"},
		},
	}

	agent := NewLLMAgent("no-handler", "No handler", provider,
		WithTools(mockTool{}),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	// Should continue past the unrecognized tool call
	if result.Output != "handled gracefully" {
		t.Errorf("Output = %q, want %q", result.Output, "handled gracefully")
	}
}

func TestLLMAgentAskUserMetadata(t *testing.T) {
	handler := &mockInputHandler{response: InputResponse{Value: "ok"}}
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "ask_user",
				Args: json.RawMessage(`{"question":"confirm?"}`),
			}}},
			{Content: "done"},
		},
	}

	agent := NewLLMAgent("meta-agent", "Tests metadata", provider,
		WithInputHandler(handler),
	)

	agent.Execute(context.Background(), AgentTask{Input: "go"})

	// Verify metadata is auto-populated
	if len(handler.received) != 1 {
		t.Fatal("expected 1 request")
	}
	meta := handler.received[0].Metadata
	if meta["agent"] != "meta-agent" {
		t.Errorf("metadata[agent] = %q, want %q", meta["agent"], "meta-agent")
	}
	if meta["source"] != "llm" {
		t.Errorf("metadata[source] = %q, want %q", meta["source"], "llm")
	}
}

// --- Network handler propagation tests ---

func TestNetworkPropagatesHandlerToSubagent(t *testing.T) {
	handler := &mockInputHandler{response: InputResponse{Value: "approved"}}

	// Inner agent uses ask_user — needs handler from Network context
	innerProvider := &mockProvider{
		name: "inner",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "ask_user",
				Args: json.RawMessage(`{"question":"May I?"}`),
			}}},
			{Content: "User said: approved"},
		},
	}
	inner := NewLLMAgent("inner", "Inner agent that asks", innerProvider,
		WithInputHandler(handler),
	)

	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_inner",
				Args: json.RawMessage(`{"task":"ask permission"}`),
			}}},
			{Content: "inner said: User said: approved"},
		},
	}

	network := NewNetwork("net", "Network with handler", router,
		WithAgents(inner),
		WithInputHandler(handler),
	)

	result, err := network.Execute(context.Background(), AgentTask{Input: "do work"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "inner said: User said: approved" {
		t.Errorf("Output = %q, want %q", result.Output, "inner said: User said: approved")
	}

	// Handler should have been called by the inner agent
	if len(handler.received) != 1 {
		t.Fatalf("handler received %d requests, want 1", len(handler.received))
	}
	if handler.received[0].Question != "May I?" {
		t.Errorf("question = %q, want %q", handler.received[0].Question, "May I?")
	}
}

func TestNetworkAskUserDirectly(t *testing.T) {
	// Network router itself calls ask_user (not via subagent)
	handler := &mockInputHandler{response: InputResponse{Value: "go ahead"}}
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "ask_user",
				Args: json.RawMessage(`{"question":"Should I proceed?"}`),
			}}},
			{Content: "User confirmed, proceeding"},
		},
	}

	network := NewNetwork("net", "Network asks directly", router,
		WithInputHandler(handler),
	)

	result, err := network.Execute(context.Background(), AgentTask{Input: "check"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "User confirmed, proceeding" {
		t.Errorf("Output = %q, want %q", result.Output, "User confirmed, proceeding")
	}
	if len(handler.received) != 1 {
		t.Fatalf("handler received %d requests, want 1", len(handler.received))
	}
}

// --- Processor access to InputHandler tests ---

func TestProcessorAccessesInputHandler(t *testing.T) {
	handler := &mockInputHandler{response: InputResponse{Value: "approved"}}

	// Processor that uses InputHandlerFromContext to gate tool calls
	gateHit := false
	gate := &funcPostProcessor{fn: func(ctx context.Context, resp *ChatResponse) error {
		h, ok := InputHandlerFromContext(ctx)
		if !ok {
			return nil
		}
		for _, tc := range resp.ToolCalls {
			if tc.Name == "greet" {
				res, err := h.RequestInput(ctx, InputRequest{
					Question: "Allow greet?",
					Options:  []string{"Yes", "No"},
					Metadata: map[string]string{"source": "gate", "tool": tc.Name},
				})
				if err != nil {
					return err
				}
				gateHit = true
				if res.Value != "approved" {
					// Strip the tool call
					resp.ToolCalls = nil
				}
			}
		}
		return nil
	}}

	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{Content: "greeted successfully"},
		},
	}

	agent := NewLLMAgent("gated", "Gated agent", provider,
		WithTools(mockTool{}),
		WithInputHandler(handler),
		WithProcessors(gate),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "greet"})
	if err != nil {
		t.Fatal(err)
	}
	if !gateHit {
		t.Error("expected gate processor to be called")
	}
	if result.Output != "greeted successfully" {
		t.Errorf("Output = %q, want %q", result.Output, "greeted successfully")
	}
}

// funcPostProcessor is a test helper implementing PostProcessor via a function.
type funcPostProcessor struct {
	fn func(context.Context, *ChatResponse) error
}

func (f *funcPostProcessor) PostLLM(ctx context.Context, resp *ChatResponse) error {
	return f.fn(ctx, resp)
}
