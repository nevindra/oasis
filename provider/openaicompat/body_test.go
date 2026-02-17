package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis"
)

func TestBuildBody_SystemMessages(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello"},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)

	if req.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", req.Model)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(req.Messages))
	}

	// System message stays as role:"system".
	if req.Messages[0].Role != "system" {
		t.Errorf("expected role 'system', got %q", req.Messages[0].Role)
	}
	if req.Messages[0].Content != "You are a helpful assistant." {
		t.Errorf("unexpected system content: %v", req.Messages[0].Content)
	}

	// User message.
	if req.Messages[1].Role != "user" {
		t.Errorf("expected role 'user', got %q", req.Messages[1].Role)
	}
}

func TestBuildBody_UserAndAssistant(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "Hello!"},
		{Role: "user", Content: "How are you?"},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)

	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(req.Messages))
	}

	if req.Messages[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", req.Messages[0].Role)
	}
	if req.Messages[1].Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", req.Messages[1].Role)
	}
	if req.Messages[1].Content != "Hello!" {
		t.Errorf("unexpected assistant content: %v", req.Messages[1].Content)
	}
	if req.Messages[2].Role != "user" {
		t.Errorf("expected role 'user', got %q", req.Messages[2].Role)
	}
}

func TestBuildBody_AssistantWithToolCalls(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Search for cats"},
		{
			Role:    "assistant",
			Content: "Let me search for that.",
			ToolCalls: []oasis.ToolCall{
				{
					ID:   "call_123",
					Name: "search",
					Args: json.RawMessage(`{"query":"cats"}`),
				},
			},
		},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)

	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(req.Messages))
	}

	assistantMsg := req.Messages[1]
	if assistantMsg.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", assistantMsg.Role)
	}
	if assistantMsg.Content != "Let me search for that." {
		t.Errorf("unexpected content: %v", assistantMsg.Content)
	}
	if len(assistantMsg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(assistantMsg.ToolCalls))
	}

	tc := assistantMsg.ToolCalls[0]
	if tc.ID != "call_123" {
		t.Errorf("expected tool call ID 'call_123', got %q", tc.ID)
	}
	if tc.Type != "function" {
		t.Errorf("expected type 'function', got %q", tc.Type)
	}
	if tc.Function.Name != "search" {
		t.Errorf("expected function name 'search', got %q", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"query":"cats"}` {
		t.Errorf("expected arguments as JSON string, got %q", tc.Function.Arguments)
	}
}

func TestBuildBody_ToolResult(t *testing.T) {
	messages := []oasis.ChatMessage{
		{
			Role:       "tool",
			Content:    "Found 10 results about cats",
			ToolCallID: "call_123",
		},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)

	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(req.Messages))
	}

	msg := req.Messages[0]
	if msg.Role != "tool" {
		t.Errorf("expected role 'tool', got %q", msg.Role)
	}
	if msg.Content != "Found 10 results about cats" {
		t.Errorf("unexpected content: %v", msg.Content)
	}
	if msg.ToolCallID != "call_123" {
		t.Errorf("expected tool_call_id 'call_123', got %q", msg.ToolCallID)
	}
}

func TestBuildBody_Images(t *testing.T) {
	messages := []oasis.ChatMessage{
		{
			Role:    "user",
			Content: "What is this?",
			Images: []oasis.ImageData{
				{MimeType: "image/png", Base64: "iVBOR..."},
			},
		},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)

	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(req.Messages))
	}

	msg := req.Messages[0]
	if msg.Role != "user" {
		t.Errorf("expected role 'user', got %q", msg.Role)
	}

	// Content should be []ContentBlock, not a string.
	blocks, ok := msg.Content.([]ContentBlock)
	if !ok {
		t.Fatalf("expected content to be []ContentBlock, got %T", msg.Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks (text + image), got %d", len(blocks))
	}

	// First block: text.
	if blocks[0].Type != "text" {
		t.Errorf("expected first block type 'text', got %q", blocks[0].Type)
	}
	if blocks[0].Text != "What is this?" {
		t.Errorf("unexpected text: %q", blocks[0].Text)
	}

	// Second block: image_url.
	if blocks[1].Type != "image_url" {
		t.Errorf("expected second block type 'image_url', got %q", blocks[1].Type)
	}
	if blocks[1].ImageURL == nil {
		t.Fatal("expected image_url to be non-nil")
	}
	expectedURL := "data:image/png;base64,iVBOR..."
	if blocks[1].ImageURL.URL != expectedURL {
		t.Errorf("expected URL %q, got %q", expectedURL, blocks[1].ImageURL.URL)
	}
}

func TestBuildBody_WithTools(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}
	tools := []oasis.ToolDefinition{
		{
			Name:        "get_weather",
			Description: "Get the current weather",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		},
	}

	req := BuildBody(messages, tools, "gpt-4o", nil)

	if len(req.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(req.Tools))
	}

	tool := req.Tools[0]
	if tool.Type != "function" {
		t.Errorf("expected type 'function', got %q", tool.Type)
	}
	if tool.Function.Name != "get_weather" {
		t.Errorf("expected name 'get_weather', got %q", tool.Function.Name)
	}
	if tool.Function.Description != "Get the current weather" {
		t.Errorf("unexpected description: %q", tool.Function.Description)
	}

	// Parameters should be preserved as JSON.
	var params map[string]any
	if err := json.Unmarshal(tool.Function.Parameters, &params); err != nil {
		t.Fatalf("failed to parse parameters: %v", err)
	}
	if params["type"] != "object" {
		t.Errorf("expected parameters type 'object', got %v", params["type"])
	}
}

func TestBuildBody_NoTools(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)

	if len(req.Tools) != 0 {
		t.Errorf("expected no tools, got %d", len(req.Tools))
	}
}

func TestBuildToolDefs(t *testing.T) {
	tools := []oasis.ToolDefinition{
		{
			Name:        "search",
			Description: "Search the web",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		},
		{
			Name:        "calc",
			Description: "Calculate expression",
			Parameters:  nil, // empty parameters
		},
	}

	result := BuildToolDefs(tools)

	if len(result) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result))
	}

	// First tool.
	if result[0].Type != "function" {
		t.Errorf("expected type 'function', got %q", result[0].Type)
	}
	if result[0].Function.Name != "search" {
		t.Errorf("expected name 'search', got %q", result[0].Function.Name)
	}

	// Second tool with empty parameters should default to {}.
	var params map[string]any
	if err := json.Unmarshal(result[1].Function.Parameters, &params); err != nil {
		t.Fatalf("failed to parse empty parameters: %v", err)
	}
	if len(params) != 0 {
		t.Errorf("expected empty params object, got %v", params)
	}
}

func TestBuildBody_JSONRoundTrip(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "system", Content: "Be helpful."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi!"},
		{
			Role: "assistant",
			ToolCalls: []oasis.ToolCall{
				{ID: "call_1", Name: "search", Args: json.RawMessage(`{"q":"test"}`)},
			},
		},
		{Role: "tool", Content: "results", ToolCallID: "call_1"},
	}
	tools := []oasis.ToolDefinition{
		{Name: "search", Description: "Search", Parameters: json.RawMessage(`{"type":"object"}`)},
	}

	req := BuildBody(messages, tools, "gpt-4o", nil)

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	// Verify it's valid JSON.
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to parse round-tripped JSON: %v", err)
	}

	if parsed["model"] != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o' in JSON, got %v", parsed["model"])
	}

	msgs, ok := parsed["messages"].([]any)
	if !ok {
		t.Fatal("expected messages array in JSON")
	}
	if len(msgs) != 5 {
		t.Errorf("expected 5 messages in JSON, got %d", len(msgs))
	}
}

func TestBuildBody_MultipleToolCalls(t *testing.T) {
	messages := []oasis.ChatMessage{
		{
			Role: "assistant",
			ToolCalls: []oasis.ToolCall{
				{ID: "call_1", Name: "search", Args: json.RawMessage(`{"q":"a"}`)},
				{ID: "call_2", Name: "calc", Args: json.RawMessage(`{"expr":"1+1"}`)},
			},
		},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)

	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(req.Messages))
	}

	msg := req.Messages[0]
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "search" {
		t.Errorf("expected first tool call 'search', got %q", msg.ToolCalls[0].Function.Name)
	}
	if msg.ToolCalls[1].Function.Name != "calc" {
		t.Errorf("expected second tool call 'calc', got %q", msg.ToolCalls[1].Function.Name)
	}
}
