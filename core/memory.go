// core/memory.go
package core

import "context"

// MemoryItemStore stores MemoryItems. It is defined here in core so that
// satellite store implementations (store/sqlite, store/postgres) depend only
// on this leaf package rather than on the memory package, which has its own
// pipeline and orchestration logic. Keeping the storage interface in core
// collapses one dependency edge and prevents import cycles.
//
// Concurrent use: implementations must be safe for concurrent calls.
type MemoryItemStore interface {
	// Init creates any required tables/indices. Safe to call repeatedly.
	Init(ctx context.Context) error

	// Upsert writes a MemoryItem. If an item with the same ID already
	// exists, all fields except CreatedAt are overwritten and UpdatedAt
	// is set to the current time.
	Upsert(ctx context.Context, item MemoryItem) error

	// UpsertBatch is like Upsert for many items in one transaction.
	UpsertBatch(ctx context.Context, items []MemoryItem) error

	// Delete removes one item by ID. Returns nil if not found.
	Delete(ctx context.Context, id string) error

	// DeleteWhere removes all items matching the filter and returns the
	// count deleted. Empty filter is rejected with an error to prevent
	// accidental "delete everything".
	DeleteWhere(ctx context.Context, filter MemoryFilter) (int, error)

	// Get returns a single item by ID, or ErrNotFound.
	Get(ctx context.Context, id string) (MemoryItem, error)

	// List returns items matching the filter in CreatedAt-descending order.
	// Never nil.
	List(ctx context.Context, filter MemoryFilter) ([]MemoryItem, error)

	// SearchSemantic returns up to topK items matching the filter, ranked
	// by cosine similarity to the embedding (descending). Items without
	// embeddings are skipped (not an error).
	SearchSemantic(ctx context.Context, embedding []float32, filter MemoryFilter, topK int) ([]ScoredMemoryItem, error)
}

// MemoryKind discriminates the role of a MemoryItem. The framework defines
// six canonical kinds; Kind is an open string type so users may define their
// own.
type MemoryKind string

const (
	MemoryKindFact       MemoryKind = "fact"
	MemoryKindNote       MemoryKind = "note"
	MemoryKindEvent      MemoryKind = "event"
	MemoryKindPlaybook   MemoryKind = "playbook"
	MemoryKindReflection MemoryKind = "reflection"
	MemoryKindSummary    MemoryKind = "summary"
)

// MemoryScopeKind is the partition kind for memory visibility.
type MemoryScopeKind string

const (
	MemoryScopeThread   MemoryScopeKind = "thread"
	MemoryScopeResource MemoryScopeKind = "resource"
	MemoryScopeAgent    MemoryScopeKind = "agent"
	MemoryScopeGlobal   MemoryScopeKind = "global"
)

// MemoryScope anchors a MemoryItem to a specific instance of a MemoryScopeKind.
type MemoryScope struct {
	Kind MemoryScopeKind
	Ref  string
}

// MemorySource records provenance — where a MemoryItem came from.
type MemorySource struct {
	Kind    string // "message" | "tool" | "user" | "agent" | "extraction"
	Ref     string // foreign key (message ID, tool call ID, etc.) — may be empty
	AgentID string
}

// MemoryItem is the universal record type for all memory layers.
// One struct, discriminated by Kind, covering facts, notes, events,
// playbooks, reflections, summaries, and any user-defined kinds.
//
// Content is the canonical text shown to the LLM. Structured data should
// be JSON-encoded into Content — there is no Data field.
type MemoryItem struct {
	ID        string
	Kind      MemoryKind
	Content   string
	Scope     MemoryScope
	Source    MemorySource
	Pinned    bool
	Tags      []string
	Embedding []float32
	CreatedAt int64
	UpdatedAt int64
	ExpiresAt int64 // 0 = never
}

// ScoredMemoryItem is a MemoryItem paired with a similarity score from
// semantic search. Score is cosine similarity in [0, 1].
type ScoredMemoryItem struct {
	Item  MemoryItem
	Score float32
}

// MemoryFilter selects MemoryItems for read or delete queries.
type MemoryFilter struct {
	Kinds      []MemoryKind   // OR; empty = any
	Scope      *MemoryScope   // nil = any; non-nil = exact match on Kind+Ref
	Tags       []string       // AND; all tags must be present
	Pinned     *bool          // nil = any; true = only pinned; false = only unpinned
	Since      int64          // CreatedAt >= Since (0 = no lower bound)
	Until      int64          // CreatedAt <= Until (0 = no upper bound)
	Limit      int            // 0 = implementation default (50)
	IncludeExp bool           // include items where ExpiresAt > 0 AND ExpiresAt <= now
}

// IsEmpty reports whether the filter would match every item.
// DeleteWhere uses this to reject unbounded deletes.
func (f MemoryFilter) IsEmpty() bool {
	return len(f.Kinds) == 0 && f.Scope == nil && len(f.Tags) == 0 &&
		f.Pinned == nil && f.Since == 0 && f.Until == 0 && f.Limit == 0 && !f.IncludeExp
}
