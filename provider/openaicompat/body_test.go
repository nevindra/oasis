package openaicompat

import (
	"encoding/json"
	"testing"

	oasis "github.com/nevindra/oasis/core"
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
	if c := req.Messages[0].Content; !c.IsString() || c.String != "You are a helpful assistant." {
		t.Errorf("unexpected system content: %+v", req.Messages[0].Content)
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
	if c := req.Messages[1].Content; !c.IsString() || c.String != "Hello!" {
		t.Errorf("unexpected assistant content: %+v", req.Messages[1].Content)
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
	if c := assistantMsg.Content; !c.IsString() || c.String != "Let me search for that." {
		t.Errorf("unexpected content: %+v", assistantMsg.Content)
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
	if c := msg.Content; !c.IsString() || c.String != "Found 10 results about cats" {
		t.Errorf("unexpected content: %+v", msg.Content)
	}
	if msg.ToolCallID != "call_123" {
		t.Errorf("expected tool_call_id 'call_123', got %q", msg.ToolCallID)
	}
}

func TestBuildBody_ImageInlineData(t *testing.T) {
	messages := []oasis.ChatMessage{
		{
			Role:    "user",
			Content: "What is this?",
			Attachments: []oasis.Attachment{
				{MimeType: "image/png", Data: []byte("raw-png")},
			},
		},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)

	msg := req.Messages[0]
	if !msg.Content.IsBlocks() {
		t.Fatalf("expected content to be blocks, got %+v", msg.Content)
	}
	blocks := msg.Content.Blocks
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks (text + image), got %d", len(blocks))
	}

	if blocks[0].Type != "text" {
		t.Errorf("expected first block type 'text', got %q", blocks[0].Type)
	}
	if blocks[1].Type != "image_url" {
		t.Errorf("expected second block type 'image_url', got %q", blocks[1].Type)
	}
	if blocks[1].ImageURL == nil {
		t.Fatal("expected image_url to be non-nil")
	}
	expectedURL := "data:image/png;base64,cmF3LXBuZw==" // base64("raw-png")
	if blocks[1].ImageURL.URL != expectedURL {
		t.Errorf("expected URL %q, got %q", expectedURL, blocks[1].ImageURL.URL)
	}
}

func TestBuildBody_ImageURL(t *testing.T) {
	messages := []oasis.ChatMessage{
		{
			Role:    "user",
			Content: "What is this?",
			Attachments: []oasis.Attachment{
				{MimeType: "image/png", URL: "https://example.com/photo.png"},
			},
		},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)
	blocks := req.Messages[0].Content.Blocks

	if blocks[1].Type != "image_url" {
		t.Errorf("expected 'image_url', got %q", blocks[1].Type)
	}
	if blocks[1].ImageURL.URL != "https://example.com/photo.png" {
		t.Errorf("expected direct URL, got %q", blocks[1].ImageURL.URL)
	}
}

func TestBuildBody_VideoFile(t *testing.T) {
	messages := []oasis.ChatMessage{
		{
			Role:    "user",
			Content: "Describe this video",
			Attachments: []oasis.Attachment{
				{MimeType: "video/mp4", URL: "https://example.com/clip.mp4"},
			},
		},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)
	blocks := req.Messages[0].Content.Blocks

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[1].Type != "file" {
		t.Errorf("expected 'file' block for video, got %q", blocks[1].Type)
	}
	if blocks[1].File == nil {
		t.Fatal("expected File to be non-nil")
	}
	if blocks[1].File.URL != "https://example.com/clip.mp4" {
		t.Errorf("expected video URL, got %q", blocks[1].File.URL)
	}
}

func TestBuildBody_InlineBase64Attachment(t *testing.T) {
	messages := []oasis.ChatMessage{
		{
			Role:    "user",
			Content: "What is this?",
			Attachments: []oasis.Attachment{
				mustAttachmentBase64(t, "image/png", "aGVsbG8="),
			},
		},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)
	blocks := req.Messages[0].Content.Blocks

	if blocks[1].Type != "image_url" {
		t.Errorf("expected 'image_url' for base64 attachment, got %q", blocks[1].Type)
	}
	if blocks[1].ImageURL == nil {
		t.Fatal("expected image_url to be non-nil")
	}
}

// mustAttachmentBase64 fails the test if base64 decode fails. Used to keep
// test data readable while still routing through the validating constructor.
func mustAttachmentBase64(t *testing.T, mime, encoded string) oasis.Attachment {
	t.Helper()
	att, err := oasis.NewAttachmentFromBase64(mime, encoded)
	if err != nil {
		t.Fatalf("decode test attachment: %v", err)
	}
	return att
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

func TestBuildBody_WithOptions(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil,
		WithTemperature(0.3),
		WithTopP(0.9),
		WithMaxTokens(1024),
		WithFrequencyPenalty(0.5),
		WithPresencePenalty(0.2),
		WithStop("END", "STOP"),
		WithSeed(42),
	)

	if req.Temperature == nil || *req.Temperature != 0.3 {
		t.Errorf("expected temperature 0.3, got %v", req.Temperature)
	}
	if req.TopP == nil || *req.TopP != 0.9 {
		t.Errorf("expected topP 0.9, got %v", req.TopP)
	}
	if req.MaxTokens != 1024 {
		t.Errorf("expected maxTokens 1024, got %d", req.MaxTokens)
	}
	if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.5 {
		t.Errorf("expected frequencyPenalty 0.5, got %v", req.FrequencyPenalty)
	}
	if req.PresencePenalty == nil || *req.PresencePenalty != 0.2 {
		t.Errorf("expected presencePenalty 0.2, got %v", req.PresencePenalty)
	}
	if len(req.Stop) != 2 || req.Stop[0] != "END" || req.Stop[1] != "STOP" {
		t.Errorf("expected stop [END, STOP], got %v", req.Stop)
	}
	if req.Seed == nil || *req.Seed != 42 {
		t.Errorf("expected seed 42, got %v", req.Seed)
	}
}

func TestBuildBody_WithToolChoice(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}
	tools := []oasis.ToolDefinition{
		{Name: "search", Description: "Search", Parameters: json.RawMessage(`{"type":"object"}`)},
	}

	req := BuildBody(messages, tools, "gpt-4o", nil,
		WithToolChoice(ToolChoiceModeValue(ToolChoiceRequired)),
	)

	if req.ToolChoice == nil || req.ToolChoice.Mode != ToolChoiceRequired {
		t.Errorf("expected toolChoice 'required', got %+v", req.ToolChoice)
	}

	// Wire shape: a string-mode choice must marshal to a bare JSON string.
	data, err := json.Marshal(req.ToolChoice)
	if err != nil {
		t.Fatalf("marshal tool_choice: %v", err)
	}
	if string(data) != `"required"` {
		t.Errorf("expected tool_choice wire %q, got %s", `"required"`, data)
	}
}

func TestToolChoice_WireRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		tc   ToolChoice
		wire string
	}{
		{"none", ToolChoiceModeValue(ToolChoiceNone), `"none"`},
		{"auto", ToolChoiceModeValue(ToolChoiceAuto), `"auto"`},
		{"required", ToolChoiceModeValue(ToolChoiceRequired), `"required"`},
		{"function", ToolChoiceFunction("get_weather"), `{"type":"function","function":{"name":"get_weather"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.tc)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(data) != tc.wire {
				t.Fatalf("wire mismatch: got %s, want %s", data, tc.wire)
			}
			var back ToolChoice
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if back != tc.tc {
				t.Fatalf("round-trip mismatch: got %+v, want %+v", back, tc.tc)
			}
		})
	}

	// Omitted when nil: the *ToolChoice field carries omitempty.
	req := ChatRequest{Model: "m"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := parsed["tool_choice"]; ok {
		t.Errorf("expected tool_choice omitted when nil, got %s", data)
	}
}

func TestBuildBody_NoOptions(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)

	// No options set: pointer fields should be nil.
	if req.Temperature != nil {
		t.Errorf("expected nil temperature, got %v", req.Temperature)
	}
	if req.TopP != nil {
		t.Errorf("expected nil topP, got %v", req.TopP)
	}
	if req.FrequencyPenalty != nil {
		t.Errorf("expected nil frequencyPenalty, got %v", req.FrequencyPenalty)
	}
	if req.PresencePenalty != nil {
		t.Errorf("expected nil presencePenalty, got %v", req.PresencePenalty)
	}
	if req.Seed != nil {
		t.Errorf("expected nil seed, got %v", req.Seed)
	}
}

// --- CacheCheckpoint tests ---

func TestBuildBody_CacheCheckpointStringContent(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "system", Content: "You are helpful.", CacheCheckpoint: true},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)

	msg := req.Messages[0]
	if !msg.Content.IsBlocks() {
		t.Fatalf("expected blocks for CacheCheckpoint string message, got %+v", msg.Content)
	}
	blocks := msg.Content.Blocks
	if len(blocks) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(blocks))
	}
	if blocks[0].Type != "text" {
		t.Errorf("expected block type 'text', got %q", blocks[0].Type)
	}
	if blocks[0].Text != "You are helpful." {
		t.Errorf("expected text 'You are helpful.', got %q", blocks[0].Text)
	}
	if blocks[0].CacheControl == nil {
		t.Fatal("expected CacheControl to be set on the text block")
	}
	if blocks[0].CacheControl.Type != "ephemeral" {
		t.Errorf("expected CacheControl.Type 'ephemeral', got %q", blocks[0].CacheControl.Type)
	}
}

func TestBuildBody_CacheCheckpointBlockContent(t *testing.T) {
	// Message with attachments already produces []ContentBlock; CacheCheckpoint
	// should set cache_control on the LAST block only.
	messages := []oasis.ChatMessage{
		{
			Role:    "user",
			Content: "Describe this image",
			Attachments: []oasis.Attachment{
				{MimeType: "image/png", URL: "https://example.com/img.png"},
			},
			CacheCheckpoint: true,
		},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)

	if !req.Messages[0].Content.IsBlocks() {
		t.Fatalf("expected blocks, got %+v", req.Messages[0].Content)
	}
	blocks := req.Messages[0].Content.Blocks
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (text + image), got %d", len(blocks))
	}
	// First block (text) must NOT have cache_control.
	if blocks[0].CacheControl != nil {
		t.Errorf("expected no CacheControl on first block, got %+v", blocks[0].CacheControl)
	}
	// Last block (image) must have cache_control.
	if blocks[1].CacheControl == nil {
		t.Fatal("expected CacheControl on last block, got nil")
	}
	if blocks[1].CacheControl.Type != "ephemeral" {
		t.Errorf("expected CacheControl.Type 'ephemeral', got %q", blocks[1].CacheControl.Type)
	}
}

func TestBuildBody_CacheCheckpointFalse(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello", CacheCheckpoint: false},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil)

	msg := req.Messages[0]
	// Content should remain a plain string — no block promotion.
	if msg.Content.IsBlocks() {
		t.Error("expected plain string content when CacheCheckpoint is false, got blocks")
	}
	if !msg.Content.IsString() || msg.Content.String != "Hello" {
		t.Errorf("expected content 'Hello', got %+v", msg.Content)
	}
}

func TestBuildBody_CacheCheckpointComposesWithOption(t *testing.T) {
	// CacheCheckpoint=true on index 0, plus WithCacheControl(0) for the same
	// index. The result should have exactly one cache_control marker — not two.
	messages := []oasis.ChatMessage{
		{Role: "system", Content: "Be concise.", CacheCheckpoint: true},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil, WithCacheControl(0))

	if !req.Messages[0].Content.IsBlocks() {
		t.Fatalf("expected blocks, got %+v", req.Messages[0].Content)
	}
	blocks := req.Messages[0].Content.Blocks
	if len(blocks) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(blocks))
	}
	if blocks[0].CacheControl == nil {
		t.Fatal("expected CacheControl to be set")
	}
	if blocks[0].CacheControl.Type != "ephemeral" {
		t.Errorf("expected CacheControl.Type 'ephemeral', got %q", blocks[0].CacheControl.Type)
	}
	// Verify it's still a single block (no duplication).
	if len(blocks) > 1 {
		t.Errorf("expected exactly 1 block (idempotent), got %d", len(blocks))
	}
}

func TestBuildBody_OptionsJSONRoundTrip(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	req := BuildBody(messages, nil, "gpt-4o", nil,
		WithTemperature(0.0),
		WithTopP(1.0),
		WithMaxTokens(500),
		WithSeed(123),
	)

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	// Temperature 0.0 should be present (pointer, not omitted).
	if parsed["temperature"] != 0.0 {
		t.Errorf("expected temperature 0.0 in JSON, got %v", parsed["temperature"])
	}
	if parsed["top_p"] != 1.0 {
		t.Errorf("expected top_p 1.0 in JSON, got %v", parsed["top_p"])
	}
	if parsed["max_tokens"] != 500.0 {
		t.Errorf("expected max_tokens 500 in JSON, got %v", parsed["max_tokens"])
	}
	if parsed["seed"] != 123.0 {
		t.Errorf("expected seed 123 in JSON, got %v", parsed["seed"])
	}

	// Fields not set should be absent.
	if _, ok := parsed["frequency_penalty"]; ok {
		t.Error("expected frequency_penalty to be omitted")
	}
	if _, ok := parsed["presence_penalty"]; ok {
		t.Error("expected presence_penalty to be omitted")
	}
}

func TestContentBlock_TextTypeAlwaysHasTextField(t *testing.T) {
	// A text block with empty text MUST still emit "text":"" — omitempty would
	// drop it and produce an invalid {"type":"text"} part that providers reject
	// ("Expected 'text' field in text type content part to be a string").
	raw, err := json.Marshal(ContentBlock{Type: "text", Text: ""})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	txt, ok := m["text"]
	if !ok {
		t.Fatalf("text block must include a \"text\" field; got %s", raw)
	}
	if _, isStr := txt.(string); !isStr {
		t.Fatalf("\"text\" must be a string, got %T; raw=%s", txt, raw)
	}

	// Non-text blocks keep "text" omitted.
	raw2, _ := json.Marshal(ContentBlock{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,AAAA"}})
	var m2 map[string]any
	_ = json.Unmarshal(raw2, &m2)
	if _, ok := m2["text"]; ok {
		t.Fatalf("image_url block should omit \"text\"; got %s", raw2)
	}
}

func TestBuildBody_EmptyContentCacheCheckpointKeepsTextField(t *testing.T) {
	// Regression: an empty-content message marked as a cache checkpoint was
	// promoted to a {"type":"text"} block whose empty text got dropped by
	// omitempty, yielding an invalid content part (HTTP 400 from providers).
	messages := []oasis.ChatMessage{
		{Role: "tool", Content: "", ToolCallID: "call_1", CacheCheckpoint: true},
	}
	req := BuildBody(messages, nil, "gpt-4o", nil)
	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(req.Messages))
	}

	raw, err := json.Marshal(req.Messages[0].Content)
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("checkpointed content should be a content-block array; got %s", raw)
	}
	if len(blocks) == 0 {
		t.Fatalf("expected at least one content block; got %s", raw)
	}
	for _, blk := range blocks {
		if blk["type"] != "text" {
			continue
		}
		if _, ok := blk["text"].(string); !ok {
			t.Fatalf("text block must carry a string \"text\" field; got %s", raw)
		}
	}
}

func TestMessageContent_WireShape(t *testing.T) {
	// String content must stay a JSON string; block content must stay a JSON array.
	strRaw, err := json.Marshal(StringContent("hello"))
	if err != nil {
		t.Fatalf("marshal string content: %v", err)
	}
	if string(strRaw) != `"hello"` {
		t.Fatalf("string content wire = %s, want %q", strRaw, `"hello"`)
	}

	blkRaw, err := json.Marshal(BlockContent([]ContentBlock{{Type: "text", Text: "hi"}}))
	if err != nil {
		t.Fatalf("marshal block content: %v", err)
	}
	if len(blkRaw) == 0 || blkRaw[0] != '[' {
		t.Fatalf("block content must marshal to a JSON array, got %s", blkRaw)
	}

	// The zero value (unset Content) marshals to an empty JSON string, matching
	// the historical any-typed default where a missing string was "".
	zeroRaw, err := json.Marshal(MessageContent{})
	if err != nil {
		t.Fatalf("marshal zero content: %v", err)
	}
	if string(zeroRaw) != `""` {
		t.Fatalf("zero content wire = %s, want %q", zeroRaw, `""`)
	}
}

func TestMessageContent_UnmarshalRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   MessageContent
		wire string
	}{
		{"string", StringContent("hello"), `"hello"`},
		{"empty-string", StringContent(""), `""`},
		{
			"blocks",
			BlockContent([]ContentBlock{
				{Type: "text", Text: "describe"},
				{Type: "image_url", ImageURL: &ImageURL{URL: "https://x/y.png"}},
			}),
			`[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"https://x/y.png"}}]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(data) != tc.wire {
				t.Fatalf("wire = %s, want %s", data, tc.wire)
			}
			var back MessageContent
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if back.Kind != tc.in.Kind {
				t.Fatalf("kind = %v, want %v", back.Kind, tc.in.Kind)
			}
			if tc.in.IsString() && back.String != tc.in.String {
				t.Fatalf("string = %q, want %q", back.String, tc.in.String)
			}
			if tc.in.IsBlocks() && len(back.Blocks) != len(tc.in.Blocks) {
				t.Fatalf("blocks len = %d, want %d", len(back.Blocks), len(tc.in.Blocks))
			}
		})
	}

	// A JSON null content unmarshals to the zero (empty-string) value.
	var nullContent MessageContent
	if err := json.Unmarshal([]byte("null"), &nullContent); err != nil {
		t.Fatalf("unmarshal null: %v", err)
	}
	if !nullContent.IsString() || nullContent.String != "" {
		t.Fatalf("null content should be empty string, got %+v", nullContent)
	}
}

// TestMessage_WireUnchanged proves the full Message JSON shape is byte-identical
// to what the previous any-typed Content produced: a string message stays
// {"role":...,"content":"..."} and a block message stays {"content":[...]}.
func TestMessage_WireUnchanged(t *testing.T) {
	strMsg := Message{Role: "user", Content: StringContent("hi")}
	got, err := json.Marshal(strMsg)
	if err != nil {
		t.Fatalf("marshal string message: %v", err)
	}
	if want := `{"role":"user","content":"hi"}`; string(got) != want {
		t.Fatalf("string message wire = %s, want %s", got, want)
	}

	blkMsg := Message{Role: "user", Content: BlockContent([]ContentBlock{{Type: "text", Text: "hi"}})}
	got2, err := json.Marshal(blkMsg)
	if err != nil {
		t.Fatalf("marshal block message: %v", err)
	}
	if want := `{"role":"user","content":[{"type":"text","text":"hi"}]}`; string(got2) != want {
		t.Fatalf("block message wire = %s, want %s", got2, want)
	}
}
