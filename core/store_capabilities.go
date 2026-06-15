package core

import (
	"context"
	"encoding/json"
	"time"
)

// KeywordSearcher is an optional Store capability for full-text keyword search.
// Store implementations that support FTS can implement this interface;
// callers discover it via type assertion.
type KeywordSearcher interface {
	SearchChunksKeyword(ctx context.Context, query string, topK int, filters ...ChunkFilter) ([]ScoredChunk, error)
}

// GraphStore is an optional Store capability for chunk relationship graphs.
// Store implementations that maintain a knowledge graph can implement this
// interface; callers discover it via type assertion.
type GraphStore interface {
	StoreEdges(ctx context.Context, edges []ChunkEdge) error
	GetEdges(ctx context.Context, chunkIDs []string) ([]ChunkEdge, error)
	GetIncomingEdges(ctx context.Context, chunkIDs []string) ([]ChunkEdge, error)
	PruneOrphanEdges(ctx context.Context) (int, error)
}

// BidirectionalGraphStore is an optional GraphStore capability that fetches
// both outgoing and incoming edges in a single query. When the Store implements
// this interface, GraphRetriever uses it to reduce the number of database
// round-trips per hop from 2 to 1 when bidirectional traversal is enabled.
type BidirectionalGraphStore interface {
	GetBothEdges(ctx context.Context, chunkIDs []string) ([]ChunkEdge, error)
}

// DocumentGetter is an optional Store capability for batch document lookup by ID.
// Store implementations that support it can implement this interface; callers
// discover it via type assertion.
type DocumentGetter interface {
	GetDocumentsByIDs(ctx context.Context, ids []string) ([]Document, error)
}

// DocumentMetaLister is an optional Store capability that returns documents
// without their potentially-large Description blob. Callers needing only Title
// and CreatedAt should prefer this over ListDocuments to avoid loading
// expensive fields.
type DocumentMetaLister interface {
	ListDocumentMeta(ctx context.Context, limit int) ([]Document, error)
}

// ScheduledActionStore is an optional Store capability for scheduled actions.
// Store implementations that support scheduling can implement this interface;
// callers discover it via type assertion.
// Docs: docs/external/store/api.md — listed under "Optional capability interfaces", not in the base Store interface.
type ScheduledActionStore interface {
	CreateScheduledAction(ctx context.Context, action ScheduledAction) error
	ListScheduledActions(ctx context.Context) ([]ScheduledAction, error)
	GetDueScheduledActions(ctx context.Context, now int64) ([]ScheduledAction, error)
	UpdateScheduledAction(ctx context.Context, action ScheduledAction) error
	UpdateScheduledActionEnabled(ctx context.Context, id string, enabled bool) error
	DeleteScheduledAction(ctx context.Context, id string) error
	DeleteAllScheduledActions(ctx context.Context) (int, error)
	ListScheduledActionsByDescription(ctx context.Context, pattern string) ([]ScheduledAction, error)
}

// ScoreStore is an optional Store capability for persisting scorer results.
// Store implementations that support it can implement this interface; callers
// discover it via type assertion. Stores that don't implement it simply skip
// persistence (inline scores still reach AgentResult.Scores).
// SaveScores is batch-first so the async scorer worker can amortize writes;
// a single row is a one-element slice.
type ScoreStore interface {
	SaveScores(ctx context.Context, rows []ScoreRow) error
	ListScores(ctx context.Context, filter ScoreFilter) ([]ScoreRow, error)
	GetScore(ctx context.Context, id string) (ScoreRow, error)
	DeleteScores(ctx context.Context, filter ScoreFilter) (int, error)
}

// ScoreSink forwards scores to an external eval platform (Braintrust, LangSmith,
// etc.). Optional; attach with agent.WithScoreSink. Implementations POST raw
// HTTP+JSON — no vendor SDK.
type ScoreSink interface {
	Emit(ctx context.Context, row ScoreRow) error
}

// ScoreRow is the persisted form of one scorer result: the pure Score plus the
// run/entity identity the runtime adds. Details stays typed JSON — no map[string]any.
type ScoreRow struct {
	ID         string
	ScorerID   string
	RunID      string
	EntityID   string // agent or workflow name
	EntityType string // "agent" | "workflow" | "step"
	Input      string
	Output     string
	Value      float64
	Reason     string
	Details    json.RawMessage
	Source     ScorerSource
	CreatedAt  time.Time
}

// ScoreFilter controls ListScores / DeleteScores results. Zero-value fields are
// ignored (no constraint).
type ScoreFilter struct {
	ScorerID string
	EntityID string
	Source   ScorerSource
	Since    time.Time
	Limit    int
}
