package core

import "context"

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
	// SearchMessages performs semantic similarity search across messages.
	// Results are sorted by Score descending (cosine similarity in [0, 1]).
	//
	// When chatID is non-empty, results are restricted to messages whose
	// thread belongs to that chat — prevents cross-user contamination in
	// multi-tenant deployments and eliminates an N+1 lookup at the caller.
	// When chatID is "", the search spans all messages.
	SearchMessages(ctx context.Context, embedding []float32, topK int, chatID string) ([]ScoredMessage, error)

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
