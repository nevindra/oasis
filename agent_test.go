package oasis

import (
	"context"
	"encoding/json"
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
