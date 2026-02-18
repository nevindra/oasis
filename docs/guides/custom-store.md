# Building a Custom Store

Implement the `Store` interface to add a new storage backend — Postgres, DynamoDB, or any database with vector search capabilities.

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

Search methods return scored results:

```go
type ScoredMessage struct {
    Message
    Score float32  // 0 = unknown, (0,1] = cosine similarity
}
```

A score of 0 means the store doesn't compute similarity. Callers skip threshold filtering for score-0 results.

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

## See Also

- [Store Concept](../concepts/store.md)
- [Memory Concept](../concepts/memory.md)
