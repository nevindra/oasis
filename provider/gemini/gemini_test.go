package gemini

import (
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis"
)

func TestBuildBody_SystemMessages(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "system", Content: "Be concise."},
		{Role: "user", Content: "Hello"},
	}

	body, err := buildBody(messages, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	// System messages should be extracted to systemInstruction.
	si, ok := body["systemInstruction"].(map[string]any)
	if !ok {
		t.Fatal("expected systemInstruction in body")
	}
	parts, ok := si["parts"].([]map[string]any)
	if !ok || len(parts) != 1 {
		t.Fatal("expected exactly 1 systemInstruction part")
	}
	text, ok := parts[0]["text"].(string)
	if !ok {
		t.Fatal("expected text field in systemInstruction part")
	}
	if text != "You are a helpful assistant.\n\nBe concise." {
		t.Errorf("unexpected system text: %q", text)
	}

	// Contents should only have the user message (no system messages).
	contents, ok := body["contents"].([]map[string]any)
	if !ok {
		t.Fatal("expected contents array in body")
	}
	if len(contents) != 1 {
		t.Fatalf("expected 1 content entry (user only), got %d", len(contents))
	}
	if contents[0]["role"] != "user" {
		t.Errorf("expected role 'user', got %q", contents[0]["role"])
	}
}

func TestBuildBody_AssistantMapsToModel(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "Hello!"},
		{Role: "user", Content: "How are you?"},
	}

	body, err := buildBody(messages, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	contents := body["contents"].([]map[string]any)
	if len(contents) != 3 {
		t.Fatalf("expected 3 content entries, got %d", len(contents))
	}

	// Second message (assistant) should be mapped to "model".
	if contents[1]["role"] != "model" {
		t.Errorf("expected assistant role mapped to 'model', got %q", contents[1]["role"])
	}

	// First and third should remain "user".
	if contents[0]["role"] != "user" {
		t.Errorf("expected first role 'user', got %q", contents[0]["role"])
	}
	if contents[2]["role"] != "user" {
		t.Errorf("expected third role 'user', got %q", contents[2]["role"])
	}
}

func TestBuildBody_ToolResults(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Search for cats"},
		{
			Role: "assistant",
			ToolCalls: []oasis.ToolCall{
				{
					ID:   "search",
					Name: "search",
					Args: json.RawMessage(`{"query":"cats"}`),
				},
			},
		},
		{
			Role:       "tool",
			Content:    "Found 10 results about cats",
			ToolCallID: "search",
		},
	}

	body, err := buildBody(messages, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	contents := body["contents"].([]map[string]any)
	if len(contents) != 3 {
		t.Fatalf("expected 3 content entries, got %d", len(contents))
	}

	// Second entry: assistant with tool calls -> model role with functionCall parts.
	assistantEntry := contents[1]
	if assistantEntry["role"] != "model" {
		t.Errorf("expected tool call entry role 'model', got %q", assistantEntry["role"])
	}
	parts := assistantEntry["parts"].([]map[string]any)
	if len(parts) != 1 {
		t.Fatalf("expected 1 functionCall part, got %d", len(parts))
	}
	fc := parts[0]["functionCall"].(map[string]any)
	if fc["name"] != "search" {
		t.Errorf("expected functionCall name 'search', got %q", fc["name"])
	}

	// Third entry: tool result -> user role with functionResponse.
	toolEntry := contents[2]
	if toolEntry["role"] != "user" {
		t.Errorf("expected tool result role 'user', got %q", toolEntry["role"])
	}
	toolParts := toolEntry["parts"].([]map[string]any)
	if len(toolParts) != 1 {
		t.Fatalf("expected 1 functionResponse part, got %d", len(toolParts))
	}
	fr := toolParts[0]["functionResponse"].(map[string]any)
	if fr["name"] != "search" {
		t.Errorf("expected functionResponse name 'search', got %q", fr["name"])
	}
	resp := fr["response"].(map[string]any)
	if resp["result"] != "Found 10 results about cats" {
		t.Errorf("unexpected functionResponse result: %v", resp["result"])
	}
}

func TestBuildBody_ToolDeclarations(t *testing.T) {
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

	body, err := buildBody(messages, tools, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	toolsField, ok := body["tools"].([]map[string]any)
	if !ok || len(toolsField) != 1 {
		t.Fatal("expected tools array with 1 entry")
	}

	decls, ok := toolsField[0]["functionDeclarations"].([]map[string]any)
	if !ok || len(decls) != 1 {
		t.Fatal("expected 1 function declaration")
	}
	if decls[0]["name"] != "get_weather" {
		t.Errorf("expected declaration name 'get_weather', got %q", decls[0]["name"])
	}
	if decls[0]["description"] != "Get the current weather" {
		t.Errorf("unexpected description: %q", decls[0]["description"])
	}
}

func TestBuildBody_Images(t *testing.T) {
	messages := []oasis.ChatMessage{
		{
			Role:    "user",
			Content: "What is this?",
			Attachments: []oasis.Attachment{
				{MimeType: "image/png", Base64: "iVBOR..."},
			},
		},
	}

	body, err := buildBody(messages, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	contents := body["contents"].([]map[string]any)
	if len(contents) != 1 {
		t.Fatalf("expected 1 content entry, got %d", len(contents))
	}

	parts := contents[0]["parts"].([]map[string]any)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts (text + image), got %d", len(parts))
	}

	// First part should be text.
	if parts[0]["text"] != "What is this?" {
		t.Errorf("expected text part, got %v", parts[0])
	}

	// Second part should be inlineData.
	inlineData, ok := parts[1]["inlineData"].(map[string]any)
	if !ok {
		t.Fatal("expected inlineData part")
	}
	if inlineData["mimeType"] != "image/png" {
		t.Errorf("expected mimeType 'image/png', got %q", inlineData["mimeType"])
	}
	if inlineData["data"] != "iVBOR..." {
		t.Errorf("expected base64 data, got %q", inlineData["data"])
	}
}

func TestBuildBody_EmptyContentGetsFallbackPart(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: ""},
	}

	body, err := buildBody(messages, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	contents := body["contents"].([]map[string]any)
	parts := contents[0]["parts"].([]map[string]any)
	if len(parts) != 1 {
		t.Fatalf("expected 1 fallback part, got %d", len(parts))
	}
	if parts[0]["text"] != "" {
		t.Errorf("expected empty text fallback, got %v", parts[0])
	}
}

func TestBuildBody_GenerationConfig(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	body, err := buildBody(messages, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	gc, ok := body["generationConfig"].(map[string]any)
	if !ok {
		t.Fatal("expected generationConfig in body")
	}
	temp, ok := gc["temperature"].(float64)
	if !ok || temp != 1.0 {
		t.Errorf("expected temperature 1.0, got %v", gc["temperature"])
	}
}

func TestBuildBody_ThoughtSignaturePreserved(t *testing.T) {
	meta, _ := json.Marshal(map[string]string{
		"thoughtSignature": "abc123sig",
	})

	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Think about this"},
		{
			Role: "assistant",
			ToolCalls: []oasis.ToolCall{
				{
					ID:       "search",
					Name:     "search",
					Args:     json.RawMessage(`{"q":"test"}`),
					Metadata: meta,
				},
			},
		},
	}

	body, err := buildBody(messages, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	contents := body["contents"].([]map[string]any)
	if len(contents) != 2 {
		t.Fatalf("expected 2 content entries, got %d", len(contents))
	}

	parts := contents[1]["parts"].([]map[string]any)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	sig, ok := parts[0]["thoughtSignature"].(string)
	if !ok || sig != "abc123sig" {
		t.Errorf("expected thoughtSignature 'abc123sig', got %v", parts[0]["thoughtSignature"])
	}
}

func TestMapRole(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"user", "user"},
		{"assistant", "model"},
		{"system", "system"},
		{"tool", "tool"},
	}

	for _, tt := range tests {
		got := mapRole(tt.input)
		if got != tt.expected {
			t.Errorf("mapRole(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestIsCompleteJSON(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{`{"key": "value"}`, true},
		{`{"key": "val`, false},
		{`{"nested": {"a": 1}}`, true},
		{`[1, 2, 3]`, true},
		{`[1, 2`, false},
		{`{"key": "value with \" escape"}`, true},
		{`{"key": "value with { brace"}`, true},
		{``, true}, // empty is balanced (depth 0)
	}

	for _, tt := range tests {
		got := isCompleteJSON(tt.input)
		if got != tt.expected {
			t.Errorf("isCompleteJSON(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

func TestBuildBody_NoSystemInstruction(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	body, err := buildBody(messages, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	if _, ok := body["systemInstruction"]; ok {
		t.Error("expected no systemInstruction when there are no system messages")
	}
}

func TestBuildBody_NoToolsOmitted(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	body, err := buildBody(messages, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	if _, ok := body["tools"]; ok {
		t.Error("expected no tools field when tools slice is nil")
	}
}

func TestBuildBody_MultipleToolCalls(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Search and calculate"},
		{
			Role: "assistant",
			ToolCalls: []oasis.ToolCall{
				{ID: "search", Name: "search", Args: json.RawMessage(`{"q":"test"}`)},
				{ID: "calc", Name: "calc", Args: json.RawMessage(`{"expr":"1+1"}`)},
			},
		},
	}

	body, err := buildBody(messages, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	contents := body["contents"].([]map[string]any)
	if len(contents) != 2 {
		t.Fatalf("expected 2 content entries, got %d", len(contents))
	}

	parts := contents[1]["parts"].([]map[string]any)
	if len(parts) != 2 {
		t.Fatalf("expected 2 functionCall parts, got %d", len(parts))
	}

	fc0 := parts[0]["functionCall"].(map[string]any)
	fc1 := parts[1]["functionCall"].(map[string]any)
	if fc0["name"] != "search" {
		t.Errorf("expected first functionCall name 'search', got %q", fc0["name"])
	}
	if fc1["name"] != "calc" {
		t.Errorf("expected second functionCall name 'calc', got %q", fc1["name"])
	}
}

func TestNewConstructors(t *testing.T) {
	g := New("test-key", "gemini-2.0-flash")
	if g.apiKey != "test-key" {
		t.Errorf("expected apiKey 'test-key', got %q", g.apiKey)
	}
	if g.model != "gemini-2.0-flash" {
		t.Errorf("expected model 'gemini-2.0-flash', got %q", g.model)
	}
	if g.Name() != "gemini" {
		t.Errorf("expected name 'gemini', got %q", g.Name())
	}

	e := NewEmbedding("embed-key", "text-embedding-004", 768)
	if e.apiKey != "embed-key" {
		t.Errorf("expected apiKey 'embed-key', got %q", e.apiKey)
	}
	if e.model != "text-embedding-004" {
		t.Errorf("expected model 'text-embedding-004', got %q", e.model)
	}
	if e.Dimensions() != 768 {
		t.Errorf("expected dimensions 768, got %d", e.Dimensions())
	}
	if e.Name() != "gemini" {
		t.Errorf("expected name 'gemini', got %q", e.Name())
	}
}

// TestBuildBody_JSONRoundTrip verifies that the body can be marshaled to valid JSON.
func TestBuildBody_JSONRoundTrip(t *testing.T) {
	messages := []oasis.ChatMessage{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
		{
			Role: "user",
			Content: "Search for something",
		},
		{
			Role: "assistant",
			ToolCalls: []oasis.ToolCall{
				{ID: "search", Name: "search", Args: json.RawMessage(`{"q":"something"}`)},
			},
		},
		{Role: "tool", Content: "results here", ToolCallID: "search"},
	}
	tools := []oasis.ToolDefinition{
		{Name: "search", Description: "Search the web", Parameters: json.RawMessage(`{"type":"object"}`)},
	}

	body, err := buildBody(messages, tools, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("failed to marshal body to JSON: %v", err)
	}

	// Verify it can be parsed back.
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to parse round-tripped JSON: %v", err)
	}

	// Verify key fields exist.
	if _, ok := parsed["contents"]; !ok {
		t.Error("missing 'contents' in round-tripped JSON")
	}
	if _, ok := parsed["systemInstruction"]; !ok {
		t.Error("missing 'systemInstruction' in round-tripped JSON")
	}
	if _, ok := parsed["tools"]; !ok {
		t.Error("missing 'tools' in round-tripped JSON")
	}
	if _, ok := parsed["generationConfig"]; !ok {
		t.Error("missing 'generationConfig' in round-tripped JSON")
	}
}
