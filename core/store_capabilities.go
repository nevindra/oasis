package core

import "context"

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
