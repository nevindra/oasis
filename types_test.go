package oasis

import (
	"context"
	"encoding/json"
	"testing"
)

func TestUserMessage(t *testing.T) {
	msg := UserMessage("hello")
	if msg.Role != "user" {
		t.Errorf("Role = %q, want %q", msg.Role, "user")
	}
	if msg.Content != "hello" {
		t.Errorf("Content = %q, want %q", msg.Content, "hello")
	}
	if msg.ToolCallID != "" {
		t.Errorf("ToolCallID = %q, want empty", msg.ToolCallID)
	}
	if len(msg.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %v, want empty", msg.ToolCalls)
	}
	if len(msg.Attachments) != 0 {
		t.Errorf("Images = %v, want empty", msg.Attachments)
	}
	if msg.Metadata != nil {
		t.Errorf("Metadata = %v, want nil", msg.Metadata)
	}
}

func TestSystemMessage(t *testing.T) {
	msg := SystemMessage("you are helpful")
	if msg.Role != "system" {
		t.Errorf("Role = %q, want %q", msg.Role, "system")
	}
	if msg.Content != "you are helpful" {
		t.Errorf("Content = %q, want %q", msg.Content, "you are helpful")
	}
}

func TestAssistantMessage(t *testing.T) {
	msg := AssistantMessage("sure thing")
	if msg.Role != "assistant" {
		t.Errorf("Role = %q, want %q", msg.Role, "assistant")
	}
	if msg.Content != "sure thing" {
		t.Errorf("Content = %q, want %q", msg.Content, "sure thing")
	}
}

func TestToolResultMessage(t *testing.T) {
	msg := ToolResultMessage("call-123", "result data")
	if msg.Role != "tool" {
		t.Errorf("Role = %q, want %q", msg.Role, "tool")
	}
	if msg.Content != "result data" {
		t.Errorf("Content = %q, want %q", msg.Content, "result data")
	}
	if msg.ToolCallID != "call-123" {
		t.Errorf("ToolCallID = %q, want %q", msg.ToolCallID, "call-123")
	}
}

func TestToolResultMessageFields(t *testing.T) {
	callID := "call-abc"
	content := "tool output"
	msg := ToolResultMessage(callID, content)

	// callID must go to ToolCallID, not Content
	if msg.ToolCallID != callID {
		t.Errorf("ToolCallID = %q, want %q (callID)", msg.ToolCallID, callID)
	}
	if msg.Content == callID {
		t.Error("Content contains callID; callID should only be in ToolCallID")
	}

	// content must go to Content, not ToolCallID
	if msg.Content != content {
		t.Errorf("Content = %q, want %q (content)", msg.Content, content)
	}
	if msg.ToolCallID == content {
		t.Error("ToolCallID contains content; content should only be in Content")
	}
}

func TestChunkFilterConstructors(t *testing.T) {
	tests := []struct {
		name  string
		f     ChunkFilter
		field string
		op    FilterOp
	}{
		{"ByDocument single", ByDocument("doc1"), "document_id", OpIn},
		{"ByDocument multi", ByDocument("doc1", "doc2"), "document_id", OpIn},
		{"BySource", BySource("/tmp/file.pdf"), "source", OpEq},
		{"ByMeta", ByMeta("section_heading", "Introduction"), "meta.section_heading", OpEq},
		{"CreatedAfter", CreatedAfter(1000), "created_at", OpGt},
		{"CreatedBefore", CreatedBefore(2000), "created_at", OpLt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.f.Field != tt.field {
				t.Errorf("Field = %q, want %q", tt.f.Field, tt.field)
			}
			if tt.f.Op != tt.op {
				t.Errorf("Op = %d, want %d", tt.f.Op, tt.op)
			}
		})
	}

	// Verify ByDocument value is []string
	f := ByDocument("a", "b")
	ids, ok := f.Value.([]string)
	if !ok {
		t.Fatalf("ByDocument value type = %T, want []string", f.Value)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("ByDocument value = %v, want [a b]", ids)
	}
}

func TestMessageConstructorsEmpty(t *testing.T) {
	tests := []struct {
		name string
		msg  ChatMessage
		role string
	}{
		{"UserMessage", UserMessage(""), "user"},
		{"SystemMessage", SystemMessage(""), "system"},
		{"AssistantMessage", AssistantMessage(""), "assistant"},
		{"ToolResultMessage", ToolResultMessage("", ""), "tool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.msg.Role != tt.role {
				t.Errorf("%s(\"\").Role = %q, want %q", tt.name, tt.msg.Role, tt.role)
			}
		})
	}
}

// --- Error type tests (from errors_test.go) ---

func TestErrLLMError(t *testing.T) {
	tests := []struct {
		provider string
		message  string
		want     string
	}{
		{"gemini", "rate limited", "gemini: rate limited"},
		{"openai", "context length exceeded", "openai: context length exceeded"},
	}
	for _, tt := range tests {
		e := &ErrLLM{Provider: tt.provider, Message: tt.message}
		if got := e.Error(); got != tt.want {
			t.Errorf("ErrLLM{%q, %q}.Error() = %q, want %q", tt.provider, tt.message, got, tt.want)
		}
	}
}

func TestErrLLMImplementsError(t *testing.T) {
	var _ error = (*ErrLLM)(nil)
}

func TestErrHTTPError(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   string
	}{
		{429, "too many requests", "http 429: too many requests"},
		{500, "internal server error", "http 500: internal server error"},
	}
	for _, tt := range tests {
		e := &ErrHTTP{Status: tt.status, Body: tt.body}
		if got := e.Error(); got != tt.want {
			t.Errorf("ErrHTTP{%d, %q}.Error() = %q, want %q", tt.status, tt.body, got, tt.want)
		}
	}
}

func TestErrHTTPImplementsError(t *testing.T) {
	var _ error = (*ErrHTTP)(nil)
}

func TestErrLLMEmptyFields(t *testing.T) {
	e := &ErrLLM{}
	want := ": "
	if got := e.Error(); got != want {
		t.Errorf("ErrLLM{}.Error() = %q, want %q", got, want)
	}
}

func TestErrHTTPZeroStatus(t *testing.T) {
	e := &ErrHTTP{}
	want := "http 0: "
	if got := e.Error(); got != want {
		t.Errorf("ErrHTTP{}.Error() = %q, want %q", got, want)
	}
}

// --- ID tests (from id_test.go) ---

func TestNewID(t *testing.T) {
	id1 := NewID()
	id2 := NewID()
	if len(id1) != 36 {
		t.Errorf("expected 36 chars (UUIDv7), got %d: %s", len(id1), id1)
	}
	if id1 == id2 {
		t.Error("two IDs should be unique")
	}
	if id1 >= id2 {
		t.Error("sequential UUIDv7s should be time-ordered")
	}
}

// --- Tool registry tests (from tool_test.go) ---

func TestNewResponseSchema(t *testing.T) {
	s := NewResponseSchema("plan", &SchemaObject{
		Type: "object",
		Properties: map[string]*SchemaObject{
			"steps": {
				Type: "array",
				Items: &SchemaObject{
					Type: "object",
					Properties: map[string]*SchemaObject{
						"id":   {Type: "string", Description: "step identifier"},
						"tool": {Type: "string", Enum: []string{"search", "read", "write"}},
					},
					Required: []string{"id", "tool"},
				},
			},
		},
		Required: []string{"steps"},
	})

	if s.Name != "plan" {
		t.Errorf("Name = %q, want %q", s.Name, "plan")
	}
	if len(s.Schema) == 0 {
		t.Fatal("Schema is empty")
	}

	// Roundtrip: unmarshal back and verify structure.
	var got map[string]any
	if err := json.Unmarshal(s.Schema, &got); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if got["type"] != "object" {
		t.Errorf("type = %v, want %q", got["type"], "object")
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties is not an object")
	}
	if _, ok := props["steps"]; !ok {
		t.Error("missing 'steps' in properties")
	}
}

func TestNewResponseSchemaMinimal(t *testing.T) {
	s := NewResponseSchema("out", &SchemaObject{
		Type: "object",
		Properties: map[string]*SchemaObject{
			"name": {Type: "string"},
		},
	})

	var got map[string]any
	if err := json.Unmarshal(s.Schema, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Omitempty fields should not appear.
	if _, ok := got["description"]; ok {
		t.Error("empty description should be omitted")
	}
	if _, ok := got["enum"]; ok {
		t.Error("nil enum should be omitted")
	}
	if _, ok := got["required"]; ok {
		t.Error("nil required should be omitted")
	}
}

func TestToolRegistry(t *testing.T) {
	reg := NewToolRegistry()
	reg.Add(mockTool{})

	defs := reg.AllDefinitions()
	if len(defs) != 1 || defs[0].Name != "greet" {
		t.Fatalf("expected 1 definition 'greet', got %v", defs)
	}

	res, err := reg.Execute(context.Background(), "greet", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hello from greet" {
		t.Errorf("expected 'hello from greet', got %q", res.Content)
	}

	res, _ = reg.Execute(context.Background(), "nonexistent", nil)
	if res.Error == "" {
		t.Error("expected error for unknown tool")
	}
}

func TestToolRegistryEmpty(t *testing.T) {
	reg := NewToolRegistry()

	defs := reg.AllDefinitions()
	if len(defs) != 0 {
		t.Errorf("expected 0 definitions, got %d", len(defs))
	}

	res, _ := reg.Execute(context.Background(), "anything", nil)
	if res.Error == "" {
		t.Error("expected error for empty registry")
	}
}

func TestToolRegistryMultipleTools(t *testing.T) {
	reg := NewToolRegistry()
	reg.Add(mockTool{})
	reg.Add(mockToolCalc{})

	defs := reg.AllDefinitions()
	if len(defs) != 2 {
		t.Fatalf("expected 2 definitions, got %d", len(defs))
	}

	res, err := reg.Execute(context.Background(), "greet", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hello from greet" {
		t.Errorf("greet: got %q", res.Content)
	}

	res, err = reg.Execute(context.Background(), "calc", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "result from calc" {
		t.Errorf("calc: got %q", res.Content)
	}
}

func TestToolRegistryExecuteError(t *testing.T) {
	reg := NewToolRegistry()
	reg.Add(errTool{})

	_, err := reg.Execute(context.Background(), "fail", nil)
	if err == nil {
		t.Fatal("expected error from failing tool")
	}
	if err.Error() != "tool broken" {
		t.Errorf("error = %q, want %q", err.Error(), "tool broken")
	}
}

func TestToolRegistryMultiDefinitionTool(t *testing.T) {
	reg := NewToolRegistry()
	reg.Add(multiTool{})

	defs := reg.AllDefinitions()
	if len(defs) != 2 {
		t.Fatalf("expected 2 definitions, got %d", len(defs))
	}

	res, err := reg.Execute(context.Background(), "read", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "did read" {
		t.Errorf("read: got %q", res.Content)
	}

	res, err = reg.Execute(context.Background(), "write", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "did write" {
		t.Errorf("write: got %q", res.Content)
	}
}
