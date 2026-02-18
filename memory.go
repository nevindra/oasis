package oasis

import "context"

// ExtractedFact is a user fact extracted from a conversation turn.
// Returned by the auto-extraction pipeline and persisted to MemoryStore.
type ExtractedFact struct {
	Fact       string  `json:"fact"`
	Category   string  `json:"category"`
	Supersedes *string `json:"supersedes,omitempty"`
}

// MemoryStore provides long-term user memory with semantic deduplication.
// Optional — pass to WithUserMemory() to enable.
type MemoryStore interface {
	UpsertFact(ctx context.Context, fact, category string, embedding []float32) error
	// SearchFacts returns facts semantically similar to the query embedding,
	// sorted by Score descending. Only facts with confidence >= 0.3 are returned.
	SearchFacts(ctx context.Context, embedding []float32, topK int) ([]ScoredFact, error)
	BuildContext(ctx context.Context, queryEmbedding []float32) (string, error)
	// DeleteFact removes a single fact by its ID.
	DeleteFact(ctx context.Context, factID string) error
	DeleteMatchingFacts(ctx context.Context, pattern string) error
	DecayOldFacts(ctx context.Context) error
	Init(ctx context.Context) error
}
