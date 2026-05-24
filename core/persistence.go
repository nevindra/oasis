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

// ChunkFilterValue is the typed value carried by a ChunkFilter. The concrete
// types (StringValue, StringsValue, Int64Value) form a closed sum — stores
// dispatch via Raw() (for direct SQL arg passing) or a type switch (when
// they need the typed slice). The unexported marker keeps the set closed.
type ChunkFilterValue interface {
	isChunkFilterValue()
	// Raw returns the unwrapped underlying value suitable for passing to
	// database/sql args: string for StringValue, []string for StringsValue,
	// int64 for Int64Value. Stores call this instead of type-asserting an
	// untyped any.
	Raw() any
}

// StringValue is a scalar string used by BySource, ByMeta, and ByExcludeDocument.
type StringValue string

func (StringValue) isChunkFilterValue() {}
func (v StringValue) Raw() any          { return string(v) }

// StringsValue is a string slice used by ByDocument (with OpIn).
type StringsValue []string

func (StringsValue) isChunkFilterValue() {}
func (v StringsValue) Raw() any          { return []string(v) }

// Int64Value is a scalar int64 used by CreatedAfter and CreatedBefore.
type Int64Value int64

func (Int64Value) isChunkFilterValue() {}
func (v Int64Value) Raw() any          { return int64(v) }

// ChunkFilter restricts which chunks are considered during vector search.
// Field names: "document_id", "source", "created_at", or "meta.<key>" for
// JSON metadata fields (e.g. "meta.section_heading", "meta.page_number").
// Value is one of StringValue, StringsValue, or Int64Value — see the
// constructors below for the canonical Op/Value pairings.
type ChunkFilter struct {
	Field string
	Op    FilterOp
	Value ChunkFilterValue
}

// ByDocument returns a filter matching chunks belonging to the given document IDs.
func ByDocument(ids ...string) ChunkFilter {
	return ChunkFilter{Field: "document_id", Op: OpIn, Value: StringsValue(ids)}
}

// BySource returns a filter matching chunks from documents with the given source.
func BySource(source string) ChunkFilter {
	return ChunkFilter{Field: "source", Op: OpEq, Value: StringValue(source)}
}

// ByMeta returns a filter matching chunks where metadata key equals value.
// Key corresponds to a ChunkMeta JSON field (e.g. "section_heading", "page_number").
func ByMeta(key, value string) ChunkFilter {
	return ChunkFilter{Field: "meta." + key, Op: OpEq, Value: StringValue(value)}
}

// ByExcludeDocument returns a filter that excludes chunks belonging to the given document.
func ByExcludeDocument(docID string) ChunkFilter {
	return ChunkFilter{Field: "document_id", Op: OpNeq, Value: StringValue(docID)}
}

// CreatedAfter returns a filter matching chunks from documents created after unix timestamp.
func CreatedAfter(unix int64) ChunkFilter {
	return ChunkFilter{Field: "created_at", Op: OpGt, Value: Int64Value(unix)}
}

// CreatedBefore returns a filter matching chunks from documents created before unix timestamp.
func CreatedBefore(unix int64) ChunkFilter {
	return ChunkFilter{Field: "created_at", Op: OpLt, Value: Int64Value(unix)}
}

type Thread struct {
	ID        string            `json:"id"`
	ChatID    string            `json:"chat_id"`
	Title     string            `json:"title,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt int64             `json:"created_at"`
	UpdatedAt int64             `json:"updated_at"`
}

// Message is a persisted conversation message. Metadata is opaque JSON —
// callers marshal their typed payload into the field and unmarshal it back
// at read time. Storing raw JSON eliminates the unmarshal-then-remarshal
// round-trip through map[string]any and surfaces corrupt blobs as caller
// errors instead of silent nils.
type Message struct {
	ID       string `json:"id"`
	ThreadID string `json:"thread_id"`
	// Role uses the same typed string as ChatMessage.Role so the two layers
	// can be compared and assigned without conversion. JSON wire format is
	// unchanged. database/sql.Scan accepts *Role via reflection on string-kind.
	Role      Role            `json:"role"`
	Content   string          `json:"content"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	Embedding []float32       `json:"-"`
	CreatedAt int64           `json:"created_at"`
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
