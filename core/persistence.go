package core

import "encoding/json"

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
