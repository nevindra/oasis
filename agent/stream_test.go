package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nevindra/oasis/core"
)

// --- New event type tests ---

func TestNewStreamEventTypes(t *testing.T) {
	// Verify the new event type constants have the expected string values.
	tests := []struct {
		got  StreamEventType
		want string
	}{
		{EventToolCallDelta, "tool-call-delta"},
		{EventToolProgress, "tool-progress"},
		{EventStepStart, "step-start"},
		{EventStepFinish, "step-finish"},
		{EventStepProgress, "step-progress"},
		{EventRoutingDecision, "routing-decision"},
		{EventReasoningStart, "reasoning-start"},
		{EventReasoningDelta, "reasoning-delta"},
		{EventReasoningEnd, "reasoning-end"},
		{EventHalt, "halt"},
		{EventError, "error"},
		{EventStreamWarning, "stream-warning"},
		{EventToolApprovalPending, "tool-approval-pending"},
	}
	for _, tt := range tests {
		if string(tt.got) != tt.want {
			t.Errorf("event type = %q, want %q", tt.got, tt.want)
		}
	}
}

func TestStreamEventIDField(t *testing.T) {
	ev := StreamEvent{
		Type: EventToolCallStart,
		ID:   "call_123",
		Name: "search",
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"id":"call_123"`) {
		t.Errorf("JSON missing id field: %s", data)
	}

	// Zero-value ID should be omitted.
	ev2 := StreamEvent{Type: EventTextDelta, Content: "hi"}
	data2, _ := json.Marshal(ev2)
	if strings.Contains(string(data2), `"id"`) {
		t.Errorf("empty ID should be omitted: %s", data2)
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
		t.Fatalf("expected at least 3 events (run-start, iteration-start, text-delta...), got %d", len(events))
	}
	// First event should be run-start (replaced EventInputReceived + EventProcessingStart).
	if events[0].Type != EventRunStart {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, EventRunStart)
	}
	if events[0].Name != "streamer" {
		t.Errorf("events[0].Name = %q, want %q", events[0].Name, "streamer")
	}
	if events[0].Content != "hi" {
		t.Errorf("events[0].Content = %q, want %q", events[0].Content, "hi")
	}
	// Second event should be iteration-start.
	if events[1].Type != EventIterationStart {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, EventIterationStart)
	}
	// Last event should be run-finish.
	if events[len(events)-1].Type != EventRunFinish {
		t.Errorf("last event type = %q, want %q", events[len(events)-1].Type, EventRunFinish)
	}
}

func TestLLMAgentExecuteStreamWithTools(t *testing.T) {
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			// First: tool call (blocking)
			{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
			// Second: final text response (streamed as single chunk since from Chat with tools)
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
	// First two events should be the new lifecycle events.
	if events[0].Type != EventRunStart {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, EventRunStart)
	}
	if events[1].Type != EventIterationStart {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, EventIterationStart)
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

// --- SSE tests ---

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

// --- ServeSSE panic recovery ---

func TestServeSSE_AgentPanic(t *testing.T) {
	// An agent that panics inside ExecuteStream should be caught by
	// ServeSSE's panic recovery, not crash the process.
	panicAgent := &panicStreamingAgent{
		name: "panicker",
		desc: "Panics during stream",
	}

	rec := httptest.NewRecorder()
	_, err := ServeSSE(context.Background(), rec, panicAgent, AgentTask{Input: "crash"})
	if err == nil {
		t.Fatal("expected error from panicking agent")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Errorf("err = %q, want mention of panic", err.Error())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Errorf("missing error event in body:\n%s", body)
	}
}

// panicStreamingAgent is a StreamingAgent that panics during ExecuteStream.
type panicStreamingAgent struct {
	name string
	desc string
}

func (p *panicStreamingAgent) Name() string        { return p.name }
func (p *panicStreamingAgent) Description() string { return p.desc }
func (p *panicStreamingAgent) Execute(_ context.Context, _ AgentTask) (AgentResult, error) {
	panic("agent panic in Execute")
}
func (p *panicStreamingAgent) ExecuteStream(_ context.Context, _ AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	// Send one event before panicking.
	ch <- StreamEvent{Type: EventTextDelta, Content: "partial"}
	panic("agent panic in ExecuteStream")
}

// --- WriteSSEEvent tests ---

func TestWriteSSEEvent(t *testing.T) {
	rec := httptest.NewRecorder()

	data := map[string]string{"hello": "world"}
	err := WriteSSEEvent(rec, "test-event", data)
	if err != nil {
		t.Fatal(err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: test-event") {
		t.Errorf("missing event type in body:\n%s", body)
	}
	if !strings.Contains(body, `"hello":"world"`) {
		t.Errorf("missing JSON data in body:\n%s", body)
	}
}

func TestWriteSSEEvent_NoFlusher(t *testing.T) {
	w := &nonFlusher{header: http.Header{}}

	err := WriteSSEEvent(w, "test", "data")
	if err == nil {
		t.Fatal("expected error for non-flusher ResponseWriter")
	}
	if !strings.Contains(err.Error(), "Flusher") {
		t.Errorf("err = %q, want mention of Flusher", err.Error())
	}
}

func TestWriteSSEEvent_MarshalError(t *testing.T) {
	rec := httptest.NewRecorder()

	// Channels cannot be marshaled to JSON.
	err := WriteSSEEvent(rec, "test", make(chan int))
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if !strings.Contains(err.Error(), "marshal") {
		t.Errorf("err = %q, want mention of marshal", err.Error())
	}
}

// --- Stream provider error closes channel ---

func TestLLMAgentExecuteStreamContextCancellation(t *testing.T) {
	// When context is cancelled during streaming, the channel should be closed.
	ctx, cancel := context.WithCancel(context.Background())

	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			// Tool call that will trigger context cancellation.
			{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
		},
	}

	cancelTool := &contextReadingTool{
		onExecute: func(_ context.Context) {
			cancel() // cancel context during tool execution
		},
	}

	agent := NewLLMAgent("cancel", "Cancel test", provider,
		WithTools(cancelTool),
	)

	ch := make(chan StreamEvent, 32)
	_, _ = agent.ExecuteStream(ctx, AgentTask{Input: "go"}, ch)

	// Channel should be closed — verify by draining.
	drained := false
	for range ch {
		drained = true
	}
	_ = drained // just verify the range terminates (channel closed)
}

// --- Thinking event tests ---

func TestThinkingEventEmitted(t *testing.T) {
	// When provider returns thinking content in a tool-calling loop,
	// EventThinking events should be emitted on the stream channel.
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{
				ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}},
				Thinking:  "I need to call the greet tool.",
			},
			{
				Content:  "Done!",
				Thinking: "Now I can respond.",
			},
		},
	}

	agent := NewLLMAgent("thinker", "Emits thinking", provider,
		WithTools(mockTool{}),
	)

	ch := make(chan StreamEvent, 32)
	result, err := agent.ExecuteStream(context.Background(), AgentTask{Input: "greet"}, ch)
	if err != nil {
		t.Fatal(err)
	}

	var thinkingEvents []StreamEvent
	for ev := range ch {
		if ev.Type == EventThinking {
			thinkingEvents = append(thinkingEvents, ev)
		}
	}

	if len(thinkingEvents) != 2 {
		t.Fatalf("expected 2 thinking events, got %d", len(thinkingEvents))
	}
	if thinkingEvents[0].Content != "I need to call the greet tool." {
		t.Errorf("thinking[0] = %q, want %q", thinkingEvents[0].Content, "I need to call the greet tool.")
	}
	if thinkingEvents[1].Content != "Now I can respond." {
		t.Errorf("thinking[1] = %q, want %q", thinkingEvents[1].Content, "Now I can respond.")
	}
	if result.Thinking != "Now I can respond." {
		t.Errorf("result.Thinking = %q, want %q", result.Thinking, "Now I can respond.")
	}
}

// --- StreamingAnyTool tests ---

// progressTool implements StreamingAnyTool for testing.
type progressTool struct{}

func (t progressTool) Name() string { return "slow_search" }

func (t progressTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "slow_search",
		Description: "Slow search with progress",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
	}
}

func (t progressTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return core.TextResult("found 3 results"), nil
}

func (t progressTool) ExecuteStream(_ context.Context, _ json.RawMessage, ch chan<- StreamEvent) (ToolResult, error) {
	for i := 1; i <= 3; i++ {
		ch <- StreamEvent{
			Type:    EventToolProgress,
			Name:    "slow_search",
			Content: fmt.Sprintf(`{"found":%d}`, i),
		}
	}
	return core.TextResult("found 3 results"), nil
}

func TestStreamingToolEmitsProgress(t *testing.T) {
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "slow_search", Args: json.RawMessage(`{"q":"test"}`)}}},
			{Content: "done"},
		},
	}

	agent := NewLLMAgent("searcher", "Searches", provider,
		WithTools(progressTool{}),
	)

	ch := make(chan StreamEvent, 32)
	result, err := agent.ExecuteStream(context.Background(), AgentTask{Input: "search"}, ch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}

	var progressEvents []StreamEvent
	for ev := range ch {
		if ev.Type == EventToolProgress {
			progressEvents = append(progressEvents, ev)
		}
	}
	if len(progressEvents) != 3 {
		t.Fatalf("expected 3 tool-progress events, got %d", len(progressEvents))
	}
	if progressEvents[0].Name != "slow_search" {
		t.Errorf("progress[0].Name = %q, want %q", progressEvents[0].Name, "slow_search")
	}
}

func TestStreamingToolFallsBackToExecute(t *testing.T) {
	// When not streaming (Execute, not ExecuteStream), StreamingAnyTool should
	// fall back to ExecuteRaw.
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "slow_search", Args: json.RawMessage(`{"q":"test"}`)}}},
			{Content: "done"},
		},
	}

	agent := NewLLMAgent("searcher", "Searches", provider,
		WithTools(progressTool{}),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "search"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}
}

func TestNoThinkingEventWhenEmpty(t *testing.T) {
	// When provider returns no thinking content, no EventThinking events should appear.
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "greet", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
	}

	agent := NewLLMAgent("no-think", "No thinking", provider,
		WithTools(mockTool{}),
	)

	ch := make(chan StreamEvent, 32)
	_, err := agent.ExecuteStream(context.Background(), AgentTask{Input: "greet"}, ch)
	if err != nil {
		t.Fatal(err)
	}

	for ev := range ch {
		if ev.Type == EventThinking {
			t.Error("unexpected thinking event when provider returns no thinking")
		}
	}
}

// --- Workflow streaming tests ---

// --- Max-iteration tests (EventMaxIterReached replaced by EventRunFinish{FinishReason: FinishMaxIter}) ---

// alwaysToolProvider is a Provider that always returns a single ToolCall so
// the loop keeps requesting tools until it hits maxIter.
type alwaysToolProvider struct {
	toolName string
	synthResp ChatResponse
}

func (a *alwaysToolProvider) Name() string { return "always-tool" }
func (a *alwaysToolProvider) ChatStream(_ context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	// If the last message looks like a forced-synthesis prompt, return text.
	if len(req.Messages) > 0 {
		last := req.Messages[len(req.Messages)-1]
		if last.Role == "user" && len(last.Content) > 20 &&
			last.Content[:20] == "You have used all av" {
			return a.synthResp, nil
		}
	}
	return ChatResponse{
		ToolCalls: []ToolCall{{ID: "loop-1", Name: a.toolName, Args: json.RawMessage(`{}`)}},
	}, nil
}

// TestEventMaxIterReachedEmitted verifies that hitting max iterations results
// in an EventRunFinish with FinishReason=FinishMaxIter (replaces the
// deprecated EventMaxIterReached event).
func TestEventMaxIterReachedEmitted(t *testing.T) {
	provider := &alwaysToolProvider{
		toolName:  "loop_tool",
		synthResp: ChatResponse{Content: "forced synthesis result"},
	}
	a := NewLLMAgent("test", "", provider,
		WithTools(&configuredFakeAgentTool{name: "loop_tool", output: "still going"}),
		WithLimits(Limits{MaxIter: 3}),
	)

	ch := make(chan StreamEvent, 64)
	_, _ = a.ExecuteStream(context.Background(), AgentTask{Input: "loop"}, ch)

	var sawRunFinish bool
	var sawMaxIterReached bool
	for ev := range ch {
		if ev.Type == EventRunFinish && ev.FinishReason == FinishMaxIter {
			sawRunFinish = true
		}
		if ev.Type == EventMaxIterReached {
			sawMaxIterReached = true
		}
	}
	if !sawRunFinish {
		t.Error("expected EventRunFinish with FinishReason=FinishMaxIter, got none")
	}
	if sawMaxIterReached {
		t.Error("EventMaxIterReached should not be emitted (replaced by EventRunFinish)")
	}
}

// configuredFakeAgentTool is a local AnyTool with a configurable name/output.
type configuredFakeAgentTool struct {
	name   string
	output string
}

func (t *configuredFakeAgentTool) Name() string { return t.name }
func (t *configuredFakeAgentTool) Definition() ToolDefinition {
	return ToolDefinition{Name: t.name, Description: "fake tool"}
}
func (t *configuredFakeAgentTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return core.TextResult(t.output), nil
}

