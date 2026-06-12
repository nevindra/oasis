# store API reference

All types in this document live in `github.com/nevindra/oasis/core` and are re-exported at `github.com/nevindra/oasis` (the root package). You do not need to import `core` directly.

---

## `Store` interface

`Store` is the single contract every persistence backend must satisfy. Pass it to agents, memory orchestration, and the ingest pipeline. Construct one of the concrete implementations below — you never implement this yourself.

```go
type Store interface {
    // Threads
    CreateThread(ctx context.Context, thread Thread) error
    GetThread(ctx context.Context, id string) (Thread, error)
    ListThreads(ctx context.Context, chatID string, limit int) ([]Thread, error)
    UpdateThread(ctx context.Context, thread Thread) error
    DeleteThread(ctx context.Context, id string) error

    // Messages
    StoreMessage(ctx context.Context, msg Message) error
    GetMessages(ctx context.Context, threadID string, limit int) ([]Message, error)
    SearchMessages(ctx context.Context, embedding []float32, topK int, chatID string) ([]ScoredMessage, error)

    // Documents + Chunks
    StoreDocument(ctx context.Context, doc Document, chunks []Chunk) error
    ListDocuments(ctx context.Context, limit int) ([]Document, error)
    DeleteDocument(ctx context.Context, id string) error
    SearchChunks(ctx context.Context, embedding []float32, topK int, filters ...ChunkFilter) ([]ScoredChunk, error)
    GetChunksByIDs(ctx context.Context, ids []string) ([]Chunk, error)

    // Key-value config
    GetConfig(ctx context.Context, key string) (string, error)
    SetConfig(ctx context.Context, key, value string) error

    // Scheduled actions
    CreateScheduledAction(ctx context.Context, action ScheduledAction) error
    ListScheduledActions(ctx context.Context) ([]ScheduledAction, error)
    GetDueScheduledActions(ctx context.Context, now int64) ([]ScheduledAction, error)
    UpdateScheduledAction(ctx context.Context, action ScheduledAction) error
    UpdateScheduledActionEnabled(ctx context.Context, id string, enabled bool) error
    DeleteScheduledAction(ctx context.Context, id string) error
    DeleteAllScheduledActions(ctx context.Context) (int, error)
    ListScheduledActionsByDescription(ctx context.Context, pattern string) ([]ScheduledAction, error)

    // Lifecycle
    Init(ctx context.Context) error
    Close() error
}
```

**`SearchMessages`** — semantic similarity search. `chatID` scopes results to a single user/chat; pass `""` to search all messages. Results are `ScoredMessage` sorted by cosine similarity descending.

**`StoreDocument`** — stores the document record and all its chunks atomically. Chunk embeddings should be populated before calling this.

**`DeleteDocument`** — cascades to all chunks owned by the document.

**`Init`** — creates tables and indexes; safe to call on every startup (all DDL is `IF NOT EXISTS`).

**`Close`** — releases the underlying connection. Required for Postgres pools created via `Open`; no-op for Postgres pools created via `New`.

---

## Domain types

### `Thread`

A named conversation container. Group messages by session, user, or topic.

```go
type Thread struct {
    ID        string            // UUIDv7 recommended
    ChatID    string            // external session/user identifier
    Title     string            // optional display name
    Metadata  map[string]string // arbitrary key-value labels
    CreatedAt int64             // Unix timestamp
    UpdatedAt int64             // Unix timestamp
}
```

### `Message`

One turn in a conversation thread.

```go
type Message struct {
    ID        string
    ThreadID  string
    Role      string         // "user" or "assistant"
    Content   string
    Metadata  map[string]any // arbitrary extras
    Embedding []float32      // omitted from JSON; populated by the ingest/memory layer
    CreatedAt int64
}
```

`Embedding` is not serialized to JSON — it is stored separately as a binary blob and never leaks into API responses.

### `ScoredMessage`

Returned by `SearchMessages`. `Score` is cosine similarity in `[0, 1]`; higher means more relevant.

```go
type ScoredMessage struct {
    Message
    Score float32
}
```

### `Document`

A source document that has been ingested.

```go
type Document struct {
    ID        string
    Title     string
    Source    string // filename, URL, or other identifier
    Content   string // full text (may be large)
    CreatedAt int64
}
```

### `Chunk`

A piece of a document, ready for vector search.

```go
type Chunk struct {
    ID         string
    DocumentID string
    ParentID   string     // non-empty for hierarchical chunking
    Content    string
    ChunkIndex int
    Embedding  []float32  // not serialized to JSON
    Metadata   *ChunkMeta // optional structured metadata
}
```

### `ChunkMeta`

Optional metadata extracted during document ingestion.

```go
type ChunkMeta struct {
    PageNumber     int
    SectionHeading string
    SourceURL      string
    Images         []Image
    ContentType    string // "text" (default) or "image"
    BlobRef        string // external image reference e.g. "s3://bucket/key"
}
```

### `ScoredChunk`

Returned by `SearchChunks`. Same `Score` semantics as `ScoredMessage`.

```go
type ScoredChunk struct {
    Chunk
    Score float32
}
```

### `ChunkFilter`

Restricts which chunks are considered during vector or keyword search.

```go
type ChunkFilter struct {
    Field string   // "document_id", "source", "created_at", "meta.<key>"
    Op    FilterOp // OpEq, OpIn, OpGt, OpLt, OpNeq
    Value any
}
```

**Constructor helpers** (prefer these over constructing `ChunkFilter` by hand):

```go
ByDocument(ids ...string) ChunkFilter      // chunks belonging to specific documents
BySource(source string) ChunkFilter        // chunks from documents with matching source
ByMeta(key, value string) ChunkFilter      // meta.<key> == value
ByExcludeDocument(docID string) ChunkFilter
CreatedAfter(unix int64) ChunkFilter
CreatedBefore(unix int64) ChunkFilter
```

---

## Optional capability interfaces

Both `store/sqlite` and `store/postgres` implement all of these. Discover them via type assertion at runtime — existing code never breaks if a backend doesn't implement an optional interface.

### `KeywordSearcher`

Full-text keyword search over chunk content (FTS5 on SQLite, `tsvector` GIN on Postgres).

```go
type KeywordSearcher interface {
    SearchChunksKeyword(ctx context.Context, query string, topK int, filters ...ChunkFilter) ([]ScoredChunk, error)
}
```

Usage:

```go
if ks, ok := store.(oasis.KeywordSearcher); ok {
    results, err := ks.SearchChunksKeyword(ctx, "neural networks", 10)
}
```

### `GraphStore`

Knowledge-graph relationships between chunks — used by the Graph RAG retriever.

```go
type GraphStore interface {
    StoreEdges(ctx context.Context, edges []ChunkEdge) error
    GetEdges(ctx context.Context, chunkIDs []string) ([]ChunkEdge, error)
    GetIncomingEdges(ctx context.Context, chunkIDs []string) ([]ChunkEdge, error)
    PruneOrphanEdges(ctx context.Context) (int, error)
}
```

### `BidirectionalGraphStore`

Extends `GraphStore` by fetching both outgoing and incoming edges in one query, halving database round-trips per hop.

```go
type BidirectionalGraphStore interface {
    GetBothEdges(ctx context.Context, chunkIDs []string) ([]ChunkEdge, error)
}
```

### `DocumentGetter`

Batch document lookup by ID — avoids N+1 queries in the ingest and RAG layers.

```go
type DocumentGetter interface {
    GetDocumentsByIDs(ctx context.Context, ids []string) ([]Document, error)
}
```

### `DocumentMetaLister`

Returns document titles and timestamps without loading the (potentially large) `Content` field.

```go
type DocumentMetaLister interface {
    ListDocumentMeta(ctx context.Context, limit int) ([]Document, error)
}
```

### `CheckpointStore`

Ingest pipeline checkpointing — allows a crashed ingestion to resume from the last completed stage rather than starting from scratch. If the store does not implement this interface, checkpointing is silently disabled and failed ingestions are retried from the beginning.

```go
type CheckpointStore interface {
    SaveCheckpoint(ctx context.Context, cp IngestCheckpoint) error
    LoadCheckpoint(ctx context.Context, id string) (IngestCheckpoint, error)
    DeleteCheckpoint(ctx context.Context, id string) error
    ListCheckpoints(ctx context.Context) ([]IngestCheckpoint, error)
}
```

Checkpoint statuses (`CheckpointStatus`): `CheckpointExtracting`, `CheckpointChunking`, `CheckpointEnriching`, `CheckpointEmbedding`, `CheckpointStoring`, `CheckpointGraphing`.

---

## `ChunkEdge`

A directed, weighted relationship between two chunks in a knowledge graph.

```go
type ChunkEdge struct {
    ID          string
    SourceID    string
    TargetID    string
    Relation    RelationType
    Weight      float32      // strength of the relationship, 0.0–1.0
    Description string
}
```

Predefined `RelationType` constants: `RelReferences`, `RelElaborates`, `RelDependsOn`, `RelContradicts`, `RelPartOf`, `RelSimilarTo`, `RelSequence`, `RelCausedBy`.

---

## SQLite backend

Import: `github.com/nevindra/oasis/store/sqlite`

### `New(dbPath string, opts ...StoreOption) *Store`

Creates a store backed by the file at `dbPath`. Does not call `Init` — you must call it before use. The file is created if it does not exist. WAL journal mode and a 5-second busy timeout are applied via DSN pragmas.

### `StoreOption` functions

| Option | Effect |
|---|---|
| `WithLogger(l *slog.Logger)` | Emit debug logs for every operation (timing, row counts). Default: silent. |
| `WithMaxVecEntries(n int)` | Cap the in-memory vector index at `n` entries. Oldest documents are evicted FIFO; evicted chunks fall back to a slower disk path. Default `0` = unlimited. |

### `(*Store).Memory() *ItemStore`

Returns the memory item store, initialized lazily on first call. Thread-safe.

### `(*Store).DB() *sql.DB`

Returns the underlying `*sql.DB` for advanced use (e.g. sharing a connection with a custom table). Avoid holding long-lived references.

---

## Postgres backend

Import: `github.com/nevindra/oasis/store/postgres`

### `New(pool *pgxpool.Pool, opts ...Option) *Store`

Creates a store using an existing connection pool. The caller owns the pool and is responsible for closing it. Does not call `Init`.

### `Open(ctx context.Context, dsn string, opts ...Option) (*Store, error)`

Creates a store from a DSN string. Opens a new pool, calls `Init` automatically, and sets `ownedPool = true` so `Close` will close the pool.

### `ConfigurePoolConfig(cfg *pgxpool.Config, opts ...Option)`

Applies store-level settings (currently `hnsw.ef_search`) to a `pgxpool.Config` via `AfterConnect`. Call this before `pgxpool.NewWithConfig` when you need `ef_search` to be set on every connection.

### `Option` functions

| Option | Effect |
|---|---|
| `WithEmbeddingDimension(dim int)` | **Required for Init.** Sets vector column type to `vector(N)`, enabling HNSW index optimization and dimension validation at insert time. |
| `WithLogger(l *slog.Logger)` | Same as SQLite. |
| `WithHNSWM(m int)` | HNSW `m` parameter — max connections per node. Higher = better recall, more memory. Default: pgvector's 16. |
| `WithEFConstruction(ef int)` | HNSW build-time candidate list size. Higher = better index quality, slower build. Default: pgvector's 64. |
| `WithEFSearch(ef int)` | HNSW query-time candidate list size. Higher = better recall, more latency. Default: pgvector's 40. |

`WithEmbeddingDimension` is required when calling `Init`. Without it, `Init` returns an error.

### `(*Store).Memory() *ItemStore`

Same as SQLite — lazily initialized, thread-safe.

### `(*Store).Close() error`

Closes the pool only when the store was created via `Open`. When created via `New`, this is a no-op — the caller closes their pool.
