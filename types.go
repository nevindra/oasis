package oasis

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
type Provider interface {
	// Chat sends a request and returns a complete response.
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	// ChatWithTools sends a request with tool definitions, returns response (may contain tool calls).
	ChatWithTools(ctx context.Context, req ChatRequest, tools []ToolDefinition) (ChatResponse, error)
	// ChatStream streams text-delta events into ch, then returns the final response with usage stats.
	ChatStream(ctx context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error)
	// Name returns the provider name (e.g. "gemini", "anthropic").
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

// Tool defines an agent capability with one or more tool functions.
type Tool interface {
	Definitions() []ToolDefinition
	Execute(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)
}

// ToolResult is the outcome of a tool execution.
type ToolResult struct {
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

// ToolRegistry holds all registered tools and dispatches execution.
type ToolRegistry struct {
	tools []Tool
	index map[string]Tool // name → Tool for O(1) dispatch
}

// NewToolRegistry creates an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{index: make(map[string]Tool)}
}

// Add registers a tool and indexes its definitions for O(1) lookup.
func (r *ToolRegistry) Add(t Tool) {
	r.tools = append(r.tools, t)
	for _, d := range t.Definitions() {
		r.index[d.Name] = t
	}
}

// AllDefinitions returns tool definitions from all registered tools.
func (r *ToolRegistry) AllDefinitions() []ToolDefinition {
	var defs []ToolDefinition
	for _, t := range r.tools {
		defs = append(defs, t.Definitions()...)
	}
	return defs
}

// Execute dispatches a tool call by name using the pre-built index.
func (r *ToolRegistry) Execute(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	if t, ok := r.index[name]; ok {
		return t.Execute(ctx, name, args)
	}
	return ToolResult{Error: "unknown tool: " + name}, nil
}

// Store abstracts persistence with vector search capabilities.
type Store interface {
	// --- Threads ---
	CreateThread(ctx context.Context, thread Thread) error
	GetThread(ctx context.Context, id string) (Thread, error)
	ListThreads(ctx context.Context, chatID string, limit int) ([]Thread, error)
	UpdateThread(ctx context.Context, thread Thread) error
	DeleteThread(ctx context.Context, id string) error

	// --- Messages ---
	StoreMessage(ctx context.Context, msg Message) error
	GetMessages(ctx context.Context, threadID string, limit int) ([]Message, error)
	// SearchMessages performs semantic similarity search across all messages.
	// Results are sorted by Score descending (cosine similarity in [0, 1]).
	SearchMessages(ctx context.Context, embedding []float32, topK int) ([]ScoredMessage, error)

	// --- Documents + Chunks ---
	StoreDocument(ctx context.Context, doc Document, chunks []Chunk) error
	ListDocuments(ctx context.Context, limit int) ([]Document, error)
	// DeleteDocument removes a document and all its chunks (cascade).
	DeleteDocument(ctx context.Context, id string) error
	// SearchChunks performs semantic similarity search over document chunks.
	// Results are sorted by Score descending.
	SearchChunks(ctx context.Context, embedding []float32, topK int, filters ...ChunkFilter) ([]ScoredChunk, error)
	GetChunksByIDs(ctx context.Context, ids []string) ([]Chunk, error)

	// --- Key-value config ---
	GetConfig(ctx context.Context, key string) (string, error)
	SetConfig(ctx context.Context, key, value string) error

	// --- Scheduled Actions ---
	CreateScheduledAction(ctx context.Context, action ScheduledAction) error
	ListScheduledActions(ctx context.Context) ([]ScheduledAction, error)
	GetDueScheduledActions(ctx context.Context, now int64) ([]ScheduledAction, error)
	UpdateScheduledAction(ctx context.Context, action ScheduledAction) error
	UpdateScheduledActionEnabled(ctx context.Context, id string, enabled bool) error
	DeleteScheduledAction(ctx context.Context, id string) error
	DeleteAllScheduledActions(ctx context.Context) (int, error)
	FindScheduledActionsByDescription(ctx context.Context, pattern string) ([]ScheduledAction, error)

	// --- Skills ---
	CreateSkill(ctx context.Context, skill Skill) error
	GetSkill(ctx context.Context, id string) (Skill, error)
	ListSkills(ctx context.Context) ([]Skill, error)
	UpdateSkill(ctx context.Context, skill Skill) error
	DeleteSkill(ctx context.Context, id string) error
	// SearchSkills performs semantic similarity search over stored skills.
	// Results are sorted by Score descending.
	SearchSkills(ctx context.Context, embedding []float32, topK int) ([]ScoredSkill, error)

	// --- Lifecycle ---
	Init(ctx context.Context) error
	Close() error
}

// --- Domain types (database records) ---

// ScoredMessage is a Message paired with its cosine similarity score from a
// semantic search. Score is in [0, 1]; higher means more relevant.
type ScoredMessage struct {
	Message
	Score float32
}

// ScoredChunk is a Chunk paired with its cosine similarity score.
type ScoredChunk struct {
	Chunk
	Score float32
}

// ScoredSkill is a Skill paired with its cosine similarity score.
type ScoredSkill struct {
	Skill
	Score float32
}

// ScoredFact is a Fact paired with its cosine similarity score.
type ScoredFact struct {
	Fact
	Score float32
}

type Document struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
}

type Chunk struct {
	ID         string     `json:"id"`
	DocumentID string     `json:"document_id"`
	ParentID   string     `json:"parent_id,omitempty"`
	Content    string     `json:"content"`
	ChunkIndex int        `json:"chunk_index"`
	Embedding  []float32  `json:"-"`
	Metadata   *ChunkMeta `json:"metadata,omitempty"`
}

// ChunkMeta holds optional chunk-level metadata produced during extraction.
// Stored as JSON in the database. Zero values are omitted.
type ChunkMeta struct {
	PageNumber     int     `json:"page_number,omitempty"`
	SectionHeading string  `json:"section_heading,omitempty"`
	SourceURL      string  `json:"source_url,omitempty"`
	Images         []Image `json:"images,omitempty"`
}

// Image represents an extracted image from a document.
type Image struct {
	MimeType string `json:"mime_type"`
	Base64   string `json:"base64"`
	AltText  string `json:"alt_text,omitempty"`
	Page     int    `json:"page,omitempty"`
}

// --- Graph RAG ---

// RelationType represents a named relationship between chunks in a knowledge graph.
type RelationType string

const (
	RelReferences  RelationType = "references"
	RelElaborates  RelationType = "elaborates"
	RelDependsOn   RelationType = "depends_on"
	RelContradicts RelationType = "contradicts"
	RelPartOf      RelationType = "part_of"
	RelSimilarTo   RelationType = "similar_to"
	RelSequence    RelationType = "sequence"
	RelCausedBy    RelationType = "caused_by"
)

// ChunkEdge represents a directed, weighted relationship between two chunks.
type ChunkEdge struct {
	ID          string       `json:"id"`
	SourceID    string       `json:"source_id"`
	TargetID    string       `json:"target_id"`
	Relation    RelationType `json:"relation"`
	Weight      float32      `json:"weight"`
	Description string       `json:"description,omitempty"`
}

// --- Chunk filtering ---

// FilterOp is a comparison operator for chunk filters.
type FilterOp int

const (
	// OpEq matches when field equals value.
	OpEq FilterOp = iota
	// OpIn matches when field is in a set of values. Value must be []string.
	OpIn
	// OpGt matches when field is greater than value.
	OpGt
	// OpLt matches when field is less than value.
	OpLt
	// OpNeq matches when field does not equal value.
	OpNeq
)

// ChunkFilter restricts which chunks are considered during vector search.
// Field names: "document_id", "source", "created_at", or "meta.<key>" for
// JSON metadata fields (e.g. "meta.section_heading", "meta.page_number").
type ChunkFilter struct {
	Field string
	Op    FilterOp
	Value any
}

// ByDocument returns a filter matching chunks belonging to the given document IDs.
func ByDocument(ids ...string) ChunkFilter {
	return ChunkFilter{Field: "document_id", Op: OpIn, Value: ids}
}

// BySource returns a filter matching chunks from documents with the given source.
func BySource(source string) ChunkFilter {
	return ChunkFilter{Field: "source", Op: OpEq, Value: source}
}

// ByMeta returns a filter matching chunks where metadata key equals value.
// Key corresponds to a ChunkMeta JSON field (e.g. "section_heading", "page_number").
func ByMeta(key, value string) ChunkFilter {
	return ChunkFilter{Field: "meta." + key, Op: OpEq, Value: value}
}

// ByExcludeDocument returns a filter that excludes chunks belonging to the given document.
func ByExcludeDocument(docID string) ChunkFilter {
	return ChunkFilter{Field: "document_id", Op: OpNeq, Value: docID}
}

// CreatedAfter returns a filter matching chunks from documents created after unix timestamp.
func CreatedAfter(unix int64) ChunkFilter {
	return ChunkFilter{Field: "created_at", Op: OpGt, Value: unix}
}

// CreatedBefore returns a filter matching chunks from documents created before unix timestamp.
func CreatedBefore(unix int64) ChunkFilter {
	return ChunkFilter{Field: "created_at", Op: OpLt, Value: unix}
}

type Thread struct {
	ID        string            `json:"id"`
	ChatID    string            `json:"chat_id"`
	Title     string            `json:"title,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt int64             `json:"created_at"`
	UpdatedAt int64             `json:"updated_at"`
}

type Message struct {
	ID        string         `json:"id"`
	ThreadID  string         `json:"thread_id"`
	Role      string         `json:"role"` // "user" or "assistant"
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Embedding []float32      `json:"-"`
	CreatedAt int64          `json:"created_at"`
}

type Fact struct {
	ID         string    `json:"id"`
	Fact       string    `json:"fact"`
	Category   string    `json:"category"`
	Confidence float64   `json:"confidence"`
	Embedding  []float32 `json:"-"`
	CreatedAt  int64     `json:"created_at"`
	UpdatedAt  int64     `json:"updated_at"`
}

// Intent for classification
type Intent int

const (
	IntentChat   Intent = iota
	IntentAction
)

// Scheduled action (DB record)
type ScheduledAction struct {
	ID              string `json:"id"`
	Description     string `json:"description"`
	Schedule        string `json:"schedule"`
	ToolCalls       string `json:"tool_calls"`
	SynthesisPrompt string `json:"synthesis_prompt"`
	NextRun         int64  `json:"next_run"`
	Enabled         bool   `json:"enabled"`
	SkillID         string `json:"skill_id,omitempty"`
	CreatedAt       int64  `json:"created_at"`
}

type ScheduledToolCall struct {
	Tool   string          `json:"tool"`
	Params json.RawMessage `json:"params"`
}

// Skill is a stored instruction package for specializing the action agent.
type Skill struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Instructions string    `json:"instructions"`
	Tools        []string  `json:"tools,omitempty"`
	Model        string    `json:"model,omitempty"`
	Tags         []string  `json:"tags,omitempty"`       // categorization labels
	CreatedBy    string    `json:"created_by,omitempty"`  // origin: user ID or agent ID
	References   []string  `json:"references,omitempty"` // skill IDs this builds on
	Embedding    []float32 `json:"-"`
	CreatedAt    int64     `json:"created_at"`
	UpdatedAt    int64     `json:"updated_at"`
}

// --- LLM protocol types ---

type ChatMessage struct {
	Role       string          `json:"role"` // "system", "user", "assistant", "tool"
	Content    string          `json:"content"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"` // provider-specific (e.g. Gemini thoughtSignature)
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
//	oasis.NewResponseSchema("plan", &oasis.SchemaObject{
//	    Type: "object",
//	    Properties: map[string]*oasis.SchemaObject{
//	        "steps": {Type: "array", Items: &oasis.SchemaObject{Type: "string"}},
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

// --- Runtime workflow definition types ---

// NodeType identifies the kind of node in a WorkflowDefinition.
type NodeType string

const (
	// NodeLLM delegates to a registered Agent.
	NodeLLM NodeType = "llm"
	// NodeTool calls a registered Tool function.
	NodeTool NodeType = "tool"
	// NodeCondition evaluates an expression and routes to true/false branches.
	NodeCondition NodeType = "condition"
	// NodeTemplate performs string interpolation via WorkflowContext.Resolve.
	NodeTemplate NodeType = "template"
)

// WorkflowDefinition is a JSON-serializable description of a workflow DAG.
// Use FromDefinition to convert it into an executable *Workflow.
type WorkflowDefinition struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Nodes       []NodeDefinition `json:"nodes"`
	Edges       [][2]string      `json:"edges"` // [from, to] pairs
}

// NodeDefinition describes a single node in a runtime workflow.
type NodeDefinition struct {
	// ID is the unique identifier for this node within the workflow.
	ID   string   `json:"id"`
	// Type determines how the node executes.
	Type NodeType `json:"type"`

	// LLM node: agent registry key and input template.
	Agent string `json:"agent,omitempty"`
	Input string `json:"input,omitempty"` // template: "Summarize: {{search.output}}"

	// Tool node: tool registry key, function name, and argument templates.
	Tool     string         `json:"tool,omitempty"`
	ToolName string         `json:"tool_name,omitempty"`
	Args     map[string]any `json:"args,omitempty"` // values may contain {{key}} templates

	// Condition node: expression or registered function name, and branch targets.
	Expression  string   `json:"expression,omitempty"`
	TrueBranch  []string `json:"true_branch,omitempty"`
	FalseBranch []string `json:"false_branch,omitempty"`

	// Template node: template string to resolve.
	Template string `json:"template,omitempty"`

	// Common: override default output key, retry count.
	OutputTo string `json:"output_to,omitempty"`
	Retry    int    `json:"retry,omitempty"`
}

// DefinitionRegistry maps string names in a WorkflowDefinition to concrete
// Go objects. Pass to FromDefinition.
type DefinitionRegistry struct {
	// Agents maps names to Agent implementations (for LLM nodes).
	Agents map[string]Agent
	// Tools maps names to Tool implementations (for Tool nodes).
	Tools map[string]Tool
	// Conditions maps names to custom condition functions (escape hatch for
	// complex logic that can't be expressed as a simple comparison).
	Conditions map[string]func(*WorkflowContext) bool
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
