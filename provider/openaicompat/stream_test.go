package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// buildSSE constructs a mock SSE stream from data lines.
func buildSSE(lines ...string) string {
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString("data: ")
		sb.WriteString(line)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

func TestStreamSSE_TextChunks(t *testing.T) {
	sse := buildSSE(
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"!"}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
		"[DONE]",
	)

	reader := strings.NewReader(sse)
	ch := make(chan string, 10)

	resp, err := StreamSSE(reader, ch)
	if err != nil {
		t.Fatalf("StreamSSE returned error: %v", err)
	}

	// Collect deltas from channel.
	var deltas []string
	for d := range ch {
		deltas = append(deltas, d)
	}

	if resp.Content != "Hello world!" {
		t.Errorf("expected content 'Hello world!', got %q", resp.Content)
	}

	// Should have received 3 non-empty deltas.
	if len(deltas) != 3 {
		t.Errorf("expected 3 deltas, got %d: %v", len(deltas), deltas)
	}

	if resp.Usage.InputTokens != 5 {
		t.Errorf("expected 5 input tokens, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 3 {
		t.Errorf("expected 3 output tokens, got %d", resp.Usage.OutputTokens)
	}
}

func TestStreamSSE_ToolCallChunks(t *testing.T) {
	// OpenAI streams tool calls incrementally:
	// 1. First chunk: tool call ID + function name
	// 2. Subsequent chunks: argument fragments
	sse := buildSSE(
		`{"id":"chatcmpl-2","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`{"id":"chatcmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]}}]}`,
		`{"id":"chatcmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"London"}}]}}]}`,
		`{"id":"chatcmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]}}]}`,
		`{"id":"chatcmpl-2","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":15,"total_tokens":25}}`,
		"[DONE]",
	)

	reader := strings.NewReader(sse)
	ch := make(chan string, 10)

	resp, err := StreamSSE(reader, ch)
	if err != nil {
		t.Fatalf("StreamSSE returned error: %v", err)
	}

	// Drain channel (should be empty since tool calls don't produce text deltas).
	var deltas []string
	for d := range ch {
		deltas = append(deltas, d)
	}
	if len(deltas) != 0 {
		t.Errorf("expected no text deltas for tool call stream, got %d", len(deltas))
	}

	if resp.Content != "" {
		t.Errorf("expected empty content, got %q", resp.Content)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}

	tc := resp.ToolCalls[0]
	if tc.ID != "call_abc" {
		t.Errorf("expected ID 'call_abc', got %q", tc.ID)
	}
	if tc.Name != "get_weather" {
		t.Errorf("expected name 'get_weather', got %q", tc.Name)
	}

	var args map[string]any
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("failed to parse tool call args: %v", err)
	}
	if args["city"] != "London" {
		t.Errorf("expected city 'London', got %v", args["city"])
	}

	if resp.Usage.InputTokens != 10 {
		t.Errorf("expected 10 input tokens, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 15 {
		t.Errorf("expected 15 output tokens, got %d", resp.Usage.OutputTokens)
	}
}

func TestStreamSSE_MultipleToolCalls(t *testing.T) {
	sse := buildSSE(
		`{"id":"chatcmpl-3","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search","arguments":""}}]}}]}`,
		`{"id":"chatcmpl-3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"test\"}"}}]}}]}`,
		`{"id":"chatcmpl-3","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"calc","arguments":""}}]}}]}`,
		`{"id":"chatcmpl-3","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"expr\":\"1+1\"}"}}]}}]}`,
		`{"id":"chatcmpl-3","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"[DONE]",
	)

	reader := strings.NewReader(sse)
	ch := make(chan string, 10)

	resp, err := StreamSSE(reader, ch)
	if err != nil {
		t.Fatalf("StreamSSE returned error: %v", err)
	}

	// Drain channel.
	for range ch {
	}

	if len(resp.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(resp.ToolCalls))
	}

	if resp.ToolCalls[0].Name != "search" {
		t.Errorf("expected first tool 'search', got %q", resp.ToolCalls[0].Name)
	}
	if resp.ToolCalls[0].ID != "call_1" {
		t.Errorf("expected first tool ID 'call_1', got %q", resp.ToolCalls[0].ID)
	}

	if resp.ToolCalls[1].Name != "calc" {
		t.Errorf("expected second tool 'calc', got %q", resp.ToolCalls[1].Name)
	}
	if resp.ToolCalls[1].ID != "call_2" {
		t.Errorf("expected second tool ID 'call_2', got %q", resp.ToolCalls[1].ID)
	}
}

func TestStreamSSE_EmptyStream(t *testing.T) {
	sse := buildSSE("[DONE]")

	reader := strings.NewReader(sse)
	ch := make(chan string, 10)

	resp, err := StreamSSE(reader, ch)
	if err != nil {
		t.Fatalf("StreamSSE returned error: %v", err)
	}

	// Drain channel.
	for range ch {
	}

	if resp.Content != "" {
		t.Errorf("expected empty content, got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(resp.ToolCalls))
	}
}

func TestStreamSSE_UsageOnlyChunk(t *testing.T) {
	// Some providers send usage in a separate chunk with no choices.
	sse := buildSSE(
		`{"id":"chatcmpl-4","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"}}]}`,
		`{"id":"chatcmpl-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-4","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		"[DONE]",
	)

	reader := strings.NewReader(sse)
	ch := make(chan string, 10)

	resp, err := StreamSSE(reader, ch)
	if err != nil {
		t.Fatalf("StreamSSE returned error: %v", err)
	}

	// Drain channel.
	for range ch {
	}

	if resp.Content != "Hi" {
		t.Errorf("expected content 'Hi', got %q", resp.Content)
	}
	if resp.Usage.InputTokens != 3 {
		t.Errorf("expected 3 input tokens, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 1 {
		t.Errorf("expected 1 output tokens, got %d", resp.Usage.OutputTokens)
	}
}

func TestStreamSSE_SkipsMalformedChunks(t *testing.T) {
	sse := buildSSE(
		`{"id":"chatcmpl-5","choices":[{"index":0,"delta":{"content":"Good"}}]}`,
		`this is not json`,
		`{"id":"chatcmpl-5","choices":[{"index":0,"delta":{"content":" day"}}]}`,
		"[DONE]",
	)

	reader := strings.NewReader(sse)
	ch := make(chan string, 10)

	resp, err := StreamSSE(reader, ch)
	if err != nil {
		t.Fatalf("StreamSSE returned error: %v", err)
	}

	// Drain channel.
	for range ch {
	}

	// Should skip the malformed chunk and continue.
	if resp.Content != "Good day" {
		t.Errorf("expected content 'Good day', got %q", resp.Content)
	}
}

func TestStreamSSE_NonDataLinesIgnored(t *testing.T) {
	// SSE streams can have comments, event types, retry directives, etc.
	raw := ": this is a comment\n" +
		"event: message\n" +
		"data: {\"id\":\"chatcmpl-6\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"}}]}\n\n" +
		"retry: 3000\n" +
		"data: [DONE]\n\n"

	reader := strings.NewReader(raw)
	ch := make(chan string, 10)

	resp, err := StreamSSE(reader, ch)
	if err != nil {
		t.Fatalf("StreamSSE returned error: %v", err)
	}

	// Drain channel.
	for range ch {
	}

	if resp.Content != "OK" {
		t.Errorf("expected content 'OK', got %q", resp.Content)
	}
}
