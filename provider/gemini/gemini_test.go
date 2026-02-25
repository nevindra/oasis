package gemini

import (
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis"
)

// testGemini returns a Gemini instance with default config for testing buildBody.
func testGemini() *Gemini {
	return New("test-key", "test-model")
}

func TestBuildBody_SystemMessages(t *testing.T) {
	g := testGemini()
	messages := []oasis.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "system", Content: "Be concise."},
		{Role: "user", Content: "Hello"},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
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
	g := testGemini()
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "Hello!"},
		{Role: "user", Content: "How are you?"},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
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
	g := testGemini()
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

	body, err := g.buildBody(messages, nil, nil, nil)
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
	g := testGemini()
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

	body, err := g.buildBody(messages, tools, nil, nil)
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

func TestBuildBody_InlineData(t *testing.T) {
	g := testGemini()
	messages := []oasis.ChatMessage{
		{
			Role:    "user",
			Content: "What is this?",
			Attachments: []oasis.Attachment{
				{MimeType: "image/png", Data: []byte("raw-png-bytes")},
			},
		},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
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

	if parts[0]["text"] != "What is this?" {
		t.Errorf("expected text part, got %v", parts[0])
	}

	inlineData, ok := parts[1]["inlineData"].(map[string]any)
	if !ok {
		t.Fatal("expected inlineData part")
	}
	if inlineData["mimeType"] != "image/png" {
		t.Errorf("expected mimeType 'image/png', got %q", inlineData["mimeType"])
	}
	wantB64 := "cmF3LXBuZy1ieXRlcw==" // base64("raw-png-bytes")
	if inlineData["data"] != wantB64 {
		t.Errorf("expected base64 %q, got %q", wantB64, inlineData["data"])
	}
}

func TestBuildBody_URLAttachment(t *testing.T) {
	g := testGemini()
	messages := []oasis.ChatMessage{
		{
			Role:    "user",
			Content: "Describe this video",
			Attachments: []oasis.Attachment{
				{MimeType: "video/mp4", URL: "gs://bucket/video.mp4"},
			},
		},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	contents := body["contents"].([]map[string]any)
	parts := contents[0]["parts"].([]map[string]any)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts (text + fileData), got %d", len(parts))
	}

	fileData, ok := parts[1]["fileData"].(map[string]any)
	if !ok {
		t.Fatal("expected fileData part")
	}
	if fileData["mimeType"] != "video/mp4" {
		t.Errorf("expected mimeType 'video/mp4', got %q", fileData["mimeType"])
	}
	if fileData["fileUri"] != "gs://bucket/video.mp4" {
		t.Errorf("expected fileUri, got %q", fileData["fileUri"])
	}
}

func TestBuildBody_DeprecatedBase64(t *testing.T) {
	g := testGemini()
	messages := []oasis.ChatMessage{
		{
			Role:    "user",
			Content: "What is this?",
			Attachments: []oasis.Attachment{
				{MimeType: "image/png", Base64: "iVBOR..."},
			},
		},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	contents := body["contents"].([]map[string]any)
	parts := contents[0]["parts"].([]map[string]any)

	inlineData, ok := parts[1]["inlineData"].(map[string]any)
	if !ok {
		t.Fatal("expected inlineData part for deprecated Base64")
	}
	if inlineData["mimeType"] != "image/png" {
		t.Errorf("expected mimeType 'image/png', got %q", inlineData["mimeType"])
	}
}

func TestBuildBody_EmptyContentGetsFallbackPart(t *testing.T) {
	g := testGemini()
	messages := []oasis.ChatMessage{
		{Role: "user", Content: ""},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
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
	g := testGemini()
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	gc, ok := body["generationConfig"].(map[string]any)
	if !ok {
		t.Fatal("expected generationConfig in body")
	}

	// Default temperature should be 0.1.
	temp, ok := gc["temperature"].(float64)
	if !ok || temp != 0.1 {
		t.Errorf("expected temperature 0.1, got %v", gc["temperature"])
	}

	// Default topP should be 0.9.
	topP, ok := gc["topP"].(float64)
	if !ok || topP != 0.9 {
		t.Errorf("expected topP 0.9, got %v", gc["topP"])
	}

	// mediaResolution omitted by default.
	if _, ok := gc["mediaResolution"]; ok {
		t.Error("expected no mediaResolution when not explicitly set")
	}

	// responseModalities omitted by default.
	if _, ok := gc["responseModalities"]; ok {
		t.Error("expected no responseModalities when not explicitly set")
	}

	// thinkingConfig omitted by default (thinking disabled).
	if _, ok := gc["thinkingConfig"]; ok {
		t.Error("expected no thinkingConfig when thinking is disabled")
	}
}

func TestBuildBody_GenerationConfigWithOptions(t *testing.T) {
	g := New("key", "model",
		WithTemperature(0.7),
		WithTopP(0.95),
		WithMediaResolution("MEDIA_RESOLUTION_HIGH"),
		WithThinking(true),
	)
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	gc := body["generationConfig"].(map[string]any)
	if gc["temperature"] != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", gc["temperature"])
	}
	if gc["topP"] != 0.95 {
		t.Errorf("expected topP 0.95, got %v", gc["topP"])
	}
	if gc["mediaResolution"] != "MEDIA_RESOLUTION_HIGH" {
		t.Errorf("expected MEDIA_RESOLUTION_HIGH, got %v", gc["mediaResolution"])
	}

	// Thinking enabled: thinkingConfig should have budget -1.
	tc, ok := gc["thinkingConfig"].(map[string]any)
	if !ok {
		t.Fatal("expected thinkingConfig when thinking is enabled")
	}
	if tc["thinkingBudget"] != -1 {
		t.Errorf("expected thinkingBudget -1, got %v", tc["thinkingBudget"])
	}
}

func TestBuildBody_ImageGeneration(t *testing.T) {
	g := New("key", "gemini-2.0-flash-exp-image-generation",
		WithResponseModalities("TEXT", "IMAGE"),
	)
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Generate an image of a sunset"},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	gc := body["generationConfig"].(map[string]any)

	// responseModalities should be set.
	rm, ok := gc["responseModalities"].([]string)
	if !ok || len(rm) != 2 || rm[0] != "TEXT" || rm[1] != "IMAGE" {
		t.Errorf("expected responseModalities [TEXT IMAGE], got %v", gc["responseModalities"])
	}

	// thinkingConfig should be absent (not supported by image-gen models).
	if _, ok := gc["thinkingConfig"]; ok {
		t.Error("expected no thinkingConfig for image generation")
	}

	// mediaResolution should be absent (not explicitly set).
	if _, ok := gc["mediaResolution"]; ok {
		t.Error("expected no mediaResolution for image generation")
	}
}

func TestBuildBody_ToolConfigDisabledByDefault(t *testing.T) {
	g := testGemini()
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	// With no tools and functionCalling disabled (default), toolConfig should be set to NONE.
	tc, ok := body["toolConfig"].(map[string]any)
	if !ok {
		t.Fatal("expected toolConfig in body when function calling is disabled")
	}
	fc := tc["functionCallingConfig"].(map[string]any)
	if fc["mode"] != "NONE" {
		t.Errorf("expected mode NONE, got %v", fc["mode"])
	}
}

func TestBuildBody_ToolConfigNotSetWithTools(t *testing.T) {
	g := testGemini()
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}
	tools := []oasis.ToolDefinition{
		{Name: "search", Description: "Search", Parameters: json.RawMessage(`{"type":"object"}`)},
	}

	body, err := g.buildBody(messages, tools, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	// When tools are provided, toolConfig should not force NONE.
	if _, ok := body["toolConfig"]; ok {
		t.Error("expected no toolConfig when tools are explicitly provided")
	}
}

func TestBuildBody_AdditionalToolTypes(t *testing.T) {
	g := New("key", "model",
		WithCodeExecution(true),
		WithGoogleSearch(true),
		WithURLContext(true),
	)
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	toolsField, ok := body["tools"].([]map[string]any)
	if !ok {
		t.Fatal("expected tools array when tool types are enabled")
	}
	if len(toolsField) != 3 {
		t.Fatalf("expected 3 tool entries (codeExecution, googleSearch, urlContext), got %d", len(toolsField))
	}

	if _, ok := toolsField[0]["codeExecution"]; !ok {
		t.Error("expected codeExecution tool entry")
	}
	if _, ok := toolsField[1]["googleSearch"]; !ok {
		t.Error("expected googleSearch tool entry")
	}
	if _, ok := toolsField[2]["urlContext"]; !ok {
		t.Error("expected urlContext tool entry")
	}
}

func TestBuildBody_StructuredOutputDisabled(t *testing.T) {
	g := New("key", "model", WithStructuredOutput(false))
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}
	schema := &oasis.ResponseSchema{
		Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
	}

	body, err := g.buildBody(messages, nil, schema, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	gc := body["generationConfig"].(map[string]any)
	if _, ok := gc["responseMimeType"]; ok {
		t.Error("expected no responseMimeType when structured output is disabled")
	}
	if _, ok := gc["responseSchema"]; ok {
		t.Error("expected no responseSchema when structured output is disabled")
	}
}

func TestBuildBody_ThoughtSignaturePreserved(t *testing.T) {
	g := testGemini()
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

	body, err := g.buildBody(messages, nil, nil, nil)
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
	g := testGemini()
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	if _, ok := body["systemInstruction"]; ok {
		t.Error("expected no systemInstruction when there are no system messages")
	}
}

func TestBuildBody_NoToolsOmitted(t *testing.T) {
	g := testGemini()
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Hello"},
	}

	body, err := g.buildBody(messages, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	if _, ok := body["tools"]; ok {
		t.Error("expected no tools field when tools slice is nil")
	}
}

func TestBuildBody_MultipleToolCalls(t *testing.T) {
	g := testGemini()
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

	body, err := g.buildBody(messages, nil, nil, nil)
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

	// Verify default config values.
	if g.temperature != 0.1 {
		t.Errorf("expected default temperature 0.1, got %v", g.temperature)
	}
	if g.topP != 0.9 {
		t.Errorf("expected default topP 0.9, got %v", g.topP)
	}
	if g.mediaResolution != "" {
		t.Errorf("expected default mediaResolution empty, got %q", g.mediaResolution)
	}
	if g.structuredOutput != true {
		t.Error("expected default structuredOutput true")
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

func TestNewWithOptions(t *testing.T) {
	g := New("key", "model",
		WithTemperature(0.5),
		WithTopP(0.8),
		WithMediaResolution("MEDIA_RESOLUTION_LOW"),
		WithResponseModalities("TEXT", "IMAGE"),
		WithThinking(true),
		WithStructuredOutput(false),
		WithCodeExecution(true),
		WithFunctionCalling(true),
		WithGoogleSearch(true),
		WithURLContext(true),
	)

	if g.temperature != 0.5 {
		t.Errorf("expected temperature 0.5, got %v", g.temperature)
	}
	if g.topP != 0.8 {
		t.Errorf("expected topP 0.8, got %v", g.topP)
	}
	if g.mediaResolution != "MEDIA_RESOLUTION_LOW" {
		t.Errorf("expected MEDIA_RESOLUTION_LOW, got %q", g.mediaResolution)
	}
	if len(g.responseModalities) != 2 || g.responseModalities[0] != "TEXT" || g.responseModalities[1] != "IMAGE" {
		t.Errorf("expected responseModalities [TEXT IMAGE], got %v", g.responseModalities)
	}
	if !g.thinkingEnabled {
		t.Error("expected thinkingEnabled true")
	}
	if g.structuredOutput {
		t.Error("expected structuredOutput false")
	}
	if !g.codeExecution {
		t.Error("expected codeExecution true")
	}
	if !g.functionCalling {
		t.Error("expected functionCalling true")
	}
	if !g.googleSearch {
		t.Error("expected googleSearch true")
	}
	if !g.urlContext {
		t.Error("expected urlContext true")
	}
}

func TestExtractAttachmentsFromParsed(t *testing.T) {
	raw := `{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "here"},
					{"inlineData": {"mimeType": "image/png", "data": "abc123"}}
				]
			}
		}]
	}`

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	atts := extractAttachmentsFromParsed(parsed)
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	if atts[0].MimeType != "image/png" {
		t.Errorf("expected mimeType 'image/png', got %q", atts[0].MimeType)
	}
	if len(atts[0].Data) == 0 {
		t.Error("expected Data to be populated")
	}
}

func TestExtractAttachmentsFromParsed_NoAttachments(t *testing.T) {
	raw := `{
		"candidates": [{
			"content": {
				"parts": [{"text": "just text"}]
			}
		}]
	}`

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	atts := extractAttachmentsFromParsed(parsed)
	if len(atts) != 0 {
		t.Fatalf("expected 0 attachments, got %d", len(atts))
	}
}

func TestDoGenerate_InlineDataAttachments(t *testing.T) {
	// Test that geminiPart with inlineData is parsed into ChatResponse.Attachments.
	respJSON := `{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "Here is the image"},
					{"inlineData": {"mimeType": "image/png", "data": "iVBOR..."}}
				],
				"role": "model"
			}
		}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5}
	}`

	var parsed geminiResponse
	if err := json.Unmarshal([]byte(respJSON), &parsed); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(parsed.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(parsed.Candidates))
	}

	parts := parsed.Candidates[0].Content.Parts
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}

	// First part: text
	if parts[0].Text == nil || *parts[0].Text != "Here is the image" {
		t.Errorf("expected text 'Here is the image', got %v", parts[0].Text)
	}

	// Second part: inlineData
	if parts[1].InlineData == nil {
		t.Fatal("expected InlineData in second part")
	}
	if parts[1].InlineData.MimeType != "image/png" {
		t.Errorf("expected mimeType 'image/png', got %q", parts[1].InlineData.MimeType)
	}
	if parts[1].InlineData.Data != "iVBOR..." {
		t.Errorf("expected data 'iVBOR...', got %q", parts[1].InlineData.Data)
	}
}

func TestBuildBody_ResponseSchemaInBody(t *testing.T) {
	g := testGemini()
	messages := []oasis.ChatMessage{
		{Role: "user", Content: "Return JSON"},
	}
	schema := &oasis.ResponseSchema{
		Name:   "output",
		Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
	}

	body, err := g.buildBody(messages, nil, schema, nil)
	if err != nil {
		t.Fatalf("buildBody returned error: %v", err)
	}

	gc := body["generationConfig"].(map[string]any)
	if gc["responseMimeType"] != "application/json" {
		t.Errorf("expected responseMimeType 'application/json', got %v", gc["responseMimeType"])
	}
	if _, ok := gc["responseSchema"]; !ok {
		t.Error("expected responseSchema in generationConfig")
	}
}

// TestBuildBody_JSONRoundTrip verifies that the body can be marshaled to valid JSON.
func TestBuildBody_JSONRoundTrip(t *testing.T) {
	g := testGemini()
	messages := []oasis.ChatMessage{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
		{
			Role:    "user",
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

	body, err := g.buildBody(messages, tools, nil, nil)
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
