package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// --- Streaming tests ---

func TestLLMAgentExecuteStreamNoTools(t *testing.T) {
	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "streamed hello"}},
	}

	agent := NewLLMAgent("streamer", "Streams output", provider)

	ch := make(chan StreamEvent, 10)
	result, err := agent.ExecuteStream(context.Background(), AgentTask{Input: "hi"}, ch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "streamed hello" {
		t.Errorf("Output = %q, want %q", result.Output, "streamed hello")
	}

	// Verify events were sent to channel
	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events (input-received, processing-start, text-delta), got %d", len(events))
	}
	// First event should be input-received.
	if events[0].Type != EventInputReceived {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, EventInputReceived)
	}
	if events[0].Name != "streamer" {
		t.Errorf("events[0].Name = %q, want %q", events[0].Name, "streamer")
	}
	if events[0].Content != "hi" {
		t.Errorf("events[0].Content = %q, want %q", events[0].Content, "hi")
	}
	// Second event should be processing-start.
	if events[1].Type != EventProcessingStart {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, EventProcessingStart)
	}
}

func TestLLMAgentExecuteStreamWithTools(t *testing.T) {
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			// First: tool call (blocking)
			{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
			// Second: final text response (streamed as single chunk since from ChatWithTools)
			{Content: "after tool call"},
		},
	}

	agent := NewLLMAgent("streamer", "Streams with tools", provider,
		WithTools(mockTool{}),
	)

	ch := make(chan StreamEvent, 10)
	result, err := agent.ExecuteStream(context.Background(), AgentTask{Input: "greet"}, ch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "after tool call" {
		t.Errorf("Output = %q, want %q", result.Output, "after tool call")
	}

	// Channel should be closed and contain lifecycle + tool + text events
	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(events))
	}
	// First two events should be lifecycle events.
	if events[0].Type != EventInputReceived {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, EventInputReceived)
	}
	if events[1].Type != EventProcessingStart {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, EventProcessingStart)
	}
	// Should have tool-call-start, tool-call-result, and text-delta events
	var hasToolStart, hasToolResult, hasTextDelta bool
	for _, ev := range events {
		switch ev.Type {
		case EventToolCallStart:
			hasToolStart = true
		case EventToolCallResult:
			hasToolResult = true
		case EventTextDelta:
			hasTextDelta = true
		}
	}
	if !hasToolStart {
		t.Error("expected tool-call-start event")
	}
	if !hasToolResult {
		t.Error("expected tool-call-result event")
	}
	if !hasTextDelta {
		t.Error("expected text-delta event")
	}
}

func TestLLMAgentStreamingInterfaceCompliance(t *testing.T) {
	agent := NewLLMAgent("test", "test", &mockProvider{name: "test"})
	var _ StreamingAgent = agent
}

func TestNetworkExecuteStream(t *testing.T) {
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			// Router calls agent_echo
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_echo",
				Args: json.RawMessage(`{"task":"say hi"}`),
			}}},
			// Final response (streamed as single chunk)
			{Content: "network streamed response"},
		},
	}

	echoAgent := &stubAgent{
		name: "echo",
		desc: "Echoes",
		fn: func(task AgentTask) (AgentResult, error) {
			return AgentResult{Output: "echoed: " + task.Input}, nil
		},
	}

	network := NewNetwork("net", "Streams", router, WithAgents(echoAgent))

	ch := make(chan StreamEvent, 10)
	result, err := network.ExecuteStream(context.Background(), AgentTask{Input: "test"}, ch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "network streamed response" {
		t.Errorf("Output = %q, want %q", result.Output, "network streamed response")
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(events))
	}
	// First two events should be lifecycle events.
	if events[0].Type != EventInputReceived {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, EventInputReceived)
	}
	if events[0].Name != "net" {
		t.Errorf("events[0].Name = %q, want %q", events[0].Name, "net")
	}
	if events[1].Type != EventProcessingStart {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, EventProcessingStart)
	}
	// Should have agent-start and agent-finish events
	var hasAgentStart, hasAgentFinish bool
	for _, ev := range events {
		switch ev.Type {
		case EventAgentStart:
			hasAgentStart = true
		case EventAgentFinish:
			hasAgentFinish = true
		}
	}
	if !hasAgentStart {
		t.Error("expected agent-start event")
	}
	if !hasAgentFinish {
		t.Error("expected agent-finish event")
	}
}

func TestNetworkExecuteStreamDelegatesToStreamingSubagent(t *testing.T) {
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			// Router calls agent_streamer
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_streamer",
				Args: json.RawMessage(`{"task":"say hi"}`),
			}}},
			// Final response after delegation
			{Content: "done"},
		},
	}

	// Subagent that implements StreamingAgent — emits token-by-token events.
	streamer := &stubStreamingAgent{
		name: "streamer",
		desc: "Streams tokens",
		events: []StreamEvent{
			{Type: EventTextDelta, Content: "hel"},
			{Type: EventTextDelta, Content: "lo "},
			{Type: EventTextDelta, Content: "world"},
		},
		result: AgentResult{Output: "hello world"},
	}

	network := NewNetwork("net", "Streaming delegation", router, WithAgents(streamer))

	ch := make(chan StreamEvent, 32)
	result, err := network.ExecuteStream(context.Background(), AgentTask{Input: "test"}, ch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	// First two events should be lifecycle events.
	if events[0].Type != EventInputReceived {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, EventInputReceived)
	}
	if events[1].Type != EventProcessingStart {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, EventProcessingStart)
	}

	// Expect: input-received, processing-start, then
	// tool-call-start, agent-start, 3x text-delta (forwarded from subagent),
	// agent-finish, tool-call-result. The router's final text-delta is
	// suppressed because the sub-agent already streamed via ExecuteStream.
	var agentStart, agentFinish int
	var textDeltas []string
	for _, ev := range events {
		switch ev.Type {
		case EventAgentStart:
			agentStart++
		case EventAgentFinish:
			agentFinish++
		case EventTextDelta:
			textDeltas = append(textDeltas, ev.Content)
		}
	}
	if agentStart != 1 {
		t.Errorf("agent-start events = %d, want 1", agentStart)
	}
	if agentFinish != 1 {
		t.Errorf("agent-finish events = %d, want 1", agentFinish)
	}
	// Only the 3 forwarded text-deltas from the subagent; the router's
	// final response is suppressed to avoid duplication.
	if len(textDeltas) != 3 {
		t.Errorf("text-delta events = %d, want 3 (got: %v)", len(textDeltas), textDeltas)
	}
	if len(textDeltas) >= 3 {
		if textDeltas[0] != "hel" || textDeltas[1] != "lo " || textDeltas[2] != "world" {
			t.Errorf("forwarded deltas = %v, want [hel, lo , world]", textDeltas[:3])
		}
	}
}

func TestNetworkStreamNoDuplicateWhenRouterEchoes(t *testing.T) {
	subagentOutput := "hello world"
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			// Router delegates to agent_streamer
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_streamer",
				Args: json.RawMessage(`{"task":"say hi"}`),
			}}},
			// Router echoes the sub-agent output verbatim — common for
			// pure-routing LLMs. This must NOT produce a second text-delta.
			{Content: subagentOutput},
		},
	}

	streamer := &stubStreamingAgent{
		name: "streamer",
		desc: "Streams tokens",
		events: []StreamEvent{
			{Type: EventTextDelta, Content: "hello "},
			{Type: EventTextDelta, Content: "world"},
		},
		result: AgentResult{Output: subagentOutput},
	}

	network := NewNetwork("net", "Streaming dedup", router, WithAgents(streamer))

	ch := make(chan StreamEvent, 32)
	result, err := network.ExecuteStream(context.Background(), AgentTask{Input: "test"}, ch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != subagentOutput {
		t.Errorf("Output = %q, want %q", result.Output, subagentOutput)
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	// Count text-delta events — should only have the 2 from the sub-agent,
	// NOT a 3rd duplicate from the router's echoed final response.
	var textDeltas []string
	for _, ev := range events {
		if ev.Type == EventTextDelta {
			textDeltas = append(textDeltas, ev.Content)
		}
	}
	if len(textDeltas) != 2 {
		t.Errorf("text-delta events = %d, want 2 (got: %v)", len(textDeltas), textDeltas)
	}
	if len(textDeltas) >= 2 {
		if textDeltas[0] != "hello " || textDeltas[1] != "world" {
			t.Errorf("deltas = %v, want [hello , world]", textDeltas)
		}
	}
}

func TestNetworkStreamNoDuplicateWhenRouterEmpty(t *testing.T) {
	subagentOutput := "hello world"
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			// Router delegates to agent_streamer
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_streamer",
				Args: json.RawMessage(`{"task":"say hi"}`),
			}}},
			// Router returns empty — falls back to lastAgentOutput.
			// Must NOT produce a second text-delta.
			{Content: ""},
		},
	}

	streamer := &stubStreamingAgent{
		name: "streamer",
		desc: "Streams tokens",
		events: []StreamEvent{
			{Type: EventTextDelta, Content: "hello "},
			{Type: EventTextDelta, Content: "world"},
		},
		result: AgentResult{Output: subagentOutput},
	}

	network := NewNetwork("net", "Streaming dedup empty", router, WithAgents(streamer))

	ch := make(chan StreamEvent, 32)
	result, err := network.ExecuteStream(context.Background(), AgentTask{Input: "test"}, ch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != subagentOutput {
		t.Errorf("Output = %q, want %q", result.Output, subagentOutput)
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	// Count text-delta events — should only have the 2 from the sub-agent.
	var textDeltas []string
	for _, ev := range events {
		if ev.Type == EventTextDelta {
			textDeltas = append(textDeltas, ev.Content)
		}
	}
	if len(textDeltas) != 2 {
		t.Errorf("text-delta events = %d, want 2 (got: %v)", len(textDeltas), textDeltas)
	}
}

func TestNetworkStreamNoDuplicateWhenRouterParaphrases(t *testing.T) {
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			// Router delegates to agent_streamer
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_streamer",
				Args: json.RawMessage(`{"task":"say hi"}`),
			}}},
			// Router paraphrases the sub-agent output (different text,
			// same meaning). Must NOT produce a second text-delta.
			{Content: "A greeting: hello world!"},
		},
	}

	streamer := &stubStreamingAgent{
		name: "streamer",
		desc: "Streams tokens",
		events: []StreamEvent{
			{Type: EventTextDelta, Content: "hello "},
			{Type: EventTextDelta, Content: "world"},
		},
		result: AgentResult{Output: "hello world"},
	}

	network := NewNetwork("net", "Streaming dedup paraphrase", router, WithAgents(streamer))

	ch := make(chan StreamEvent, 32)
	result, err := network.ExecuteStream(context.Background(), AgentTask{Input: "test"}, ch)
	if err != nil {
		t.Fatal(err)
	}
	// AgentResult.Output carries the router's response for non-streaming consumers.
	if result.Output != "A greeting: hello world!" {
		t.Errorf("Output = %q, want %q", result.Output, "A greeting: hello world!")
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	// Only the 2 text-deltas from the sub-agent; the router's paraphrase
	// is suppressed in the stream (available via AgentResult.Output).
	var textDeltas []string
	for _, ev := range events {
		if ev.Type == EventTextDelta {
			textDeltas = append(textDeltas, ev.Content)
		}
	}
	if len(textDeltas) != 2 {
		t.Errorf("text-delta events = %d, want 2 (got: %v)", len(textDeltas), textDeltas)
	}
}

func TestNetworkStreamingInterfaceCompliance(t *testing.T) {
	network := NewNetwork("test", "test", &mockProvider{name: "test"})
	var _ StreamingAgent = network
}

func TestLLMAgentExecuteStreamProviderError(t *testing.T) {
	agent := NewLLMAgent("broken", "Broken", &errProvider{
		name: "fail",
		err:  errors.New("stream error"),
	})

	ch := make(chan StreamEvent, 10)
	_, err := agent.ExecuteStream(context.Background(), AgentTask{Input: "hi"}, ch)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "stream error" {
		t.Errorf("error = %q, want %q", err.Error(), "stream error")
	}

	// Drain any lifecycle events and verify channel is closed.
	for range ch {
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

// --- Parallel tool execution tests ---

// barrierTool is a Tool where each Execute blocks until all concurrent calls
// have started. If tools run sequentially, this deadlocks (caught by timeout).
type barrierTool struct {
	name    string
	barrier chan struct{}
	started chan struct{}
}

func (b *barrierTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: b.name, Description: "barrier tool"}}
}

func (b *barrierTool) Execute(_ context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
	b.started <- struct{}{} // signal: I have started
	<-b.barrier             // wait for release
	return ToolResult{Content: "done from " + b.name}, nil
}

func TestLLMAgentParallelToolExecution(t *testing.T) {
	const numTools = 3
	barrier := make(chan struct{})
	started := make(chan struct{}, numTools)

	// Create tools that share a barrier
	var tools []Tool
	for i := 0; i < numTools; i++ {
		tools = append(tools, &barrierTool{
			name:    fmt.Sprintf("tool_%d", i),
			barrier: barrier,
			started: started,
		})
	}

	// Provider returns all tool calls at once, then a final response
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{
				{ID: "1", Name: "tool_0", Args: json.RawMessage(`{}`)},
				{ID: "2", Name: "tool_1", Args: json.RawMessage(`{}`)},
				{ID: "3", Name: "tool_2", Args: json.RawMessage(`{}`)},
			}},
			{Content: "all tools completed"},
		},
	}

	agent := NewLLMAgent("parallel", "Tests parallel", provider, WithTools(tools...))

	done := make(chan struct{})
	var result AgentResult
	var execErr error
	go func() {
		result, execErr = agent.Execute(context.Background(), AgentTask{Input: "go"})
		close(done)
	}()

	// All 3 tools must start before any can finish.
	// If sequential, tool_1 would block waiting for tool_0 to finish,
	// but tool_0 is waiting for all 3 to start — deadlock.
	for i := 0; i < numTools; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("tool did not start — tools likely running sequentially")
		}
	}

	// Release all tools
	close(barrier)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not finish in time")
	}

	if execErr != nil {
		t.Fatal(execErr)
	}
	if result.Output != "all tools completed" {
		t.Errorf("Output = %q, want %q", result.Output, "all tools completed")
	}
}

func TestNetworkParallelToolExecution(t *testing.T) {
	const numTools = 3
	barrier := make(chan struct{})
	started := make(chan struct{}, numTools)

	var tools []Tool
	for i := 0; i < numTools; i++ {
		tools = append(tools, &barrierTool{
			name:    fmt.Sprintf("tool_%d", i),
			barrier: barrier,
			started: started,
		})
	}

	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{
				{ID: "1", Name: "tool_0", Args: json.RawMessage(`{}`)},
				{ID: "2", Name: "tool_1", Args: json.RawMessage(`{}`)},
				{ID: "3", Name: "tool_2", Args: json.RawMessage(`{}`)},
			}},
			{Content: "network parallel done"},
		},
	}

	network := NewNetwork("parallel", "Tests parallel", router, WithTools(tools...))

	done := make(chan struct{})
	var result AgentResult
	var execErr error
	go func() {
		result, execErr = network.Execute(context.Background(), AgentTask{Input: "go"})
		close(done)
	}()

	for i := 0; i < numTools; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("tool did not start — tools likely running sequentially")
		}
	}

	close(barrier)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("network did not finish in time")
	}

	if execErr != nil {
		t.Fatal(execErr)
	}
	if result.Output != "network parallel done" {
		t.Errorf("Output = %q, want %q", result.Output, "network parallel done")
	}
}

func TestNetworkParallelAgentExecution(t *testing.T) {
	// Verify subagent dispatches also run in parallel
	barrier := make(chan struct{})
	started := make(chan struct{}, 2)

	makeAgent := func(name string) *stubAgent {
		return &stubAgent{
			name: name,
			desc: "Barrier agent",
			fn: func(task AgentTask) (AgentResult, error) {
				started <- struct{}{}
				<-barrier
				return AgentResult{Output: name + " done"}, nil
			},
		}
	}

	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{
				{ID: "1", Name: "agent_alpha", Args: json.RawMessage(`{"task":"work"}`)},
				{ID: "2", Name: "agent_beta", Args: json.RawMessage(`{"task":"work"}`)},
			}},
			{Content: "both agents done"},
		},
	}

	network := NewNetwork("parallel", "Tests parallel agents", router,
		WithAgents(makeAgent("alpha"), makeAgent("beta")),
	)

	done := make(chan struct{})
	var result AgentResult
	var execErr error
	go func() {
		result, execErr = network.Execute(context.Background(), AgentTask{Input: "go"})
		close(done)
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("agent did not start — agents likely running sequentially")
		}
	}

	close(barrier)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("network did not finish in time")
	}

	if execErr != nil {
		t.Fatal(execErr)
	}
	if result.Output != "both agents done" {
		t.Errorf("Output = %q, want %q", result.Output, "both agents done")
	}
}

// --- Plan execution tests ---

func TestLLMAgentPlanExecution(t *testing.T) {
	// Provider calls execute_plan with 3 steps, then synthesizes final response
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "execute_plan",
				Args: json.RawMessage(`{"steps":[
					{"tool":"greet","args":{}},
					{"tool":"greet","args":{}},
					{"tool":"greet","args":{}}
				]}`),
			}}},
			{Content: "all 3 greetings done"},
		},
	}

	agent := NewLLMAgent("planner", "Plans tool calls", provider,
		WithTools(mockTool{}),
		WithPlanExecution(),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "greet 3 times"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "all 3 greetings done" {
		t.Errorf("Output = %q, want %q", result.Output, "all 3 greetings done")
	}
}

func TestLLMAgentPlanExecutionResultFormat(t *testing.T) {
	// Verify the structured per-step result format
	var capturedResult string
	captureProvider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "execute_plan",
				Args: json.RawMessage(`{"steps":[
					{"tool":"greet","args":{}},
					{"tool":"calc","args":{}}
				]}`),
			}}},
			{Content: "done"},
		},
	}

	agent := NewLLMAgent("planner", "Plans", captureProvider,
		WithTools(mockTool{}, mockToolCalc{}),
		WithPlanExecution(),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	_ = result

	// The plan result was fed back as a tool result message.
	// We can verify the format by calling executePlan directly.
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		return DispatchResult{Content: "result_" + tc.Name, Usage: Usage{InputTokens: 10}}
	}
	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[
		{"tool":"greet","args":{}},
		{"tool":"calc","args":{}}
	]}`), dispatch)
	capturedResult = dr.Content

	var steps []planStepResult
	if err := json.Unmarshal([]byte(capturedResult), &steps); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(steps))
	}
	if steps[0].Tool != "greet" || steps[0].Status != "ok" || steps[0].Result != "result_greet" {
		t.Errorf("step 0 = %+v, want tool=greet status=ok result=result_greet", steps[0])
	}
	if steps[1].Tool != "calc" || steps[1].Status != "ok" || steps[1].Result != "result_calc" {
		t.Errorf("step 1 = %+v, want tool=calc status=ok result=result_calc", steps[1])
	}
	if dr.Usage.InputTokens != 20 {
		t.Errorf("usage.InputTokens = %d, want 20", dr.Usage.InputTokens)
	}
}

func TestLLMAgentPlanExecutionErrorStep(t *testing.T) {
	// Verify that a failed step reports error without aborting other steps
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		if tc.Name == "fail" {
			return DispatchResult{Content: "error: tool broken"}
		}
		return DispatchResult{Content: "ok_" + tc.Name}
	}

	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[
		{"tool":"greet","args":{}},
		{"tool":"fail","args":{}},
		{"tool":"calc","args":{}}
	]}`), dispatch)

	var steps []planStepResult
	if err := json.Unmarshal([]byte(dr.Content), &steps); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
	if steps[0].Status != "ok" {
		t.Errorf("step 0 status = %q, want ok", steps[0].Status)
	}
	if steps[1].Status != "error" || steps[1].Error != "tool broken" {
		t.Errorf("step 1 = %+v, want status=error error='tool broken'", steps[1])
	}
	if steps[2].Status != "ok" {
		t.Errorf("step 2 status = %q, want ok", steps[2].Status)
	}
}

func TestLLMAgentPlanExecutionRecursionPrevented(t *testing.T) {
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		return DispatchResult{Content: "should not reach"}
	}

	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[
		{"tool":"execute_plan","args":{"steps":[]}}
	]}`), dispatch)

	if dr.Content != "error: execute_plan steps cannot call execute_plan" {
		t.Errorf("expected recursion error, got %q", dr.Content)
	}
}

func TestLLMAgentPlanExecutionEmptySteps(t *testing.T) {
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		return DispatchResult{Content: "should not reach"}
	}

	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[]}`), dispatch)
	if dr.Content != "error: execute_plan requires at least one step" {
		t.Errorf("expected empty steps error, got %q", dr.Content)
	}
}

func TestLLMAgentPlanExecutionInvalidArgs(t *testing.T) {
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		return DispatchResult{Content: "should not reach"}
	}

	dr := executePlan(context.Background(), json.RawMessage(`not json`), dispatch)
	if len(dr.Content) < 7 || dr.Content[:7] != "error: " {
		t.Errorf("expected error for invalid args, got %q", dr.Content)
	}
}

func TestLLMAgentPlanExecutionNotEnabledIgnored(t *testing.T) {
	// When WithPlanExecution is NOT set, execute_plan is treated as unknown tool
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "execute_plan",
				Args: json.RawMessage(`{"steps":[{"tool":"greet","args":{}}]}`),
			}}},
			{Content: "recovered"},
		},
	}

	agent := NewLLMAgent("nope", "No plan", provider,
		WithTools(mockTool{}),
		// Note: WithPlanExecution() NOT set
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "recovered" {
		t.Errorf("Output = %q, want %q", result.Output, "recovered")
	}
}

func TestNetworkPlanExecution(t *testing.T) {
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "execute_plan",
				Args: json.RawMessage(`{"steps":[
					{"tool":"greet","args":{}},
					{"tool":"greet","args":{}}
				]}`),
			}}},
			{Content: "network plan done"},
		},
	}

	network := NewNetwork("net", "Plans", router,
		WithTools(mockTool{}),
		WithPlanExecution(),
	)

	result, err := network.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "network plan done" {
		t.Errorf("Output = %q, want %q", result.Output, "network plan done")
	}
}

// --- InputHandler tests (from input_test.go) ---

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

// --- Suspend tests (from suspend_test.go) ---

func TestSuspendReturnsErrSuspend(t *testing.T) {
	payload := json.RawMessage(`{"action": "approve"}`)
	err := Suspend(payload)
	if err == nil {
		t.Fatal("Suspend should return non-nil error")
	}

	var s *errSuspend
	if !errors.As(err, &s) {
		t.Fatalf("expected errSuspend, got %T", err)
	}
	if string(s.payload) != `{"action": "approve"}` {
		t.Errorf("payload = %s, want %s", s.payload, `{"action": "approve"}`)
	}
}

func TestErrSuspendedError(t *testing.T) {
	e := &ErrSuspended{Step: "approval"}
	if e.Error() != `suspended at step "approval"` {
		t.Errorf("Error() = %q", e.Error())
	}
}

func TestErrSuspendedResume(t *testing.T) {
	called := false
	e := &ErrSuspended{
		Step:    "test",
		Payload: json.RawMessage(`{}`),
		resume: func(ctx context.Context, data json.RawMessage) (AgentResult, error) {
			called = true
			return AgentResult{Output: string(data)}, nil
		},
	}

	result, err := e.Resume(context.Background(), json.RawMessage(`{"ok":true}`))
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if !called {
		t.Error("resume func was not called")
	}
	if result.Output != `{"ok":true}` {
		t.Errorf("Output = %q", result.Output)
	}
}

func TestResumeDataNotPresent(t *testing.T) {
	wCtx := newWorkflowContext(AgentTask{Input: "test"})
	data, ok := ResumeData(wCtx)
	if ok {
		t.Error("ResumeData should return false when no resume data")
	}
	if data != nil {
		t.Error("ResumeData should return nil when no resume data")
	}
}

func TestResumeDataPresent(t *testing.T) {
	wCtx := newWorkflowContext(AgentTask{Input: "test"})
	wCtx.Set("_resume_data", json.RawMessage(`{"approved": true}`))

	data, ok := ResumeData(wCtx)
	if !ok {
		t.Error("ResumeData should return true when resume data is set")
	}
	if string(data) != `{"approved": true}` {
		t.Errorf("ResumeData = %s", data)
	}
}

func TestStepSuspendedStatus(t *testing.T) {
	if StepSuspended != "suspended" {
		t.Errorf("StepSuspended = %q, want %q", StepSuspended, "suspended")
	}
}

func TestWorkflowSuspendPayload(t *testing.T) {
	wf, _ := NewWorkflow("test", "test",
		Step("gate", func(_ context.Context, _ *WorkflowContext) error {
			return Suspend(json.RawMessage(`{"key": "value", "num": 42}`))
		}),
	)

	_, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(suspended.Payload, &payload); err != nil {
		t.Fatalf("failed to parse payload: %v", err)
	}
	if payload["key"] != "value" {
		t.Errorf("payload[key] = %v", payload["key"])
	}
	if payload["num"] != float64(42) {
		t.Errorf("payload[num] = %v", payload["num"])
	}
}

func TestWorkflowSuspendPreservesCompletedSteps(t *testing.T) {
	prepareCount := 0
	wf, _ := NewWorkflow("test", "test",
		Step("prepare", func(_ context.Context, wCtx *WorkflowContext) error {
			prepareCount++
			wCtx.Set("prepare.output", "done")
			return nil
		}),
		Step("gate", func(_ context.Context, wCtx *WorkflowContext) error {
			if _, ok := ResumeData(wCtx); ok {
				return nil
			}
			return Suspend(json.RawMessage(`{}`))
		}, After("prepare")),
	)

	_, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	var suspended *ErrSuspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", err)
	}

	_, err = suspended.Resume(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Resume error: %v", err)
	}

	// "prepare" should only have run once (not re-executed on resume).
	if prepareCount != 1 {
		t.Errorf("prepare ran %d times, want 1", prepareCount)
	}
}

func TestWorkflowMultiSuspend(t *testing.T) {
	// Each gate uses a call counter to distinguish first execution (suspend)
	// from resume, because ResumeData is workflow-global and gate2 would
	// otherwise see gate1's resume data.
	gate1Calls := 0
	gate2Calls := 0

	wf, _ := NewWorkflow("test", "test",
		Step("gate1", func(_ context.Context, wCtx *WorkflowContext) error {
			gate1Calls++
			if gate1Calls > 1 {
				wCtx.Set("gate1.output", "passed")
				return nil
			}
			return Suspend(json.RawMessage(`{"gate": 1}`))
		}),
		Step("gate2", func(_ context.Context, wCtx *WorkflowContext) error {
			gate2Calls++
			if gate2Calls > 1 {
				wCtx.Set("gate2.output", "passed")
				return nil
			}
			return Suspend(json.RawMessage(`{"gate": 2}`))
		}, After("gate1")),
	)

	// First suspend at gate1.
	_, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	var s1 *ErrSuspended
	if !errors.As(err, &s1) {
		t.Fatalf("expected first ErrSuspended, got %v", err)
	}
	if s1.Step != "gate1" {
		t.Errorf("first suspend step = %q, want gate1", s1.Step)
	}

	// Resume gate1 → gate2 suspends.
	_, err = s1.Resume(context.Background(), json.RawMessage(`{}`))
	var s2 *ErrSuspended
	if !errors.As(err, &s2) {
		t.Fatalf("expected second ErrSuspended, got %v", err)
	}
	if s2.Step != "gate2" {
		t.Errorf("second suspend step = %q, want gate2", s2.Step)
	}

	// Resume gate2 → workflow completes.
	result, err := s2.Resume(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("final Resume error: %v", err)
	}
	if result.Output != "passed" {
		t.Errorf("Output = %q, want %q", result.Output, "passed")
	}
}

func TestWorkflowSuspendResumeRejection(t *testing.T) {
	wf, _ := NewWorkflow("test", "test",
		Step("gate", func(_ context.Context, wCtx *WorkflowContext) error {
			if data, ok := ResumeData(wCtx); ok {
				var d struct {
					Approved bool `json:"approved"`
				}
				json.Unmarshal(data, &d)
				if !d.Approved {
					return fmt.Errorf("rejected")
				}
				return nil
			}
			return Suspend(json.RawMessage(`{}`))
		}),
	)

	_, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	var suspended *ErrSuspended
	errors.As(err, &suspended)

	_, err = suspended.Resume(context.Background(), json.RawMessage(`{"approved": false}`))
	if err == nil {
		t.Fatal("expected error on rejection")
	}

	var wfErr *WorkflowError
	if !errors.As(err, &wfErr) {
		t.Fatalf("expected WorkflowError, got %T: %v", err, err)
	}
}

func TestWorkflowSuspendNoCallbacks(t *testing.T) {
	onFinishCalled := false
	onErrorCalled := false

	wf, _ := NewWorkflow("test", "test",
		Step("gate", func(_ context.Context, _ *WorkflowContext) error {
			return Suspend(json.RawMessage(`{}`))
		}),
		WithOnFinish(func(_ WorkflowResult) { onFinishCalled = true }),
		WithOnError(func(_ string, _ error) { onErrorCalled = true }),
	)

	wf.Execute(context.Background(), AgentTask{Input: "go"})

	if onFinishCalled {
		t.Error("onFinish should not be called on suspend")
	}
	if onErrorCalled {
		t.Error("onError should not be called on suspend")
	}
}

func TestWorkflowSuspendContextCancellation(t *testing.T) {
	gateCalled := false

	wf, _ := NewWorkflow("test", "test",
		Step("gate", func(ctx context.Context, _ *WorkflowContext) error {
			// On resume, check the context first — a cancelled context
			// should prevent meaningful work.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			gateCalled = true
			return Suspend(json.RawMessage(`{}`))
		}),
	)

	_, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	var suspended *ErrSuspended
	errors.As(err, &suspended)

	// Reset to track the resume call.
	gateCalled = false

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// When the context is already cancelled, executeStep skips the step
	// before calling the step function (ctx.Err() check in executeStep).
	// The step is marked StepSkipped, which is not a failure, so err is nil.
	result, err := suspended.Resume(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The gate step function should NOT have been called.
	if gateCalled {
		t.Error("gate step should not execute with cancelled context")
	}

	// No output since the step was skipped.
	if result.Output != "" {
		t.Errorf("Output = %q, want empty", result.Output)
	}
}

func TestWorkflowSuspendAndResume(t *testing.T) {
	callCount := 0
	wf, err := NewWorkflow("test", "test workflow",
		Step("prepare", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("data", "prepared")
			return nil
		}),
		Step("gate", func(_ context.Context, wCtx *WorkflowContext) error {
			callCount++
			if data, ok := ResumeData(wCtx); ok {
				wCtx.Set("gate.output", "approved:"+string(data))
				return nil
			}
			return Suspend(json.RawMessage(`{"needs": "approval"}`))
		}, After("prepare")),
		Step("finish", func(_ context.Context, wCtx *WorkflowContext) error {
			v, _ := wCtx.Get("gate.output")
			wCtx.Set("finish.output", "done:"+v.(string))
			return nil
		}, After("gate")),
	)
	if err != nil {
		t.Fatalf("NewWorkflow: %v", err)
	}

	// First execution — should suspend at "gate".
	result, execErr := wf.Execute(context.Background(), AgentTask{Input: "go"})

	var suspended *ErrSuspended
	if !errors.As(execErr, &suspended) {
		t.Fatalf("expected ErrSuspended, got %v", execErr)
	}
	if suspended.Step != "gate" {
		t.Errorf("Step = %q, want %q", suspended.Step, "gate")
	}
	if string(suspended.Payload) != `{"needs": "approval"}` {
		t.Errorf("Payload = %s", suspended.Payload)
	}

	// Resume with approval data.
	result, execErr = suspended.Resume(context.Background(), json.RawMessage(`"yes"`))
	if execErr != nil {
		t.Fatalf("Resume returned error: %v", execErr)
	}

	// "finish" step should have run.
	if result.Output != `done:approved:"yes"` {
		t.Errorf("Output = %q", result.Output)
	}

	// "gate" should have been called twice: once for suspend, once for resume.
	if callCount != 2 {
		t.Errorf("gate called %d times, want 2", callCount)
	}
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

// --- SSE streaming tests (from stream_test.go) ---

// stubStreamingAgent implements StreamingAgent for testing.
type stubStreamingAgent struct {
	name   string
	desc   string
	events []StreamEvent
	result AgentResult
	err    error
}

func (s *stubStreamingAgent) Name() string        { return s.name }
func (s *stubStreamingAgent) Description() string { return s.desc }
func (s *stubStreamingAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return s.result, s.err
}
func (s *stubStreamingAgent) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	defer close(ch)
	for _, ev := range s.events {
		select {
		case ch <- ev:
		case <-ctx.Done():
			return AgentResult{}, ctx.Err()
		}
	}
	return s.result, s.err
}

func TestServeSSE(t *testing.T) {
	agent := &stubStreamingAgent{
		name: "test",
		desc: "test agent",
		events: []StreamEvent{
			{Type: EventTextDelta, Content: "Hello"},
			{Type: EventTextDelta, Content: " world"},
			{Type: EventToolCallStart, Name: "search", Args: json.RawMessage(`{"q":"test"}`)},
			{Type: EventToolCallResult, Name: "search", Content: "found it"},
		},
		result: AgentResult{Output: "Hello world"},
	}

	rec := httptest.NewRecorder()
	task := AgentTask{Input: "say hello"}

	result, err := ServeSSE(context.Background(), rec, agent, task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "Hello world" {
		t.Errorf("result.Output = %q, want %q", result.Output, "Hello world")
	}

	// Check headers.
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/event-stream")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-cache")
	}

	body := rec.Body.String()

	// Verify all 4 events are present.
	if strings.Count(body, "event: ") != 5 { // 4 stream events + 1 done
		t.Errorf("expected 5 event lines, got %d in:\n%s", strings.Count(body, "event: "), body)
	}

	// Verify event types appear in order.
	events := []string{"event: text-delta", "event: tool-call-start", "event: tool-call-result", "event: done"}
	pos := 0
	for _, ev := range events {
		idx := strings.Index(body[pos:], ev)
		if idx < 0 {
			t.Errorf("missing %q after position %d in body:\n%s", ev, pos, body)
			break
		}
		pos += idx + len(ev)
	}

	// Verify done event contains JSON-serialized AgentResult.
	doneIdx := strings.Index(body, "event: done\ndata: ")
	if doneIdx < 0 {
		t.Fatalf("missing done event in body:\n%s", body)
	}
	doneData := body[doneIdx+len("event: done\ndata: "):]
	doneData = strings.TrimRight(strings.SplitN(doneData, "\n", 2)[0], " ")
	var doneResult AgentResult
	if err := json.Unmarshal([]byte(doneData), &doneResult); err != nil {
		t.Fatalf("failed to parse done data as AgentResult: %v\ndata: %s", err, doneData)
	}
	if doneResult.Output != "Hello world" {
		t.Errorf("done result output = %q, want %q", doneResult.Output, "Hello world")
	}
}

func TestServeSSE_AgentError(t *testing.T) {
	agent := &stubStreamingAgent{
		name: "fail",
		desc: "fails",
		events: []StreamEvent{
			{Type: EventTextDelta, Content: "partial"},
		},
		err: errors.New("provider timeout"),
	}

	rec := httptest.NewRecorder()
	task := AgentTask{Input: "fail"}

	_, err := ServeSSE(context.Background(), rec, agent, task)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "provider timeout" {
		t.Errorf("err = %q, want %q", err.Error(), "provider timeout")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Errorf("missing error event in body:\n%s", body)
	}
	if !strings.Contains(body, "provider timeout") {
		t.Errorf("missing error message in body:\n%s", body)
	}
}

// nonFlusher is a ResponseWriter that does not implement http.Flusher.
type nonFlusher struct {
	header http.Header
}

func (n *nonFlusher) Header() http.Header        { return n.header }
func (n *nonFlusher) Write(b []byte) (int, error) { return len(b), nil }
func (n *nonFlusher) WriteHeader(int)             {}

func TestServeSSE_NoFlusher(t *testing.T) {
	agent := &stubStreamingAgent{name: "test", desc: "test"}
	w := &nonFlusher{header: http.Header{}}

	_, err := ServeSSE(context.Background(), w, agent, AgentTask{})
	if err == nil {
		t.Fatal("expected error for non-flusher ResponseWriter")
	}
	if !strings.Contains(err.Error(), "Flusher") {
		t.Errorf("err = %q, want mention of Flusher", err.Error())
	}
}
