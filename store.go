package oasis

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
	SearchMessages(ctx context.Context, embedding []float32, topK int) ([]Message, error)

	// --- Documents + Chunks ---
	StoreDocument(ctx context.Context, doc Document, chunks []Chunk) error
	SearchChunks(ctx context.Context, embedding []float32, topK int) ([]Chunk, error)

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
	SearchSkills(ctx context.Context, embedding []float32, topK int) ([]Skill, error)

	// --- Lifecycle ---
	Init(ctx context.Context) error
	Close() error
}
