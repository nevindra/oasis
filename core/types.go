package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
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

// Provider abstracts the LLM backend. ChatStream is the only entry point;
// for non-streaming use, callers use the core.Chat helper (which passes nil).
type Provider interface {
	// ChatStream emits StreamEvent values into ch as content is generated.
	// ch may be nil for non-streaming calls — when nil, implementations MUST
	// NOT send to or close ch. When non-nil, implementations MUST close ch
	// before returning. Returns the final assembled ChatResponse with
	// complete ToolCalls and Usage.
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

// UIComponent describes a frontend component to render in place of (or
// alongside) a tool result's text. Name is a registry key the frontend
// resolves to a renderer; Props is the typed payload the renderer receives.
type UIComponent struct {
	Name  string          `json:"name"`
	Props json.RawMessage `json:"props"`
}

// ToolResult is the outcome of a tool execution.
// Content holds the result as a string. For plain text, use TextResult.
// For JSON output, use JSONResult (which marshals to a JSON string).
// For pre-encoded JSON bytes, use JSONContent.
type ToolResult struct {
	Content     string       `json:"content,omitempty"`
	Error       string       `json:"error,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"` // multimodal content (images, PDFs, etc.) passed to the LLM
	// UI, when non-nil, instructs consumers to render the result as the named
	// frontend component instead of (or alongside) Content. Set via UIResult
	// or by an Out type implementing UIRenderable.
	UI *UIComponent `json:"ui,omitempty"`
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

// Add registers a tool, indexed by t.Name(). If a tool with the same name is
// already registered, it is replaced in place (preserving slice order) rather
// than appended, so AllDefinitions never emits duplicate names.
func (r *ToolRegistry) Add(t AnyTool) {
	name := t.Name()
	if old, exists := r.index[name]; exists {
		// Find the old tool's position and overwrite it in place.
		for i, existing := range r.tools {
			if existing == old {
				r.tools[i] = t
				break
			}
		}
	} else {
		r.tools = append(r.tools, t)
	}
	r.index[name] = t
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
	defs := make([]ToolDefinition, 0, len(r.tools))
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

// Lookup returns the tool registered under name, or (nil, false) when not found.
// Used by the agent loop to perform interface checks (e.g. core.Sourced) on
// the live tool object after dispatch.
func (r *ToolRegistry) Lookup(name string) (AnyTool, bool) {
	t, ok := r.index[name]
	return t, ok
}

// IsStreamingTool reports whether the tool registered under name implements
// StreamingAnyTool. Returns false for unknown names. Used by the agent
// dispatch layer to decide whether to bypass the per-tool policy wrapper.
func (r *ToolRegistry) IsStreamingTool(name string) bool {
	t, ok := r.index[name]
	if !ok {
		return false
	}
	_, ok = t.(StreamingAnyTool)
	return ok
}

// --- LLM protocol types ---

// Role is the originator of a chat message.
//
// Defined as a typed string so `msg.Role == "user"` continues to compile (Go
// allows comparing a defined string type to an untyped string literal). JSON
// round-trips are preserved without a custom marshaler.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ChatMessage struct {
	Role        Role            `json:"role"` // see Role* constants
	Content     string          `json:"content"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	ToolCalls   []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID  string          `json:"tool_call_id,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"` // provider-specific (e.g. Gemini thoughtSignature)
	// CacheCheckpoint signals that providers supporting ephemeral prompt
	// caching should mark this message as a cache breakpoint. The provider
	// caches all tokens up to and including this message. Providers without
	// cache support ignore this field. Mutually composable with provider-
	// specific markers (e.g. openaicompat.WithCacheControl).
	CacheCheckpoint bool `json:"-"`
}

// Attachment represents binary content (image, PDF, audio, video, etc.) sent to a multimodal LLM.
// The MimeType determines how the provider interprets the data.
//
// Populate URL for remote references (pre-uploaded to storage/CDN) or Data for
// transient inline bytes. Providers resolve the best transport: URL > Data.
//
// Construct via NewAttachment, NewAttachmentFromURL, or NewAttachmentFromBase64
// to surface decode errors at construction time rather than at provider call time.
type Attachment struct {
	MimeType string `json:"mime_type"`
	URL      string `json:"url,omitempty"`
	// Data carries raw inline bytes. encoding/json marshals []byte as a
	// base64 string on the wire, so JSON round-trips preserve binary content.
	Data []byte `json:"data,omitempty"`
}

// NewAttachment constructs an Attachment from raw inline bytes.
func NewAttachment(mime string, data []byte) Attachment {
	return Attachment{MimeType: mime, Data: data}
}

// NewAttachmentFromURL constructs an Attachment from a remote URL.
// Providers fetch the resource at request time.
func NewAttachmentFromURL(mime, url string) Attachment {
	return Attachment{MimeType: mime, URL: url}
}

// NewAttachmentFromBase64 decodes a base64-encoded payload into an Attachment.
// Returns an error if the encoded string is not valid base64.
//
// Use this when integrating with a source that hands you base64 (some legacy
// APIs, document extractors). For raw bytes, use NewAttachment directly.
func NewAttachmentFromBase64(mime, encoded string) (Attachment, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return Attachment{}, fmt.Errorf("decode base64 attachment: %w", err)
	}
	return Attachment{MimeType: mime, Data: data}, nil
}

// InlineData returns the raw inline bytes, or nil if the attachment only carries a URL.
// Why: callers historically branched on Data vs Base64; constructors now decode
// at construction so this read path is infallible.
func (a Attachment) InlineData() []byte { return a.Data }

// HasInlineData reports whether inline bytes are available.
func (a Attachment) HasInlineData() bool { return len(a.Data) > 0 }

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
	// Modalities requests specific output modalities (e.g. ["text","image"]).
	// Providers that support image output enable returning generated images
	// when "image" is present. Empty means text-only (the default). Provider-
	// agnostic: OpenAI-compatible providers map it to the request's
	// `modalities` field; Gemini maps it to responseModalities.
	Modalities []string `json:"modalities,omitempty"`
}

type ChatResponse struct {
	Content     string       `json:"content"`
	Thinking    string       `json:"thinking,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	Usage       Usage        `json:"usage"`
	// FinishReason is the provider-reported reason for stopping. Providers
	// that don't report a finish reason leave this empty; the agent loop
	// then synthesizes one (FinishToolCalls if ToolCalls is non-empty,
	// otherwise FinishStop) when populating EventRunFinish and AgentResult.
	FinishReason FinishReason `json:"finish_reason,omitempty"`
	// Warnings are non-fatal provider notes (e.g. fallback used, parameter
	// ignored). Decorator providers (RetryMiddleware, ratelimit) may append.
	Warnings []string `json:"warnings,omitempty"`
	// ProviderMeta carries provider-specific opaque metadata. Documented
	// per provider package; consumers decode according to provider docs.
	ProviderMeta json.RawMessage `json:"provider_meta,omitempty"`
}

// Usage reports token consumption for a single LLM call.
//
// CachedTokens counts input tokens that were served from the provider's prompt
// cache — a cache hit. Both OpenAI (via prompt_tokens_details.cached_tokens)
// and Anthropic (via cache_read_input_tokens) populate this field.
//
// CacheCreationTokens counts input tokens written into the provider's prompt
// cache during this call — a cache-warming cost paid now to save tokens on
// future calls. Populated by Anthropic (cache_creation_input_tokens) only;
// OpenAI does not expose this metric.
type Usage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CachedTokens        int `json:"cached_tokens,omitempty"`         // tokens READ from cache (cache hit); both providers
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"` // tokens WRITTEN to cache (cache-warming cost); Anthropic only
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`            // JSON Schema for the input.
	// OutputSchema is the JSON Schema for the tool's successful result. It is
	// derived at registration time by Erase/EraseStreaming via DeriveSchema[Out].
	// Tools that need richer constraints than reflection produces may implement
	// OutSchemaProvider to override the derived schema. Provider implementations
	// decide whether to forward this field to the LLM in the tool spec.
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// OutSchemaProvider is the opt-in override for the reflection-based output
// schema derivation performed by Erase. Tool implementations may implement
// this to supply a custom JSON Schema (enum values, format hints, min/max)
// that reflection cannot express.
//
// When a Tool[In, Out] also implements OutSchemaProvider, Erase uses the
// override and discards the schema derived from Out.
type OutSchemaProvider interface {
	OutSchema() json.RawMessage
}

// --- ChatMessage constructors ---

func UserMessage(text string) ChatMessage {
	return ChatMessage{Role: RoleUser, Content: text}
}

func SystemMessage(text string) ChatMessage {
	return ChatMessage{Role: RoleSystem, Content: text}
}

func AssistantMessage(text string) ChatMessage {
	return ChatMessage{Role: RoleAssistant, Content: text}
}

func ToolResultMessage(callID, content string) ChatMessage {
	return ChatMessage{Role: RoleTool, Content: content, ToolCallID: callID}
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
	// strconv.Atoi is ~100× faster than fmt.Sscanf("%d", ...) — finding 4.1.f.
	if secs, err := strconv.Atoi(value); err == nil && secs > 0 {
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
