package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// stubAgent is a minimal Agent for testing.
type stubAgent struct {
	name string
	desc string
	fn   func(AgentTask) (AgentResult, error)
}

func (s *stubAgent) Name() string        { return s.name }
func (s *stubAgent) Description() string { return s.desc }
func (s *stubAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return s.fn(task)
}

func TestAgentInterface(t *testing.T) {
	agent := &stubAgent{
		name: "echo",
		desc: "Echoes the input",
		fn: func(task AgentTask) (AgentResult, error) {
			return AgentResult{Output: task.Input}, nil
		},
	}

	// Verify interface compliance
	var _ Agent = agent

	if agent.Name() != "echo" {
		t.Errorf("Name() = %q, want %q", agent.Name(), "echo")
	}
	if agent.Description() != "Echoes the input" {
		t.Errorf("Description() = %q, want %q", agent.Description(), "Echoes the input")
	}

	result, err := agent.Execute(context.Background(), AgentTask{
		Input:   "hello",
		Context: map[string]string{"user_id": "123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "hello" {
		t.Errorf("Output = %q, want %q", result.Output, "hello")
	}
}

// mockProvider is a test Provider that returns canned responses.
type mockProvider struct {
	name      string
	responses []ChatResponse // popped in order
	idx       int
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	return m.next(), nil
}
func (m *mockProvider) ChatWithTools(ctx context.Context, req ChatRequest, tools []ToolDefinition) (ChatResponse, error) {
	return m.next(), nil
}
func (m *mockProvider) ChatStream(ctx context.Context, req ChatRequest, ch chan<- string) (ChatResponse, error) {
	defer close(ch)
	resp := m.next()
	ch <- resp.Content
	return resp, nil
}
func (m *mockProvider) next() ChatResponse {
	if m.idx >= len(m.responses) {
		return ChatResponse{Content: "exhausted"}
	}
	resp := m.responses[m.idx]
	m.idx++
	return resp
}

func TestLLMAgentNoTools(t *testing.T) {
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{Content: "Hello! I'm your assistant."},
		},
	}

	agent := NewLLMAgent("greeter", "A friendly greeter", provider)
	result, err := agent.Execute(context.Background(), AgentTask{Input: "Hi there"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "Hello! I'm your assistant." {
		t.Errorf("Output = %q, want %q", result.Output, "Hello! I'm your assistant.")
	}
}

func TestLLMAgentWithTools(t *testing.T) {
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			// First response: call the greet tool
			{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{"name":"world"}`)}}},
			// Second response: final text using tool result
			{Content: "The greeting is: hello world"},
		},
	}

	agent := NewLLMAgent("tooluser", "Uses tools", provider,
		WithTools(mockTool{}),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "Greet the world"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "The greeting is: hello world" {
		t.Errorf("Output = %q, want %q", result.Output, "The greeting is: hello world")
	}
}

func TestLLMAgentMaxIterations(t *testing.T) {
	// Provider always returns tool calls — should hit max iterations
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{ToolCalls: []ToolCall{{ID: "2", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{ToolCalls: []ToolCall{{ID: "3", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{Content: "forced synthesis"}, // force-synthesis response
		},
	}

	agent := NewLLMAgent("looper", "Loops forever", provider,
		WithTools(mockTool{}),
		WithMaxIter(3),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "Loop"})
	if err != nil {
		t.Fatal(err)
	}
	// Should get the forced synthesis response (4th response)
	if result.Output == "" {
		t.Error("expected non-empty output after max iterations")
	}
}

func TestLLMAgentInterfaceCompliance(t *testing.T) {
	agent := NewLLMAgent("test", "test agent", &mockProvider{name: "test"})
	var _ Agent = agent
}

func TestNetworkRoutesToAgent(t *testing.T) {
	// The router will call agent_echo, which is a stubAgent
	echoAgent := &stubAgent{
		name: "echo",
		desc: "Echoes the input back",
		fn: func(task AgentTask) (AgentResult, error) {
			return AgentResult{Output: "echoed: " + task.Input}, nil
		},
	}

	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			// Router decides to call agent_echo
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_echo",
				Args: json.RawMessage(`{"task":"say hello"}`),
			}}},
			// Router produces final response using agent result
			{Content: "The echo agent said: echoed: say hello"},
		},
	}

	network := NewNetwork("coordinator", "Routes to agents", router,
		WithAgents(echoAgent),
	)

	result, err := network.Execute(context.Background(), AgentTask{Input: "Please echo something"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "The echo agent said: echoed: say hello" {
		t.Errorf("Output = %q, want %q", result.Output, "The echo agent said: echoed: say hello")
	}
}

func TestNetworkRoutesToTool(t *testing.T) {
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			// Router calls a direct tool
			{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
			// Final response
			{Content: "Tool said: hello from greet"},
		},
	}

	network := NewNetwork("coordinator", "Routes to tools", router,
		WithTools(mockTool{}),
	)

	result, err := network.Execute(context.Background(), AgentTask{Input: "Say hi"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "Tool said: hello from greet" {
		t.Errorf("Output = %q, want %q", result.Output, "Tool said: hello from greet")
	}
}

func TestNetworkComposition(t *testing.T) {
	// Inner network (implements Agent)
	inner := NewNetwork("inner", "Inner network", &mockProvider{
		name: "inner-router",
		responses: []ChatResponse{
			{Content: "inner result"},
		},
	})

	// Outer network routes to inner network
	outer := NewNetwork("outer", "Outer network", &mockProvider{
		name: "outer-router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_inner",
				Args: json.RawMessage(`{"task":"do inner work"}`),
			}}},
			{Content: "outer got: inner result"},
		},
	}, WithAgents(inner))

	result, err := outer.Execute(context.Background(), AgentTask{Input: "Compose"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "outer got: inner result" {
		t.Errorf("Output = %q, want %q", result.Output, "outer got: inner result")
	}
}

func TestNetworkInterfaceCompliance(t *testing.T) {
	network := NewNetwork("test", "test network", &mockProvider{name: "test"})
	var _ Agent = network
}

// --- Error path and edge case tests ---

// errProvider always returns an error from all methods.
type errProvider struct {
	name string
	err  error
}

func (p *errProvider) Name() string { return p.name }
func (p *errProvider) Chat(_ context.Context, _ ChatRequest) (ChatResponse, error) {
	return ChatResponse{}, p.err
}
func (p *errProvider) ChatWithTools(_ context.Context, _ ChatRequest, _ []ToolDefinition) (ChatResponse, error) {
	return ChatResponse{}, p.err
}
func (p *errProvider) ChatStream(_ context.Context, _ ChatRequest, ch chan<- string) (ChatResponse, error) {
	defer close(ch)
	return ChatResponse{}, p.err
}

// ctxProvider returns the context's error (simulates context-aware provider).
type ctxProvider struct{ name string }

func (p *ctxProvider) Name() string { return p.name }
func (p *ctxProvider) Chat(ctx context.Context, _ ChatRequest) (ChatResponse, error) {
	return ChatResponse{}, ctx.Err()
}
func (p *ctxProvider) ChatWithTools(ctx context.Context, _ ChatRequest, _ []ToolDefinition) (ChatResponse, error) {
	return ChatResponse{}, ctx.Err()
}
func (p *ctxProvider) ChatStream(ctx context.Context, _ ChatRequest, ch chan<- string) (ChatResponse, error) {
	defer close(ch)
	return ChatResponse{}, ctx.Err()
}

func TestLLMAgentProviderError(t *testing.T) {
	agent := NewLLMAgent("broken", "Broken agent", &errProvider{
		name: "fail",
		err:  errors.New("api timeout"),
	})

	_, err := agent.Execute(context.Background(), AgentTask{Input: "hello"})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "api timeout" {
		t.Errorf("error = %q, want %q", err.Error(), "api timeout")
	}
}

func TestLLMAgentProviderErrorWithTools(t *testing.T) {
	// ChatWithTools path (provider has tools registered)
	agent := NewLLMAgent("broken", "Broken agent", &errProvider{
		name: "fail",
		err:  errors.New("rate limited"),
	}, WithTools(mockTool{}))

	_, err := agent.Execute(context.Background(), AgentTask{Input: "hello"})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "rate limited" {
		t.Errorf("error = %q, want %q", err.Error(), "rate limited")
	}
}

func TestLLMAgentContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Execute

	agent := NewLLMAgent("ctx", "Context test", &ctxProvider{name: "ctx"})

	_, err := agent.Execute(ctx, AgentTask{Input: "hello"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestLLMAgentUsageAccumulation(t *testing.T) {
	// 2 tool-call rounds + 1 final text = 3 LLM calls, each with 100 input / 50 output
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{
				ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}},
				Usage:     Usage{InputTokens: 100, OutputTokens: 50},
			},
			{
				ToolCalls: []ToolCall{{ID: "2", Name: "greet", Args: json.RawMessage(`{}`)}},
				Usage:     Usage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Content: "done",
				Usage:   Usage{InputTokens: 100, OutputTokens: 50},
			},
		},
	}

	agent := NewLLMAgent("counter", "Counts usage", provider, WithTools(mockTool{}))
	result, err := agent.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 150 {
		t.Errorf("OutputTokens = %d, want 150", result.Usage.OutputTokens)
	}
}

func TestLLMAgentEmptyInput(t *testing.T) {
	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "ok"}},
	}
	agent := NewLLMAgent("empty", "Handles empty", provider)

	result, err := agent.Execute(context.Background(), AgentTask{Input: ""})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "ok" {
		t.Errorf("Output = %q, want %q", result.Output, "ok")
	}
}

func TestLLMAgentWithSystemPrompt(t *testing.T) {
	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "I am helpful"}},
	}
	agent := NewLLMAgent("prompted", "Has system prompt", provider,
		WithPrompt("You are helpful"),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "I am helpful" {
		t.Errorf("Output = %q, want %q", result.Output, "I am helpful")
	}
}

func TestNetworkUnknownAgent(t *testing.T) {
	// Router calls agent_nonexistent — dispatch returns error string, network continues
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_nonexistent",
				Args: json.RawMessage(`{"task":"do something"}`),
			}}},
			{Content: "handled the error"},
		},
	}

	network := NewNetwork("net", "Test network", router)
	result, err := network.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatalf("network should not fail on unknown agent: %v", err)
	}
	if result.Output != "handled the error" {
		t.Errorf("Output = %q, want %q", result.Output, "handled the error")
	}
}

func TestNetworkSubagentError(t *testing.T) {
	// Sub-agent returns an error — network should stringify it and continue
	failAgent := &stubAgent{
		name: "failer",
		desc: "Always fails",
		fn: func(_ AgentTask) (AgentResult, error) {
			return AgentResult{}, errors.New("subagent crashed")
		},
	}

	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_failer",
				Args: json.RawMessage(`{"task":"fail"}`),
			}}},
			{Content: "recovered from error"},
		},
	}

	network := NewNetwork("net", "Test network", router, WithAgents(failAgent))
	result, err := network.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatalf("network should not propagate subagent error: %v", err)
	}
	if result.Output != "recovered from error" {
		t.Errorf("Output = %q, want %q", result.Output, "recovered from error")
	}
}

func TestNetworkRouterError(t *testing.T) {
	// Router provider itself fails — should propagate error
	network := NewNetwork("net", "Test network", &errProvider{
		name: "broken-router",
		err:  errors.New("router down"),
	})

	_, err := network.Execute(context.Background(), AgentTask{Input: "test"})
	if err == nil {
		t.Fatal("expected error when router fails")
	}
	if err.Error() != "router down" {
		t.Errorf("error = %q, want %q", err.Error(), "router down")
	}
}

func TestNetworkMaxIterations(t *testing.T) {
	// Router always returns tool calls — should hit max iterations and force synthesis
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{ToolCalls: []ToolCall{{ID: "2", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{ToolCalls: []ToolCall{{ID: "3", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{Content: "forced synthesis"},
		},
	}

	network := NewNetwork("net", "Test", router,
		WithTools(mockTool{}),
		WithMaxIter(3),
	)

	result, err := network.Execute(context.Background(), AgentTask{Input: "loop"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output == "" {
		t.Error("expected non-empty output after max iterations")
	}
}

func TestNetworkInvalidAgentArgs(t *testing.T) {
	// Router calls agent with invalid JSON args — dispatch returns error string
	echoAgent := &stubAgent{
		name: "echo",
		desc: "Echoes",
		fn: func(task AgentTask) (AgentResult, error) {
			return AgentResult{Output: task.Input}, nil
		},
	}

	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_echo",
				Args: json.RawMessage(`not valid json`),
			}}},
			{Content: "handled bad args"},
		},
	}

	network := NewNetwork("net", "Test", router, WithAgents(echoAgent))
	result, err := network.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatalf("network should handle invalid args gracefully: %v", err)
	}
	if result.Output != "handled bad args" {
		t.Errorf("Output = %q, want %q", result.Output, "handled bad args")
	}
}
