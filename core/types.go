package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// --- Helpers ---

// NewID generates a globally unique, time-sortable UUIDv7 (RFC 9562).
func NewID() string {
	return uuid.Must(uuid.NewV7()).String()
}

// NowUnix returns current time as Unix seconds.
func NowUnix() int64 {
	return time.Now().Unix()
}

// --- Core interfaces ---

// Provider abstracts the LLM backend.
//
// Two methods handle all interaction patterns:
//   - Chat: blocking request/response. When req.Tools is non-empty, the
//     response may contain ToolCalls that the caller must dispatch.
//   - ChatStream: like Chat but emits StreamEvent values into ch as content
//     is generated. When req.Tools is non-empty, emits EventToolCallDelta
//     events as tool call arguments are generated incrementally. The channel
//     is NOT closed by the provider — the caller owns its lifecycle.
//     Returns the final assembled ChatResponse with complete ToolCalls and Usage.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error)
	Name() string
}

// EmbeddingProvider abstracts text embedding.
type EmbeddingProvider interface {
	// Embed returns embedding vectors for the given texts.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimensions returns the embedding vector size.
	Dimensions() int
	// Name returns the provider name.
	Name() string
}

// MultimodalInput represents an embedding input containing text, images, or both.
// At least one of Text or Attachments must be populated.
type MultimodalInput struct {
	Text        string
	Attachments []Attachment
}

// MultimodalEmbeddingProvider embeds inputs containing text, images, or both
// into a shared vector space. Models like Qwen3-VL-Embedding produce vectors
// where text and images are comparable via cosine similarity, enabling
// cross-modal retrieval (e.g. text query "black shirt" matches a photo).
//
// Implementations that also support text-only embedding should implement
// EmbeddingProvider as well. Discover via type assertion:
//
//	if mp, ok := embProvider.(MultimodalEmbeddingProvider); ok {
//	    vecs, err := mp.EmbedMultimodal(ctx, inputs)
//	}
type MultimodalEmbeddingProvider interface {
	EmbedMultimodal(ctx context.Context, inputs []MultimodalInput) ([][]float32, error)
}

// BlobStore abstracts binary object storage for large assets (images, audio,
// video) that are too large to store inline in metadata JSON.
//
// Implementations may store blobs in PostgreSQL large objects, S3-compatible
// storage (MinIO, SeaweedFS), or the local filesystem.
//
// StoreBlob returns an opaque reference string (e.g. "s3://bucket/key",
// "pglo://12345") that can be stored in ChunkMeta and resolved later via
// GetBlob. The framework does not interpret the reference — it is
// implementation-defined.
type BlobStore interface {
	// StoreBlob stores binary data and returns an opaque reference.
	// mimeType is advisory (e.g. "image/jpeg") and may be stored alongside the blob.
	StoreBlob(ctx context.Context, key string, data []byte, mimeType string) (ref string, err error)
	// GetBlob retrieves binary data by reference.
	GetBlob(ctx context.Context, ref string) (data []byte, mimeType string, err error)
	// DeleteBlob removes a blob by reference.
	DeleteBlob(ctx context.Context, ref string) error
}

// ToolResult is the outcome of a tool execution.
type ToolResult struct {
	Content     string       `json:"content"`
	Error       string       `json:"error,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"` // multimodal content (images, PDFs, etc.) passed to the LLM
}

// ToolRegistry holds all registered atomic tools and dispatches execution.
// Each AnyTool represents exactly one operation; the registry indexes them
// by Name() for O(1) lookup.
type ToolRegistry struct {
	tools []AnyTool
	index map[string]AnyTool // name → AnyTool for O(1) dispatch
}

// NewToolRegistry creates an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{index: make(map[string]AnyTool)}
}

// Add registers a tool, indexed by t.Name().
func (r *ToolRegistry) Add(t AnyTool) {
	r.tools = append(r.tools, t)
	r.index[t.Name()] = t
}

// Remove deletes a tool from the registry by name.
// Returns an error if no tool is registered under the given name.
func (r *ToolRegistry) Remove(name string) error {
	t, ok := r.index[name]
	if !ok {
		return fmt.Errorf("tool %q not registered", name)
	}
	delete(r.index, name)
	filtered := r.tools[:0]
	for _, existing := range r.tools {
		if existing != t {
			filtered = append(filtered, existing)
		}
	}
	r.tools = filtered
	return nil
}

// AllDefinitions returns tool definitions from all registered tools.
func (r *ToolRegistry) AllDefinitions() []ToolDefinition {
	var defs []ToolDefinition
	for _, t := range r.tools {
		defs = append(defs, t.Definition())
	}
	return defs
}

// DeferredDefinitions returns tool definitions whose Parameters schema is
// currently empty — i.e., MCP tools registered with deferred-schema mode that
// haven't been resolved yet. Used by ToolSearch to enumerate candidates.
func (r *ToolRegistry) DeferredDefinitions() []ToolDefinition {
	var out []ToolDefinition
	for _, t := range r.tools {
		d := t.Definition()
		if len(d.Parameters) == 0 {
			out = append(out, d)
		}
	}
	return out
}

// SchemaEnsurer is the optional capability for tools that support deferred
// input-schema loading. ToolRegistry.EnsureSchema invokes EnsureSchema on
// the tool when its current Definition has no Parameters. The MCP client
// (mcpToolWrapper) implements this; users may implement it on their own
// tools to participate in deferred-schema mode.
type SchemaEnsurer interface {
	EnsureSchema(ctx context.Context) error
}

// EnsureSchema lazy-loads the Parameters schema for a deferred tool.
// Tools opt into deferred-schema support by implementing SchemaEnsurer.
//
// No-op for:
//   - Tools whose Definition's Parameters is already non-empty
//   - Tools that do not implement SchemaEnsurer
//
// Returns an error only if the named tool is not registered, or if the
// underlying EnsureSchema call fails.
func (r *ToolRegistry) EnsureSchema(ctx context.Context, name string) error {
	tool, ok := r.index[name]
	if !ok {
		return fmt.Errorf("tool %q not registered", name)
	}
	if len(tool.Definition().Parameters) > 0 {
		return nil
	}
	ensurer, ok := tool.(SchemaEnsurer)
	if !ok {
		return nil
	}
	return ensurer.EnsureSchema(ctx)
}

// Execute dispatches a tool call by name using the pre-built index.
func (r *ToolRegistry) Execute(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	if t, ok := r.index[name]; ok {
		return t.ExecuteRaw(ctx, args)
	}
	return ToolResult{Error: "unknown tool: " + name}, nil
}

// ExecuteStream dispatches a tool call with streaming support. If the resolved
// tool implements StreamingAnyTool and ch is non-nil, it calls ExecuteStream.
// Otherwise falls back to ExecuteRaw.
func (r *ToolRegistry) ExecuteStream(ctx context.Context, name string, args json.RawMessage, ch chan<- StreamEvent) (ToolResult, error) {
	t, ok := r.index[name]
	if !ok {
		return ToolResult{Error: "unknown tool: " + name}, nil
	}
	if ch != nil {
		if st, ok := t.(StreamingAnyTool); ok {
			return st.ExecuteStream(ctx, args, ch)
		}
	}
	return t.ExecuteRaw(ctx, args)
}

// --- LLM protocol types ---

type ChatMessage struct {
	Role        string          `json:"role"` // "system", "user", "assistant", "tool"
	Content     string          `json:"content"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	ToolCalls   []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID  string          `json:"tool_call_id,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"` // provider-specific (e.g. Gemini thoughtSignature)
}

// Attachment represents binary content (image, PDF, audio, video, etc.) sent to a multimodal LLM.
// The MimeType determines how the provider interprets the data.
//
// Populate URL for remote references (pre-uploaded to storage/CDN) or Data for
// transient inline bytes. Providers resolve the best transport: URL > Data > Base64.
type Attachment struct {
	MimeType string `json:"mime_type"`
	URL      string `json:"url,omitempty"`
	Data     []byte `json:"-"`

	// Deprecated: use Data for inline bytes or URL for remote references.
	Base64 string `json:"base64,omitempty"`
}

// InlineData returns raw bytes from whichever inline source is populated.
// Priority: Data > Base64 (decoded). Returns nil if only URL is set.
func (a Attachment) InlineData() []byte {
	if len(a.Data) > 0 {
		return a.Data
	}
	if a.Base64 != "" {
		data, _ := base64.StdEncoding.DecodeString(a.Base64)
		return data
	}
	return nil
}

// HasInlineData reports whether inline bytes are available (Data or Base64).
func (a Attachment) HasInlineData() bool {
	return len(a.Data) > 0 || a.Base64 != ""
}

type ToolCall struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Args     json.RawMessage `json:"args"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// ResponseSchema tells the provider to enforce structured JSON output.
// When set on a ChatRequest, the provider translates it to its native
// structured output mechanism (e.g. Gemini responseSchema, OpenAI response_format).
type ResponseSchema struct {
	Name   string          `json:"name"`   // schema identifier (required by some providers)
	Schema json.RawMessage `json:"schema"` // JSON Schema object
}

// SchemaObject is a typed builder for common JSON Schema constructs.
// Use with NewResponseSchema for a type-safe alternative to raw JSON:
//
//	core.NewResponseSchema("plan", &core.SchemaObject{
//	    Type: "object",
//	    Properties: map[string]*core.SchemaObject{
//	        "steps": {Type: "array", Items: &core.SchemaObject{Type: "string"}},
//	    },
//	    Required: []string{"steps"},
//	})
//
// For schemas that need keywords beyond this subset, use ResponseSchema
// directly with json.RawMessage.
type SchemaObject struct {
	Type        string                   `json:"type"`
	Description string                   `json:"description,omitempty"`
	Properties  map[string]*SchemaObject `json:"properties,omitempty"`
	Items       *SchemaObject            `json:"items,omitempty"`
	Enum        []string                 `json:"enum,omitempty"`
	Required    []string                 `json:"required,omitempty"`
}

// NewResponseSchema creates a ResponseSchema by marshalling a SchemaObject.
// This provides a type-safe way to build JSON Schemas without raw JSON strings.
func NewResponseSchema(name string, s *SchemaObject) *ResponseSchema {
	b, _ := json.Marshal(s)
	return &ResponseSchema{Name: name, Schema: b}
}

// GenerationParams controls LLM generation behavior.
// All fields are pointers — nil means "use provider default".
// A Temperature of 0.0 is a valid setting, so nil (not zero) signals "unset".
type GenerationParams struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int     `json:"top_k,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
}

type ChatRequest struct {
	Messages         []ChatMessage     `json:"messages"`
	Tools            []ToolDefinition  `json:"tools,omitempty"`
	ResponseSchema   *ResponseSchema   `json:"response_schema,omitempty"`
	GenerationParams *GenerationParams `json:"generation_params,omitempty"`
}

type ChatResponse struct {
	Content     string       `json:"content"`
	Thinking    string       `json:"thinking,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	Usage       Usage        `json:"usage"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CachedTokens int `json:"cached_tokens,omitempty"` // input tokens served from provider cache
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// --- ChatMessage constructors ---

func UserMessage(text string) ChatMessage {
	return ChatMessage{Role: "user", Content: text}
}

func SystemMessage(text string) ChatMessage {
	return ChatMessage{Role: "system", Content: text}
}

func AssistantMessage(text string) ChatMessage {
	return ChatMessage{Role: "assistant", Content: text}
}

func ToolResultMessage(callID, content string) ChatMessage {
	return ChatMessage{Role: "tool", Content: content, ToolCallID: callID}
}

// --- Error types ---

type ErrLLM struct {
	Provider string
	Message  string
}

func (e *ErrLLM) Error() string {
	return fmt.Sprintf("%s: %s", e.Provider, e.Message)
}

type ErrHTTP struct {
	Status     int
	Body       string
	RetryAfter time.Duration // parsed from Retry-After header; zero = not set
}

func (e *ErrHTTP) Error() string {
	return fmt.Sprintf("http %d: %s", e.Status, e.Body)
}

// ParseRetryAfter parses a Retry-After header value into a duration.
// Supports both delay-seconds ("120") and HTTP-date ("Wed, 21 Oct 2015 07:28:00 GMT")
// formats per RFC 9110 §10.2.3. Returns zero on empty or unparseable values.
func ParseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	// Try seconds first (most common for rate limiting).
	var secs int
	if _, err := fmt.Sscanf(value, "%d", &secs); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	// Try HTTP-date format.
	if t, err := time.Parse(time.RFC1123, value); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}
