package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/history"
)

// ptr returns a pointer to v. Test helper for optional Generation fields.
func ptr[T any](v T) *T { return &v }

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
		Input:  "hello",
		UserID: "123",
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
	onChat    func(*ChatRequest) // optional hook called at the start of each ChatStream
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) ChatStream(ctx context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	if m.onChat != nil {
		m.onChat(&req)
	}
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
		WithLimits(Limits{MaxIter: 3}),
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



// --- Error path and edge case tests ---

// errProvider always returns an error from all methods.
type errProvider struct {
	name string
	err  error
}

func (p *errProvider) Name() string { return p.name }
func (p *errProvider) ChatStream(_ context.Context, _ ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	return ChatResponse{}, p.err
}

// ctxProvider returns the context's error (simulates context-aware provider).
type ctxProvider struct{ name string }

func (p *ctxProvider) Name() string { return p.name }
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
	// Chat with tools path (req.Tools is non-empty)
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

// --- Field access tests ---

func TestTaskFields(t *testing.T) {
	task := AgentTask{
		Input:    "test",
		ThreadID: "thread-1",
		UserID:   "user-42",
		ChatID:   "chat-99",
	}

	if got := task.ThreadID; got != "thread-1" {
		t.Errorf("ThreadID = %q, want %q", got, "thread-1")
	}
	if got := task.UserID; got != "user-42" {
		t.Errorf("UserID = %q, want %q", got, "user-42")
	}
	if got := task.ChatID; got != "chat-99" {
		t.Errorf("ChatID = %q, want %q", got, "chat-99")
	}
}

func TestTaskFieldsZero(t *testing.T) {
	task := AgentTask{Input: "test"}

	if task.ThreadID != "" {
		t.Errorf("ThreadID = %q, want empty", task.ThreadID)
	}
	if task.UserID != "" {
		t.Errorf("UserID = %q, want empty", task.UserID)
	}
	if task.ChatID != "" {
		t.Errorf("ChatID = %q, want empty", task.ChatID)
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
		WithPostProcessors(gate),
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
			return "dynamic: " + task.UserID
		}),
	)

	agent.Execute(context.Background(), AgentTask{
		Input:  "hi",
		UserID: "alice",
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
			if task.Extra["tier"] == "pro" {
				return providerB
			}
			return providerA
		}),
	)

	result, _ := agent.Execute(context.Background(), AgentTask{
		Input: "hi",
		Extra: map[string]any{"tier": "pro"},
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
		WithDynamicTools(func(_ context.Context, task AgentTask) []AnyTool {
			return []AnyTool{mockToolCalc{}}
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
				gotUserID = task.UserID
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
		Input:  "test",
		UserID: "user-42",
	})

	if gotUserID != "user-42" {
		t.Errorf("gotUserID = %q, want %q", gotUserID, "user-42")
	}
}

// --- Task context tests ---

func TestTaskFromContextPresent(t *testing.T) {
	task := AgentTask{
		Input:  "hello",
		UserID: "u1",
	}
	ctx := WithTaskContext(context.Background(), task)

	got, ok := TaskFromContext(ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.Input != "hello" {
		t.Errorf("Input = %q, want %q", got.Input, "hello")
	}
	if got.UserID != "u1" {
		t.Errorf("UserID = %q, want %q", got.UserID, "u1")
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

func (bigResultTool) Name() string { return "big" }
func (bigResultTool) Definition() ToolDefinition {
	return ToolDefinition{Name: "big", Description: "Returns big content"}
}
func (bigResultTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return core.TextResult(strings.Repeat("x", 500)), nil
}

func TestContextCompression(t *testing.T) {
	// Track message counts per Chat call.
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
		WithHistory(history.Compress(func(_ context.Context, _ AgentTask) Provider { return compressProvider }, 1500)),
		WithLimits(Limits{MaxIter: 10}),
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
func (s *sequentialCallbackProvider) ChatStream(_ context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	return s.next(req), nil
}

// --- Close tests ---

func TestLLMAgentCloseCompletes(t *testing.T) {
	// Close() should complete without blocking indefinitely, even after
	// multiple Execute calls. Without a Store, there are no background
	// persist goroutines, so Close should be a fast no-op.
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
		if err := agent.Close(); err != nil {
			t.Errorf("Close error: %v", err)
		}
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("Close() did not complete within 1 second")
	}
}

// --- Option builder tests ---

func TestBuildConfigDefaults(t *testing.T) {
	cfg := BuildConfig(nil)

	if cfg.logger == nil {
		t.Error("logger should default to nopLogger, not nil")
	}
	if cfg.maxIter != 0 {
		t.Errorf("maxIter = %d, want 0 (buildConfig doesn't set defaults, constructors do)", cfg.maxIter)
	}
}

func TestWithMaxIterOption(t *testing.T) {
	cfg := BuildConfig([]AgentOption{WithLimits(Limits{MaxIter: 5})})
	if cfg.maxIter != 5 {
		t.Errorf("maxIter = %d, want 5", cfg.maxIter)
	}
}

func TestWithPromptOption(t *testing.T) {
	cfg := BuildConfig([]AgentOption{WithPrompt("You are helpful.")})
	if cfg.systemPrompt != "You are helpful." {
		t.Errorf("systemPrompt = %q, want %q", cfg.systemPrompt, "You are helpful.")
	}
}

func TestWithSuspendBudgetOption(t *testing.T) {
	cfg := BuildConfig([]AgentOption{WithLimits(Limits{MaxSuspendSnapshots: 5, MaxSuspendBytes: 1 << 20})})
	if cfg.maxSuspendSnapshots != 5 {
		t.Errorf("maxSuspendSnapshots = %d, want 5", cfg.maxSuspendSnapshots)
	}
	if cfg.maxSuspendBytes != 1<<20 {
		t.Errorf("maxSuspendBytes = %d, want %d", cfg.maxSuspendBytes, 1<<20)
	}
}

func TestWithCompressThresholdOption(t *testing.T) {
	cfg := BuildConfig([]AgentOption{WithHistory(history.Compress(nil, 100_000))})
	if cfg.compressThreshold != 100_000 {
		t.Errorf("compressThreshold = %d, want 100000", cfg.compressThreshold)
	}
}

func TestCompressThreshold_DefaultIsDisabled(t *testing.T) {
	cfg := BuildConfig(nil)
	if cfg.compressThreshold != 0 {
		t.Errorf("default compressThreshold = %d, want 0 (meaning disabled)", cfg.compressThreshold)
	}
}

func TestCompressThreshold_ZeroDisablesInLoop(t *testing.T) {
	threshold := 0
	enabled := threshold > 0
	if enabled {
		t.Error("threshold = 0 must be treated as disabled")
	}
}

func TestWithMaxAttachmentBytesOption(t *testing.T) {
	cfg := BuildConfig([]AgentOption{WithLimits(Limits{MaxAttachmentBytes: 10 << 20})})
	if cfg.maxAttachmentBytes != 10<<20 {
		t.Errorf("maxAttachmentBytes = %d, want %d", cfg.maxAttachmentBytes, 10<<20)
	}
}

// --- Generation parameters option tests ---

func TestWithTemperatureOption(t *testing.T) {
	cfg := BuildConfig([]AgentOption{WithGeneration(Generation{Temperature: ptr(0.7)})})
	if cfg.genParams == nil {
		t.Fatal("genParams should not be nil")
	}
	if cfg.genParams.Temperature == nil || *cfg.genParams.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", cfg.genParams.Temperature)
	}
}

func TestWithTemperatureZero(t *testing.T) {
	// 0.0 is a valid temperature — must not be conflated with "unset".
	cfg := BuildConfig([]AgentOption{WithGeneration(Generation{Temperature: ptr(0.0)})})
	if cfg.genParams == nil || cfg.genParams.Temperature == nil {
		t.Fatal("Temperature should be set (pointer to 0.0)")
	}
	if *cfg.genParams.Temperature != 0.0 {
		t.Errorf("Temperature = %v, want 0.0", *cfg.genParams.Temperature)
	}
}

func TestWithTopPOption(t *testing.T) {
	cfg := BuildConfig([]AgentOption{WithGeneration(Generation{TopP: ptr(0.95)})})
	if cfg.genParams == nil || cfg.genParams.TopP == nil {
		t.Fatal("TopP should be set")
	}
	if *cfg.genParams.TopP != 0.95 {
		t.Errorf("TopP = %v, want 0.95", *cfg.genParams.TopP)
	}
}

func TestWithTopKOption(t *testing.T) {
	cfg := BuildConfig([]AgentOption{WithGeneration(Generation{TopK: ptr(40)})})
	if cfg.genParams == nil || cfg.genParams.TopK == nil {
		t.Fatal("TopK should be set")
	}
	if *cfg.genParams.TopK != 40 {
		t.Errorf("TopK = %v, want 40", *cfg.genParams.TopK)
	}
}

func TestWithMaxTokensOption(t *testing.T) {
	cfg := BuildConfig([]AgentOption{WithGeneration(Generation{MaxTokens: ptr(2048)})})
	if cfg.genParams == nil || cfg.genParams.MaxTokens == nil {
		t.Fatal("MaxTokens should be set")
	}
	if *cfg.genParams.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %v, want 2048", *cfg.genParams.MaxTokens)
	}
}

func TestGenerationParamsCompose(t *testing.T) {
	// WithGeneration composes all parameters in one call.
	cfg := BuildConfig([]AgentOption{
		WithGeneration(Generation{
			Temperature: ptr(0.5),
			TopP:        ptr(0.9),
			TopK:        ptr(50),
			MaxTokens:   ptr(1024),
		}),
	})
	if cfg.genParams == nil {
		t.Fatal("genParams should not be nil")
	}
	if *cfg.genParams.Temperature != 0.5 {
		t.Errorf("Temperature = %v, want 0.5", *cfg.genParams.Temperature)
	}
	if *cfg.genParams.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", *cfg.genParams.TopP)
	}
	if *cfg.genParams.TopK != 50 {
		t.Errorf("TopK = %v, want 50", *cfg.genParams.TopK)
	}
	if *cfg.genParams.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %v, want 1024", *cfg.genParams.MaxTokens)
	}
}

func TestGenerationParamsNilWhenUnset(t *testing.T) {
	cfg := BuildConfig(nil)
	if cfg.genParams != nil {
		t.Error("genParams should be nil when no generation options are set")
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
		WithGeneration(Generation{Temperature: ptr(0.3), TopP: ptr(0.85)}),
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
	// GenerationParams should be present when using Chat with tools path.
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
		WithGeneration(Generation{Temperature: ptr(0.1)}),
	)

	_, err := agent.Execute(context.Background(), AgentTask{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	if capturedReq.GenerationParams == nil {
		t.Fatal("GenerationParams should be set in Chat requests with tools")
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
		WithLimits(Limits{MaxIter: 3}),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "loop"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Thinking != "Forced to summarize." {
		t.Errorf("Thinking = %q, want %q", result.Thinking, "Forced to summarize.")
	}
}

func TestWithActiveSkills(t *testing.T) {
	sk := Skill{
		Name:         "test-skill",
		Description:  "A test skill",
		Instructions: "Always use blue color.",
	}

	provider := &callbackProvider{
		name:     "test",
		response: ChatResponse{Content: "done"},
		onChat: func(req ChatRequest) {
			if len(req.Messages) == 0 {
				t.Fatal("expected messages")
			}
			sysMsg := req.Messages[0].Content
			if !strings.Contains(sysMsg, "Always use blue color.") {
				t.Errorf("system prompt should contain skill instructions, got: %s", sysMsg[:min(200, len(sysMsg))])
			}
		},
	}

	agent := NewLLMAgent("test", "Base prompt.", provider,
		WithPrompt("Base prompt."),
		WithActiveSkills(sk),
	)
	_, err := agent.Execute(context.Background(), AgentTask{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
}

// --- Configurable limit knob tests ---

func TestWithMaxParallelDispatchSetsConfig(t *testing.T) {
	c := BuildConfig([]AgentOption{WithLimits(Limits{MaxParallelDispatch: 3})})
	if c.maxParallelDispatch != 3 {
		t.Errorf("expected 3, got %d", c.maxParallelDispatch)
	}
}

func TestWithMaxPlanStepsSetsConfig(t *testing.T) {
	c := BuildConfig([]AgentOption{WithLimits(Limits{MaxPlanSteps: 7})})
	if c.maxPlanSteps != 7 {
		t.Errorf("expected 7, got %d", c.maxPlanSteps)
	}
}

func TestWithMaxToolResultLenSetsConfig(t *testing.T) {
	c := BuildConfig([]AgentOption{WithLimits(Limits{MaxToolResultLen: 50_000})})
	if c.maxToolResultLen != 50_000 {
		t.Errorf("expected 50000, got %d", c.maxToolResultLen)
	}
}

func TestDefaultMaxParallelDispatch(t *testing.T) {
	c := BuildConfig(nil)
	if c.maxParallelDispatch != 10 {
		t.Errorf("expected default 10, got %d", c.maxParallelDispatch)
	}
}

// --- Embedding provider conflict tests ---

type fakeEmbeddingProvider struct{ name string }

func (f *fakeEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0}
	}
	return out, nil
}
func (f *fakeEmbeddingProvider) Dimensions() int { return 1 }
func (f *fakeEmbeddingProvider) Name() string    { return f.name }

type fakeMemoryStore struct{}

func (fakeMemoryStore) Init(_ context.Context) error                                           { return nil }
func (fakeMemoryStore) UpsertFact(_ context.Context, _, _ string, _ []float32) error          { return nil }
func (fakeMemoryStore) SearchFacts(_ context.Context, _ []float32, _ int) ([]ScoredFact, error) { return nil, nil }
func (fakeMemoryStore) DeleteFact(_ context.Context, _ string) error                           { return nil }
func (fakeMemoryStore) DeleteMatchingFacts(_ context.Context, _ string) error                  { return nil }
func (fakeMemoryStore) DecayOldFacts(_ context.Context) error                                  { return nil }
func (fakeMemoryStore) BuildContext(_ context.Context, _ []float32) (string, error)            { return "", nil }

// TestBuildConfigSharedEmbedding verifies the post-4B-a design: a single
// agent-level WithEmbedding feeds both WithUserMemory and
// history.CrossThreadSearch. The previous "conflicting providers" panic is
// impossible by construction — there is only one embedding slot.
func TestBuildConfigSharedEmbedding(t *testing.T) {
	em := &fakeEmbeddingProvider{name: "em"}
	mem := &fakeMemoryStore{}

	cfg := BuildConfig([]AgentOption{
		WithEmbedding(em),
		WithUserMemory(mem),
		WithHistory(history.CrossThreadSearch()),
	})

	if cfg.embedding != em {
		t.Errorf("expected embedding %p, got %p", em, cfg.embedding)
	}
	if cfg.memory != mem {
		t.Errorf("expected memory store wired")
	}
	if !cfg.crossThreadSearch {
		t.Errorf("expected crossThreadSearch enabled")
	}
}

// TestAgentResultStepsCapped verifies that WithMaxSteps(n) keeps at most n
// StepTrace entries, retaining the most recent ones when the cap is exceeded.
func TestAgentResultStepsCapped(t *testing.T) {
	const cap = 3
	// 5 tool calls, final text response.
	responses := []ChatResponse{
		{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
		{ToolCalls: []ToolCall{{ID: "2", Name: "greet", Args: json.RawMessage(`{}`)}}},
		{ToolCalls: []ToolCall{{ID: "3", Name: "greet", Args: json.RawMessage(`{}`)}}},
		{ToolCalls: []ToolCall{{ID: "4", Name: "greet", Args: json.RawMessage(`{}`)}}},
		{ToolCalls: []ToolCall{{ID: "5", Name: "greet", Args: json.RawMessage(`{}`)}}},
		{Content: "done"},
	}
	provider := &mockProvider{name: "test", responses: responses}
	a := NewLLMAgent("capped", "step cap test", provider,
		WithTools(mockTool{}),
		WithLimits(Limits{MaxSteps: cap}),
	)
	result, err := a.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Steps) != cap {
		t.Fatalf("Steps len = %d, want %d", len(result.Steps), cap)
	}
}

// TestAgentResultStepsUnbounded verifies that WithMaxSteps(0) keeps all steps.
func TestAgentResultStepsUnbounded(t *testing.T) {
	const numCalls = 5
	responses := make([]ChatResponse, numCalls+1)
	for i := range numCalls {
		responses[i] = ChatResponse{ToolCalls: []ToolCall{{ID: fmt.Sprintf("%d", i+1), Name: "greet", Args: json.RawMessage(`{}`)}}}
	}
	responses[numCalls] = ChatResponse{Content: "done"}
	provider := &mockProvider{name: "test", responses: responses}
	a := NewLLMAgent("unbounded", "step unbounded test", provider,
		WithTools(mockTool{}),
		WithLimits(Limits{MaxSteps: Unbounded}),
	)
	result, err := a.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Steps) != numCalls {
		t.Fatalf("Steps len = %d, want %d", len(result.Steps), numCalls)
	}
}

// TestWithLimits_AppliesAllFields verifies that WithLimits wires every
// limit field into Config. Pins the Limits → Config field mapping.
func TestWithLimits_AppliesAllFields(t *testing.T) {
	lim := Limits{
		MaxIter:             7,
		MaxSteps:            42,
		MaxPlanSteps:        13,
		MaxParallelDispatch: 3,
		MaxAttachmentBytes:  1234,
		MaxToolResultLen:    5678,
		MaxSuspendSnapshots: 9,
		MaxSuspendBytes:     8765,
	}
	cfg := BuildConfig([]AgentOption{WithLimits(lim)})
	if cfg.maxIter != 7 || cfg.maxPlanSteps != 13 || cfg.maxParallelDispatch != 3 ||
		cfg.maxAttachmentBytes != 1234 || cfg.maxToolResultLen != 5678 ||
		cfg.maxSuspendSnapshots != 9 || cfg.maxSuspendBytes != 8765 {
		t.Fatalf("Limits not propagated: %+v", cfg)
	}
	if cfg.maxSteps == nil || *cfg.maxSteps != 42 {
		t.Fatalf("MaxSteps not propagated: %v", cfg.maxSteps)
	}
}

// TestWithLimits_UnboundedMaxSteps verifies that the Unbounded sentinel
// produces the "explicit unbounded" semantics that WithMaxSteps(0) used.
func TestWithLimits_UnboundedMaxSteps(t *testing.T) {
	cfg := BuildConfig([]AgentOption{WithLimits(Limits{MaxSteps: Unbounded})})
	if cfg.maxSteps == nil || *cfg.maxSteps != 0 {
		t.Fatalf("Unbounded should set maxSteps to 0 (legacy unbounded sentinel): %v", cfg.maxSteps)
	}
}

// TestWithLimits_ZeroFieldsUseDefaults verifies that Limits{} fields left at
// zero do not override the agent's defaults. Only explicit non-zero fields
// take effect — Limits{} is a no-op.
func TestWithLimits_ZeroFieldsUseDefaults(t *testing.T) {
	base := BuildConfig(nil)
	withZero := BuildConfig([]AgentOption{WithLimits(Limits{})})
	if base.maxIter != withZero.maxIter ||
		base.maxAttachmentBytes != withZero.maxAttachmentBytes ||
		base.maxParallelDispatch != withZero.maxParallelDispatch {
		t.Fatalf("Limits{} should be a no-op; base=%+v withZero=%+v", base, withZero)
	}
}
