package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
