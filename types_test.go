package oasis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
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

// --- Attachment helper tests ---

func TestAttachment_InlineData_FromData(t *testing.T) {
	att := Attachment{MimeType: "image/png", Data: []byte("raw-bytes")}
	got := att.InlineData()
	if string(got) != "raw-bytes" {
		t.Errorf("InlineData() = %q, want %q", got, "raw-bytes")
	}
}

func TestAttachment_InlineData_FromBase64(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("legacy-data"))
	att := Attachment{MimeType: "image/png", Base64: encoded}
	got := att.InlineData()
	if string(got) != "legacy-data" {
		t.Errorf("InlineData() = %q, want %q", got, "legacy-data")
	}
}

func TestAttachment_InlineData_DataTakesPriority(t *testing.T) {
	att := Attachment{
		MimeType: "image/png",
		Data:     []byte("preferred"),
		Base64:   base64.StdEncoding.EncodeToString([]byte("ignored")),
	}
	got := att.InlineData()
	if string(got) != "preferred" {
		t.Errorf("InlineData() = %q, want %q (Data should take priority)", got, "preferred")
	}
}

func TestAttachment_InlineData_URLOnly(t *testing.T) {
	att := Attachment{MimeType: "video/mp4", URL: "https://example.com/video.mp4"}
	if got := att.InlineData(); got != nil {
		t.Errorf("InlineData() = %v, want nil for URL-only attachment", got)
	}
}

func TestAttachment_HasInlineData(t *testing.T) {
	tests := []struct {
		name string
		att  Attachment
		want bool
	}{
		{"Data set", Attachment{Data: []byte("x")}, true},
		{"Base64 set", Attachment{Base64: "abc"}, true},
		{"URL only", Attachment{URL: "https://example.com"}, false},
		{"empty", Attachment{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.att.HasInlineData(); got != tt.want {
				t.Errorf("HasInlineData() = %v, want %v", got, tt.want)
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

// --- ParseRetryAfter tests ---

func TestParseRetryAfter_Seconds(t *testing.T) {
	got := ParseRetryAfter("120")
	want := 120 * time.Second
	if got != want {
		t.Errorf("ParseRetryAfter(%q) = %v, want %v", "120", got, want)
	}
}

func TestParseRetryAfter_Zero(t *testing.T) {
	if got := ParseRetryAfter("0"); got != 0 {
		t.Errorf("ParseRetryAfter(%q) = %v, want 0", "0", got)
	}
}

func TestParseRetryAfter_Empty(t *testing.T) {
	if got := ParseRetryAfter(""); got != 0 {
		t.Errorf("ParseRetryAfter(%q) = %v, want 0", "", got)
	}
}

func TestParseRetryAfter_InvalidString(t *testing.T) {
	if got := ParseRetryAfter("not-a-number"); got != 0 {
		t.Errorf("ParseRetryAfter(%q) = %v, want 0", "not-a-number", got)
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

// fakeMultimodalEmb is a test double for MultimodalEmbeddingProvider.
type fakeMultimodalEmb struct{}

func (fakeMultimodalEmb) EmbedMultimodal(_ context.Context, inputs []MultimodalInput) ([][]float32, error) {
	vecs := make([][]float32, len(inputs))
	for i := range inputs {
		vecs[i] = []float32{0.1, 0.2, 0.3}
	}
	return vecs, nil
}

func TestMultimodalEmbeddingProvider_TypeAssertion(t *testing.T) {
	var emb any = fakeMultimodalEmb{}
	mp, ok := emb.(MultimodalEmbeddingProvider)
	if !ok {
		t.Fatal("expected fakeMultimodalEmb to implement MultimodalEmbeddingProvider")
	}
	vecs, err := mp.EmbedMultimodal(context.Background(), []MultimodalInput{
		{Text: "black shirt"},
		{Attachments: []Attachment{{MimeType: "image/jpeg", Data: []byte{0xFF}}}},
		{Text: "describe", Attachments: []Attachment{{MimeType: "image/png", Data: []byte{0x89}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vecs))
	}
}

type fakeBlobStore struct {
	data map[string][]byte
}

func (f *fakeBlobStore) StoreBlob(_ context.Context, key string, data []byte, _ string) (string, error) {
	f.data[key] = data
	return "blob://" + key, nil
}

func (f *fakeBlobStore) GetBlob(_ context.Context, ref string) ([]byte, string, error) {
	key := ref[len("blob://"):]
	return f.data[key], "image/png", nil
}

func (f *fakeBlobStore) DeleteBlob(_ context.Context, ref string) error {
	key := ref[len("blob://"):]
	delete(f.data, key)
	return nil
}

func TestChunkMeta_ContentType_JSON(t *testing.T) {
	meta := ChunkMeta{
		ContentType: "image",
		PageNumber:  1,
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ChunkMeta
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ContentType != "image" {
		t.Errorf("expected content_type 'image', got %q", decoded.ContentType)
	}

	// Zero value preserves backward compatibility (omitempty).
	metaEmpty := ChunkMeta{PageNumber: 1}
	data, _ = json.Marshal(metaEmpty)
	if strings.Contains(string(data), "content_type") {
		t.Error("expected content_type to be omitted when empty")
	}
}

func TestBlobStore_TypeAssertion(t *testing.T) {
	var s any = &fakeBlobStore{data: make(map[string][]byte)}
	bs, ok := s.(BlobStore)
	if !ok {
		t.Fatal("expected fakeBlobStore to implement BlobStore")
	}
	ref, err := bs.StoreBlob(context.Background(), "img-1", []byte{0x89, 0x50}, "image/png")
	if err != nil {
		t.Fatalf("StoreBlob: %v", err)
	}
	data, mime, err := bs.GetBlob(context.Background(), ref)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if mime != "image/png" {
		t.Errorf("expected image/png, got %s", mime)
	}
	if len(data) != 2 {
		t.Errorf("expected 2 bytes, got %d", len(data))
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
