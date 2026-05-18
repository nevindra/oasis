package oasis

import (
	"context"
	"encoding/json"
)

// This file holds types that are still root-package-local during the Phase 0
// migration: the Store/SkillProvider interfaces and their domain record types
// (Thread, Message, Document, Chunk, Fact, ScheduledAction, etc.), plus
// workflow definition shapes. The LLM protocol types and core interfaces have
// moved to github.com/nevindra/oasis/core — see types_aliases.go for the
// transitional re-export aliases.

// --- Persistence ---

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

	// --- Lifecycle ---
	Init(ctx context.Context) error
	Close() error
}

// SkillProvider discovers and loads skills from any backing store.
// Implementations must be safe for concurrent use.
type SkillProvider interface {
	// Discover returns lightweight summaries of all available skills.
	// Only name, description, and tags are loaded — full instructions remain unread.
	// Results are rescanned on every call (no caching), so newly created skills
	// are immediately visible without restart.
	Discover(ctx context.Context) ([]SkillSummary, error)

	// Activate loads the full skill by name, including instructions and metadata.
	// Returns an error if the skill does not exist.
	Activate(ctx context.Context, name string) (Skill, error)
}

// SkillWriter creates and modifies skills. File-based providers implement this
// to let agents author skills at runtime. Check via type assertion:
//
//	if w, ok := provider.(SkillWriter); ok { ... }
type SkillWriter interface {
	// CreateSkill writes a new skill. The Name field determines the folder name.
	CreateSkill(ctx context.Context, skill Skill) error

	// UpdateSkill modifies an existing skill identified by name.
	UpdateSkill(ctx context.Context, name string, skill Skill) error

	// DeleteSkill removes a skill and its entire folder.
	DeleteSkill(ctx context.Context, name string) error
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
	// ContentType discriminates chunk modality: "text" (default/empty) or "image".
	// Used by filters to scope retrieval to a specific modality.
	ContentType string `json:"content_type,omitempty"`
	// BlobRef is an opaque reference to a BlobStore object (e.g. "s3://bucket/key").
	// Populated when images are stored externally instead of inline in Images.
	BlobRef string `json:"blob_ref,omitempty"`
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

// SkillSummary is a lightweight view of a skill for discovery.
// Contains only the metadata needed for an agent to decide whether to activate.
type SkillSummary struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Tags          []string `json:"tags,omitempty"`
	Compatibility string   `json:"compatibility,omitempty"`
}

// Skill is a stored instruction package that specializes agent behavior.
// Skills are folders on disk with a SKILL.md file containing YAML frontmatter
// (metadata) and markdown body (instructions). Compatible with the AgentSkills
// open specification (https://agentskills.io/specification.md).
type Skill struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Instructions  string            `json:"instructions"`
	Tools         []string          `json:"tools,omitempty"`
	Model         string            `json:"model,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	References    []string          `json:"references,omitempty"`
	Dir           string            `json:"-"`
	Compatibility string            `json:"compatibility,omitempty"`
	License       string            `json:"license,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
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
	ID string `json:"id"`
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
	// Tools maps names to AnyTool implementations (for Tool nodes).
	// Each entry is one atomic tool — the map key is the registry name used
	// in NodeDefinition.Tool and matches the tool's own Name().
	Tools map[string]AnyTool
	// Conditions maps names to custom condition functions (escape hatch for
	// complex logic that can't be expressed as a simple comparison).
	Conditions map[string]func(*WorkflowContext) bool
}
