package oasis

import "context"

// VectorStore abstracts persistence with vector search capabilities.
type VectorStore interface {
	// --- Messages ---
	StoreMessage(ctx context.Context, msg Message) error
	GetMessages(ctx context.Context, conversationID string, limit int) ([]Message, error)
	SearchMessages(ctx context.Context, embedding []float32, topK int) ([]Message, error)

	// --- Documents + Chunks ---
	StoreDocument(ctx context.Context, doc Document, chunks []Chunk) error
	SearchChunks(ctx context.Context, embedding []float32, topK int) ([]Chunk, error)

	// --- Conversations ---
	GetOrCreateConversation(ctx context.Context, chatID string) (Conversation, error)

	// --- Key-value config ---
	GetConfig(ctx context.Context, key string) (string, error)
	SetConfig(ctx context.Context, key, value string) error

	// --- Lifecycle ---
	Init(ctx context.Context) error
	Close() error
}
