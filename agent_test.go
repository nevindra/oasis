package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
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
		Context: map[string]any{"user_id": "123"},
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
func (m *mockProvider) ChatStream(ctx context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	resp := m.next()
	ch <- StreamEvent{Type: EventTextDelta, Content: resp.Content}
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
func (p *errProvider) ChatStream(_ context.Context, _ ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
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
func (p *ctxProvider) ChatStream(ctx context.Context, _ ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
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

// --- Context accessor tests ---

func TestTaskAccessors(t *testing.T) {
	task := AgentTask{
		Input: "test",
		Context: map[string]any{
			ContextThreadID: "thread-1",
			ContextUserID:   "user-42",
			ContextChatID:   "chat-99",
		},
	}

	if got := task.TaskThreadID(); got != "thread-1" {
		t.Errorf("TaskThreadID() = %q, want %q", got, "thread-1")
	}
	if got := task.TaskUserID(); got != "user-42" {
		t.Errorf("TaskUserID() = %q, want %q", got, "user-42")
	}
	if got := task.TaskChatID(); got != "chat-99" {
		t.Errorf("TaskChatID() = %q, want %q", got, "chat-99")
	}
}

func TestTaskAccessorsEmptyContext(t *testing.T) {
	task := AgentTask{Input: "test"}

	if got := task.TaskThreadID(); got != "" {
		t.Errorf("TaskThreadID() = %q, want empty", got)
	}
	if got := task.TaskUserID(); got != "" {
		t.Errorf("TaskUserID() = %q, want empty", got)
	}
	if got := task.TaskChatID(); got != "" {
		t.Errorf("TaskChatID() = %q, want empty", got)
	}
}

func TestTaskAccessorsWrongType(t *testing.T) {
	task := AgentTask{
		Input: "test",
		Context: map[string]any{
			ContextThreadID: 123, // int, not string
		},
	}

	if got := task.TaskThreadID(); got != "" {
		t.Errorf("TaskThreadID() = %q, want empty for non-string value", got)
	}
}

// --- InputHandler tests ---

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

// --- Dynamic config tests ---

func TestLLMAgentDynamicPrompt(t *testing.T) {
	var capturedPrompt string
	provider := &callbackProvider{
		name:     "test",
		response: ChatResponse{Content: "ok"},
		onChat: func(req ChatRequest) {
			for _, m := range req.Messages {
				if m.Role == "system" {
					capturedPrompt = m.Content
				}
			}
		},
	}

	agent := NewLLMAgent("dynamic", "Dynamic prompt", provider,
		WithPrompt("static fallback"),
		WithDynamicPrompt(func(_ context.Context, task AgentTask) string {
			return "dynamic: " + task.TaskUserID()
		}),
	)

	agent.Execute(context.Background(), AgentTask{
		Input:   "hi",
		Context: map[string]any{ContextUserID: "alice"},
	})

	if capturedPrompt != "dynamic: alice" {
		t.Errorf("prompt = %q, want %q", capturedPrompt, "dynamic: alice")
	}
}

func TestLLMAgentDynamicPromptFallback(t *testing.T) {
	var capturedPrompt string
	provider := &callbackProvider{
		name:     "test",
		response: ChatResponse{Content: "ok"},
		onChat: func(req ChatRequest) {
			for _, m := range req.Messages {
				if m.Role == "system" {
					capturedPrompt = m.Content
				}
			}
		},
	}

	agent := NewLLMAgent("static", "Static prompt", provider,
		WithPrompt("I am static"),
	)

	agent.Execute(context.Background(), AgentTask{Input: "hi"})

	if capturedPrompt != "I am static" {
		t.Errorf("prompt = %q, want %q", capturedPrompt, "I am static")
	}
}

func TestLLMAgentDynamicModel(t *testing.T) {
	providerA := &mockProvider{name: "model-a", responses: []ChatResponse{{Content: "from A"}}}
	providerB := &mockProvider{name: "model-b", responses: []ChatResponse{{Content: "from B"}}}

	agent := NewLLMAgent("dynamic", "Dynamic model", providerA,
		WithDynamicModel(func(_ context.Context, task AgentTask) Provider {
			if task.Context["tier"] == "pro" {
				return providerB
			}
			return providerA
		}),
	)

	result, _ := agent.Execute(context.Background(), AgentTask{
		Input:   "hi",
		Context: map[string]any{"tier": "pro"},
	})
	if result.Output != "from B" {
		t.Errorf("Output = %q, want %q", result.Output, "from B")
	}
}

func TestLLMAgentDynamicTools(t *testing.T) {
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "calc", Args: json.RawMessage(`{}`)}}},
			{Content: "used calc"},
		},
	}

	agent := NewLLMAgent("dynamic", "Dynamic tools", provider,
		WithTools(mockTool{}), // static: greet
		WithDynamicTools(func(_ context.Context, task AgentTask) []Tool {
			return []Tool{mockToolCalc{}}
		}),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "calculate"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "used calc" {
		t.Errorf("Output = %q, want %q", result.Output, "used calc")
	}
}

func TestLLMAgentTaskFromContextInTool(t *testing.T) {
	var gotUserID string
	ctxTool := &contextReadingTool{
		onExecute: func(ctx context.Context) {
			if task, ok := TaskFromContext(ctx); ok {
				gotUserID = task.TaskUserID()
			}
		},
	}

	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "ctx_reader", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
	}

	agent := NewLLMAgent("ctx", "Context test", provider, WithTools(ctxTool))
	agent.Execute(context.Background(), AgentTask{
		Input:   "test",
		Context: map[string]any{ContextUserID: "user-42"},
	})

	if gotUserID != "user-42" {
		t.Errorf("gotUserID = %q, want %q", gotUserID, "user-42")
	}
}

// --- Task context tests ---

func TestTaskFromContextPresent(t *testing.T) {
	task := AgentTask{
		Input:   "hello",
		Context: map[string]any{ContextUserID: "u1"},
	}
	ctx := WithTaskContext(context.Background(), task)

	got, ok := TaskFromContext(ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.Input != "hello" {
		t.Errorf("Input = %q, want %q", got.Input, "hello")
	}
	if got.TaskUserID() != "u1" {
		t.Errorf("TaskUserID() = %q, want %q", got.TaskUserID(), "u1")
	}
}

func TestTaskFromContextMissing(t *testing.T) {
	_, ok := TaskFromContext(context.Background())
	if ok {
		t.Error("expected ok=false for empty context")
	}
}

// --- Context compression tests ---

// bigResultTool returns a large string result (500 runes per call).
type bigResultTool struct{}

func (bigResultTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: "big", Description: "Returns big content"}}
}
func (bigResultTool) Execute(_ context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: strings.Repeat("x", 500)}, nil
}

func TestContextCompression(t *testing.T) {
	// Track message counts per ChatWithTools call.
	var messageCounts []int

	// Responses: 4 rounds of 2 tool calls each + 1 final text.
	// Without compression, final call sees 1 + 4*(1 asst + 2 tool) = 13 messages.
	responses := []ChatResponse{
		{ToolCalls: []ToolCall{
			{ID: "1a", Name: "big", Args: json.RawMessage(`{}`)},
			{ID: "1b", Name: "big", Args: json.RawMessage(`{}`)},
		}},
		{ToolCalls: []ToolCall{
			{ID: "2a", Name: "big", Args: json.RawMessage(`{}`)},
			{ID: "2b", Name: "big", Args: json.RawMessage(`{}`)},
		}},
		{ToolCalls: []ToolCall{
			{ID: "3a", Name: "big", Args: json.RawMessage(`{}`)},
			{ID: "3b", Name: "big", Args: json.RawMessage(`{}`)},
		}},
		{ToolCalls: []ToolCall{
			{ID: "4a", Name: "big", Args: json.RawMessage(`{}`)},
			{ID: "4b", Name: "big", Args: json.RawMessage(`{}`)},
		}},
		{Content: "done"},
	}

	trackingProvider := &sequentialCallbackProvider{
		name:      "tracker",
		responses: responses,
		onChat: func(req ChatRequest) {
			messageCounts = append(messageCounts, len(req.Messages))
		},
	}

	// Separate compression provider — always returns a short summary.
	compressResponses := make([]ChatResponse, 10)
	for i := range compressResponses {
		compressResponses[i] = ChatResponse{Content: "summary"}
	}
	compressProvider := &mockProvider{
		name:      "compress",
		responses: compressResponses,
	}

	// Threshold of 1500 runes. With 500 runes per tool result and 2 calls
	// per iteration (1000/iter), compression triggers after iteration 2
	// (~2002 runes) and starts compressing old iterations from iteration 3.
	agent := NewLLMAgent("compressor", "Tests compression", trackingProvider,
		WithTools(bigResultTool{}),
		WithCompressThreshold(1500),
		WithCompressModel(func(_ context.Context, _ AgentTask) Provider { return compressProvider }),
		WithMaxIter(10),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}

	t.Logf("message counts per call: %v", messageCounts)

	// Without compression, the final call (5th) sees 13 messages.
	// With compression, old tool results get collapsed into summaries,
	// so the count must be lower.
	lastCount := messageCounts[len(messageCounts)-1]
	if lastCount >= 13 {
		t.Errorf("expected fewer than 13 messages after compression, got %d", lastCount)
	}
}

// sequentialCallbackProvider returns different responses per call and runs a callback.
type sequentialCallbackProvider struct {
	name      string
	responses []ChatResponse
	idx       int
	onChat    func(ChatRequest)
}

func (s *sequentialCallbackProvider) Name() string { return s.name }
func (s *sequentialCallbackProvider) next(req ChatRequest) ChatResponse {
	if s.onChat != nil {
		s.onChat(req)
	}
	if s.idx >= len(s.responses) {
		return ChatResponse{Content: "exhausted"}
	}
	resp := s.responses[s.idx]
	s.idx++
	return resp
}
func (s *sequentialCallbackProvider) Chat(_ context.Context, req ChatRequest) (ChatResponse, error) {
	return s.next(req), nil
}
func (s *sequentialCallbackProvider) ChatWithTools(_ context.Context, req ChatRequest, _ []ToolDefinition) (ChatResponse, error) {
	return s.next(req), nil
}
func (s *sequentialCallbackProvider) ChatStream(_ context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	return s.next(req), nil
}

// --- Drain tests ---

func TestLLMAgentDrainCompletes(t *testing.T) {
	// Drain() should complete without blocking indefinitely, even after
	// multiple Execute calls. Without a Store, there are no background
	// persist goroutines, so Drain should be a fast no-op.
	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "hello"}},
	}

	agent := NewLLMAgent("drainer", "Tests drain", provider)

	_, err := agent.Execute(context.Background(), AgentTask{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		agent.Drain()
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("Drain() did not complete within 1 second")
	}
}

func TestNetworkDrainCompletes(t *testing.T) {
	router := &mockProvider{
		name:      "router",
		responses: []ChatResponse{{Content: "done"}},
	}

	network := NewNetwork("net", "Tests drain", router)

	_, err := network.Execute(context.Background(), AgentTask{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		network.Drain()
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("Drain() did not complete within 1 second")
	}
}

// --- Option builder tests ---

func TestBuildConfigDefaults(t *testing.T) {
	cfg := buildConfig(nil)

	if cfg.logger == nil {
		t.Error("logger should default to nopLogger, not nil")
	}
	if cfg.maxIter != 0 {
		t.Errorf("maxIter = %d, want 0 (buildConfig doesn't set defaults, constructors do)", cfg.maxIter)
	}
}

func TestWithMaxIterOption(t *testing.T) {
	cfg := buildConfig([]AgentOption{WithMaxIter(5)})
	if cfg.maxIter != 5 {
		t.Errorf("maxIter = %d, want 5", cfg.maxIter)
	}
}

func TestWithPromptOption(t *testing.T) {
	cfg := buildConfig([]AgentOption{WithPrompt("You are helpful.")})
	if cfg.prompt != "You are helpful." {
		t.Errorf("prompt = %q, want %q", cfg.prompt, "You are helpful.")
	}
}

func TestWithSuspendBudgetOption(t *testing.T) {
	cfg := buildConfig([]AgentOption{WithSuspendBudget(5, 1<<20)})
	if cfg.maxSuspendSnapshots != 5 {
		t.Errorf("maxSuspendSnapshots = %d, want 5", cfg.maxSuspendSnapshots)
	}
	if cfg.maxSuspendBytes != 1<<20 {
		t.Errorf("maxSuspendBytes = %d, want %d", cfg.maxSuspendBytes, 1<<20)
	}
}

func TestWithCompressThresholdOption(t *testing.T) {
	cfg := buildConfig([]AgentOption{WithCompressThreshold(100_000)})
	if cfg.compressThreshold != 100_000 {
		t.Errorf("compressThreshold = %d, want 100000", cfg.compressThreshold)
	}
}

func TestWithMaxAttachmentBytesOption(t *testing.T) {
	cfg := buildConfig([]AgentOption{WithMaxAttachmentBytes(10 << 20)})
	if cfg.maxAttachmentBytes != 10<<20 {
		t.Errorf("maxAttachmentBytes = %d, want %d", cfg.maxAttachmentBytes, 10<<20)
	}
}

// --- Generation parameters option tests ---

func TestWithTemperatureOption(t *testing.T) {
	cfg := buildConfig([]AgentOption{WithTemperature(0.7)})
	if cfg.generationParams == nil {
		t.Fatal("generationParams should not be nil")
	}
	if cfg.generationParams.Temperature == nil || *cfg.generationParams.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", cfg.generationParams.Temperature)
	}
}

func TestWithTemperatureZero(t *testing.T) {
	// 0.0 is a valid temperature — must not be conflated with "unset".
	cfg := buildConfig([]AgentOption{WithTemperature(0.0)})
	if cfg.generationParams == nil || cfg.generationParams.Temperature == nil {
		t.Fatal("Temperature should be set (pointer to 0.0)")
	}
	if *cfg.generationParams.Temperature != 0.0 {
		t.Errorf("Temperature = %v, want 0.0", *cfg.generationParams.Temperature)
	}
}

func TestWithTopPOption(t *testing.T) {
	cfg := buildConfig([]AgentOption{WithTopP(0.95)})
	if cfg.generationParams == nil || cfg.generationParams.TopP == nil {
		t.Fatal("TopP should be set")
	}
	if *cfg.generationParams.TopP != 0.95 {
		t.Errorf("TopP = %v, want 0.95", *cfg.generationParams.TopP)
	}
}

func TestWithTopKOption(t *testing.T) {
	cfg := buildConfig([]AgentOption{WithTopK(40)})
	if cfg.generationParams == nil || cfg.generationParams.TopK == nil {
		t.Fatal("TopK should be set")
	}
	if *cfg.generationParams.TopK != 40 {
		t.Errorf("TopK = %v, want 40", *cfg.generationParams.TopK)
	}
}

func TestWithMaxTokensOption(t *testing.T) {
	cfg := buildConfig([]AgentOption{WithMaxTokens(2048)})
	if cfg.generationParams == nil || cfg.generationParams.MaxTokens == nil {
		t.Fatal("MaxTokens should be set")
	}
	if *cfg.generationParams.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %v, want 2048", *cfg.generationParams.MaxTokens)
	}
}

func TestGenerationParamsCompose(t *testing.T) {
	// Multiple generation param options should compose into a single struct.
	cfg := buildConfig([]AgentOption{
		WithTemperature(0.5),
		WithTopP(0.9),
		WithTopK(50),
		WithMaxTokens(1024),
	})
	if cfg.generationParams == nil {
		t.Fatal("generationParams should not be nil")
	}
	if *cfg.generationParams.Temperature != 0.5 {
		t.Errorf("Temperature = %v, want 0.5", *cfg.generationParams.Temperature)
	}
	if *cfg.generationParams.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", *cfg.generationParams.TopP)
	}
	if *cfg.generationParams.TopK != 50 {
		t.Errorf("TopK = %v, want 50", *cfg.generationParams.TopK)
	}
	if *cfg.generationParams.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %v, want 1024", *cfg.generationParams.MaxTokens)
	}
}

func TestGenerationParamsNilWhenUnset(t *testing.T) {
	cfg := buildConfig(nil)
	if cfg.generationParams != nil {
		t.Error("generationParams should be nil when no generation options are set")
	}
}

func TestGenerationParamsInjectedIntoRequest(t *testing.T) {
	// Verify GenerationParams flows from agent options into ChatRequest.
	var capturedReq ChatRequest
	provider := &callbackProvider{
		name:     "test",
		response: ChatResponse{Content: "ok"},
		onChat: func(req ChatRequest) {
			capturedReq = req
		},
	}

	agent := NewLLMAgent("gp-test", "Tests gen params", provider,
		WithTemperature(0.3),
		WithTopP(0.85),
	)

	_, err := agent.Execute(context.Background(), AgentTask{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	if capturedReq.GenerationParams == nil {
		t.Fatal("GenerationParams should be set in ChatRequest")
	}
	if *capturedReq.GenerationParams.Temperature != 0.3 {
		t.Errorf("Temperature = %v, want 0.3", *capturedReq.GenerationParams.Temperature)
	}
	if *capturedReq.GenerationParams.TopP != 0.85 {
		t.Errorf("TopP = %v, want 0.85", *capturedReq.GenerationParams.TopP)
	}
}

func TestGenerationParamsNilInRequestWhenUnset(t *testing.T) {
	var capturedReq ChatRequest
	provider := &callbackProvider{
		name:     "test",
		response: ChatResponse{Content: "ok"},
		onChat: func(req ChatRequest) {
			capturedReq = req
		},
	}

	agent := NewLLMAgent("no-gp", "No gen params", provider)

	_, err := agent.Execute(context.Background(), AgentTask{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	if capturedReq.GenerationParams != nil {
		t.Error("GenerationParams should be nil when no options set")
	}
}

func TestGenerationParamsWithTools(t *testing.T) {
	// GenerationParams should be present when using ChatWithTools path.
	var capturedReq ChatRequest
	provider := &sequentialCallbackProvider{
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
		onChat: func(req ChatRequest) {
			capturedReq = req
		},
	}

	agent := NewLLMAgent("gp-tools", "Tests gen params with tools", provider,
		WithTools(mockTool{}),
		WithTemperature(0.1),
	)

	_, err := agent.Execute(context.Background(), AgentTask{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	if capturedReq.GenerationParams == nil {
		t.Fatal("GenerationParams should be set in ChatWithTools requests")
	}
	if *capturedReq.GenerationParams.Temperature != 0.1 {
		t.Errorf("Temperature = %v, want 0.1", *capturedReq.GenerationParams.Temperature)
	}
}

// --- Thinking visibility tests ---

func TestThinkingInAgentResult(t *testing.T) {
	// Provider returns thinking content — should appear in AgentResult.Thinking.
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{Content: "The answer is 42", Thinking: "Let me reason about this..."},
		},
	}

	agent := NewLLMAgent("thinker", "Thinks", provider)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "What is the answer?"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "The answer is 42" {
		t.Errorf("Output = %q, want %q", result.Output, "The answer is 42")
	}
	if result.Thinking != "Let me reason about this..." {
		t.Errorf("Thinking = %q, want %q", result.Thinking, "Let me reason about this...")
	}
}

func TestThinkingEmptyWhenNotProvided(t *testing.T) {
	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "hello"}},
	}

	agent := NewLLMAgent("no-think", "No thinking", provider)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Thinking != "" {
		t.Errorf("Thinking = %q, want empty", result.Thinking)
	}
}

func TestThinkingFromLastResponseInToolLoop(t *testing.T) {
	// When multiple LLM calls happen (tool loop), Thinking should capture
	// the most recent thinking content.
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{
				ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}},
				Thinking:  "First, I need to call a tool...",
			},
			{
				Content:  "Done!",
				Thinking: "Now I can give the final answer.",
			},
		},
	}

	agent := NewLLMAgent("multi-think", "Multiple thinking", provider,
		WithTools(mockTool{}),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Thinking != "Now I can give the final answer." {
		t.Errorf("Thinking = %q, want %q", result.Thinking, "Now I can give the final answer.")
	}
}

func TestThinkingFromForcedSynthesis(t *testing.T) {
	// Thinking from the forced synthesis (max iterations) should be captured.
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{ToolCalls: []ToolCall{{ID: "2", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{ToolCalls: []ToolCall{{ID: "3", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{Content: "synthesized", Thinking: "Forced to summarize."},
		},
	}

	agent := NewLLMAgent("synth-think", "Synthesis thinking", provider,
		WithTools(mockTool{}),
		WithMaxIter(3),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "loop"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Thinking != "Forced to summarize." {
		t.Errorf("Thinking = %q, want %q", result.Thinking, "Forced to summarize.")
	}
}

func TestNetworkThinkingPropagated(t *testing.T) {
	// Network should propagate thinking from the router's final response.
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{Content: "result", Thinking: "Network reasoning..."},
		},
	}

	network := NewNetwork("net", "Network thinking", router)

	result, err := network.Execute(context.Background(), AgentTask{Input: "think"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Thinking != "Network reasoning..." {
		t.Errorf("Thinking = %q, want %q", result.Thinking, "Network reasoning...")
	}
}
