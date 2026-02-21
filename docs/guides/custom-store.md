# Building a Custom Store

Implement the `Store` interface to add a new storage backend. For PostgreSQL with pgvector, use the shipped `store/postgres` package instead — see [Store Concept](../concepts/store.md). This guide is for building your own backend (DynamoDB, Qdrant, etc.).

## Implement Store

The `Store` interface has many methods grouped by domain. Start by implementing the lifecycle and the methods your use case needs:

```go
package mystore

import (
    "context"

    oasis "github.com/nevindra/oasis"
)

type Store struct {
    connString string
}

func New(connString string) *Store {
    return &Store{connString: connString}
}

func (s *Store) Init(ctx context.Context) error {
    // Create tables and indexes
    return nil
}

func (s *Store) Close() error {
    // Clean up connections
    return nil
}

// compile-time check
var _ oasis.Store = (*Store)(nil)
```

## Vector Search

The most important implementation detail. You need cosine similarity search over embeddings.

Options:
- **Brute-force in-memory** — like `store/sqlite`. Simple, works for personal-scale data.
- **Database-native indexes** — pgvector (Postgres), DiskANN, HNSW
- **External vector DB** — Pinecone, Qdrant, Weaviate

Search methods accept variadic `ChunkFilter` arguments for metadata filtering:

```go
func (s *Store) SearchChunks(ctx context.Context, embedding []float32, topK int, filters ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
    // Apply filters to narrow the search scope
    for _, f := range filters {
        switch {
        case f.DocumentID != "":
            // Filter by document ID
        case f.Source != "":
            // Filter by source URL
        case f.CreatedAfter > 0:
            // Filter by creation time
        }
    }
    // Run cosine similarity search on the filtered set
    return results, nil
}
```

`ScoredMessage` and `ScoredChunk` carry similarity scores:

```go
type ScoredMessage struct {
    Message
    Score float32  // 0 = unknown, (0,1] = cosine similarity
}
```

A score of 0 means the store doesn't compute similarity. Callers skip threshold filtering for score-0 results.

## Document Management

Stores must implement document lifecycle operations:

```go
func (s *Store) StoreDocument(ctx context.Context, doc oasis.Document, chunks []oasis.Chunk) error {
    // Store document metadata and its chunks with embeddings
    return nil
}

func (s *Store) ListDocuments(ctx context.Context) ([]oasis.Document, error) {
    // Return all stored documents (metadata only, no chunks)
    return docs, nil
}

func (s *Store) DeleteDocument(ctx context.Context, documentID string) error {
    // Delete document, its chunks, and any associated edges
    // Use cascading deletes or explicit cleanup
    return nil
}
```

## Optional: KeywordSearcher

Implement `KeywordSearcher` to enable hybrid retrieval (vector + keyword search via RRF):

```go
type KeywordSearcher interface {
    SearchChunksKeyword(ctx context.Context, query string, topK int, filters ...ChunkFilter) ([]ScoredChunk, error)
}
```

This is discovered via type assertion — if your store implements it, `HybridRetriever` uses it automatically. The shipped SQLite and libSQL stores use FTS5 for this. For Postgres, use `tsvector`.

```go
// compile-time check (optional capability)
var _ oasis.KeywordSearcher = (*Store)(nil)
```

## Optional: GraphStore

Implement `GraphStore` to support Graph RAG (knowledge graph traversal during retrieval):

```go
type GraphStore interface {
    StoreEdges(ctx context.Context, edges []ChunkEdge) error
    GetEdges(ctx context.Context, chunkIDs []string) ([]ChunkEdge, error)
    GetIncomingEdges(ctx context.Context, chunkIDs []string) ([]ChunkEdge, error)
    PruneOrphanEdges(ctx context.Context) (int, error)
}
```

Each `ChunkEdge` connects two chunks with a typed relationship and a weight:

```go
type ChunkEdge struct {
    SourceChunkID string
    TargetChunkID string
    Relation      RelationType  // "references", "elaborates", "depends_on", etc.
    Weight        float32       // 0-1, edge strength
}
```

Key implementation details:
- `StoreEdges` should upsert — if a source→target→relation triple already exists, update the weight
- `PruneOrphanEdges` removes edges where either chunk no longer exists
- `DeleteDocument` should cascade-delete edges involving the document's chunks

```go
// compile-time check (optional capability)
var _ oasis.GraphStore = (*Store)(nil)
```

## Implementing MemoryStore

For user memory support, implement `MemoryStore`:

```go
func (s *Store) UpsertFact(ctx context.Context, fact, category string, embedding []float32) error {
    // 1. Search for semantically similar existing facts (cosine > 0.85)
    // 2. If found: merge (increment confidence by 0.1, cap at 1.0)
    // 3. If not: insert new fact with confidence = 1.0
    return nil
}
```

Key behaviors to preserve:
- **Semantic deduplication** — similar facts (cosine > 0.85) merge, not duplicate
- **Confidence scoring** — new: 1.0, reinforced: +0.1, decayed: ×0.95 after 7 days
- **Pruning** — facts with confidence < 0.3 and age > 30 days are removed

## Chunk Metadata

Chunks may carry arbitrary metadata stored as JSON. If your backend supports JSON columns (Postgres JSONB, SQLite JSON1), store and query them:

```go
type ChunkMeta struct {
    Author    string `json:"author,omitempty"`
    Language  string `json:"language,omitempty"`
    PageNum   int    `json:"page_num,omitempty"`
    // ... any fields from MetadataExtractor
}
```

The `ByMeta(key, value)` chunk filter should query into this JSON field.

## See Also

- [Store Concept](../concepts/store.md) — full interface and schema reference
- [Memory Concept](../concepts/memory.md) — MemoryStore interface
- [Retrieval](../concepts/retrieval.md) — how retrievers use Store capabilities
