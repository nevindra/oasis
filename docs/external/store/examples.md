# store examples

Copy-paste recipes. All imports use the root package alias `oasis` where the types are re-exported; the actual implementation is in the `store/sqlite` or `store/postgres` sub-packages.

---

## SQLite — minimal setup

```go
import (
    "context"
    "log"

    "github.com/nevindra/oasis/store/sqlite"
)

func main() {
    ctx := context.Background()

    s := sqlite.New("data/agent.db")
    if err := s.Init(ctx); err != nil {
        log.Fatal(err)
    }
    defer s.Close()

    // s is now ready — pass to agent.WithStore(s), rag pipeline, etc.
}
```

`sqlite.New` only opens the driver. `Init` runs the DDL. Both steps are required on first run; subsequent starts are idempotent.

---

## SQLite — with logging and a bounded vector index

```go
import (
    "log/slog"

    "github.com/nevindra/oasis/store/sqlite"
)

logger := slog.Default()

s := sqlite.New("data/agent.db",
    sqlite.WithLogger(logger),
    sqlite.WithMaxVecEntries(25_000), // evict oldest docs above 25k chunks
)
```

`WithMaxVecEntries` prevents the in-memory vector index from growing unbounded when you're ingesting large corpora. Evicted chunks are still searchable — just slower (disk fallback). Set this when your dataset is between 25 k–50 k chunks and RAM is a concern.

---

## Postgres — using `Open` (simplest)

```go
import (
    "context"
    "log"

    "github.com/nevindra/oasis/store/postgres"
)

func main() {
    ctx := context.Background()

    s, err := postgres.Open(ctx,
        "postgres://user:pass@localhost:5432/agentdb",
        postgres.WithEmbeddingDimension(1536), // match your embedding model's output size
    )
    if err != nil {
        log.Fatal(err)
    }
    defer s.Close() // owns the pool, will close it

    // s is ready — pgvector extension and all tables were created by Open
}
```

`Open` wraps `New` + `Init` into one call. Use it when Oasis owns the connection pool. `WithEmbeddingDimension` is required; it tells pgvector how to size the HNSW index columns.

---

## Postgres — caller-managed pool

When you share a connection pool across multiple components (e.g. your own database models alongside Oasis), supply the pool yourself:

```go
import (
    "context"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/nevindra/oasis/store/postgres"
)

// Build the pool config
cfg, _ := pgxpool.ParseConfig("postgres://user:pass@localhost/agentdb")

// Inject ef_search so every connection in the pool has it set
postgres.ConfigurePoolConfig(cfg,
    postgres.WithEmbeddingDimension(1536),
    postgres.WithEFSearch(80), // higher recall, trades some latency
)

pool, _ := pgxpool.NewWithConfig(ctx, cfg)
defer pool.Close() // your code owns this

s := postgres.New(pool,
    postgres.WithEmbeddingDimension(1536),
    postgres.WithHNSWM(32),             // denser graph = better recall
    postgres.WithEFConstruction(128),   // slower build, better index quality
)
if err := s.Init(ctx); err != nil {
    // handle
}
// s.Close() is a no-op here — pool.Close() above handles cleanup
```

Calling `ConfigurePoolConfig` before `pgxpool.NewWithConfig` ensures `hnsw.ef_search` is applied to every new connection, not just the first one. Without this, `WithEFSearch` has no effect.

---

## Storing and retrieving messages

```go
import (
    "github.com/nevindra/oasis/core"
    "github.com/google/uuid"
)

threadID := uuid.New().String()

// Create a thread
if err := s.CreateThread(ctx, core.Thread{
    ID:     threadID,
    ChatID: "user-42",
    Title:  "Support session",
}); err != nil { /* handle */ }

// Store a message
if err := s.StoreMessage(ctx, core.Message{
    ID:       uuid.New().String(),
    ThreadID: threadID,
    Role:     "user",
    Content:  "How do I reset my password?",
}); err != nil { /* handle */ }

// Retrieve the last 20 messages
msgs, err := s.GetMessages(ctx, threadID, 20)
```

`GetMessages` returns messages in ascending order (oldest first), up to `limit`. Pass `0` for no limit.

---

## Semantic search across messages

```go
// Assume you have a []float32 embedding for the current query
queryEmbedding := embed("How do I reset my password?")

results, err := s.SearchMessages(ctx, queryEmbedding, 5, "user-42")
// results is []core.ScoredMessage sorted by Score descending
for _, r := range results {
    fmt.Printf("%.3f  %s\n", r.Score, r.Content)
}
```

`chatID = "user-42"` scopes results to that user's threads. Pass `""` to search across all messages — useful for admin tooling, not for per-user agents.

---

## Vector search over document chunks

```go
import "github.com/nevindra/oasis/core"

queryEmbedding := embed("HNSW index parameters")

// Simple vector search
chunks, err := s.SearchChunks(ctx, queryEmbedding, 5)

// Filtered to a specific document
chunks, err = s.SearchChunks(ctx, queryEmbedding, 5,
    core.ByDocument("doc-abc123"),
)

// Exclude a specific document and restrict by metadata section
chunks, err = s.SearchChunks(ctx, queryEmbedding, 5,
    core.ByExcludeDocument("draft-doc"),
    core.ByMeta("section_heading", "Configuration"),
)
```

Filters compose as AND. Pass multiple `ChunkFilter` values to narrow the search space without touching the embedding.

---

## Keyword search (optional capability)

```go
import "github.com/nevindra/oasis"

ks, ok := s.(oasis.KeywordSearcher)
if !ok {
    // store doesn't support FTS — fall back to vector-only
}

results, err := ks.SearchChunksKeyword(ctx, "HNSW ef_construction", 10)
```

Both SQLite and Postgres implement `KeywordSearcher`. The type assertion is a runtime check — it does nothing more than confirm the backend supports it. You will never get `ok = false` with the built-in backends, but the pattern future-proofs your code against custom Store implementations.

---

## Accessing memory items (ItemStore)

The `Memory()` method returns a `*sqlite.ItemStore` (or `*postgres.ItemStore`) that implements `memory.ItemStore`:

```go
import "github.com/nevindra/oasis/memory"

itemStore := s.Memory() // lazily initialized, safe to call concurrently

// Upsert a memory item
err := itemStore.Upsert(ctx, memory.MemoryItem{
    ID:      "fact-001",
    Kind:    memory.KindFact,
    Content: "User prefers dark mode",
    Scope:   memory.Scope{Kind: memory.ScopeUser, Ref: "user-42"},
})

// List items for a user
items, err := itemStore.List(ctx, memory.Filter{
    Scope: &memory.Scope{Kind: memory.ScopeUser, Ref: "user-42"},
    Limit: 20,
})
```

`Memory()` is initialized the first time you call it; the underlying table is created automatically.

---

## Checkpoint a long-running ingest

If you're running the `ingest` pipeline and want crash-resumable ingestion:

```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/core"
)

cs, ok := s.(oasis.CheckpointStore)
if ok {
    cp := core.IngestCheckpoint{
        ID:     "ingest-xyz",
        Type:   "document",
        Source: "manual.pdf",
        Status: core.CheckpointExtracting,
    }
    _ = cs.SaveCheckpoint(ctx, cp)
}
```

The `ingest` package does this automatically when the store implements `CheckpointStore`. You only need to interact with it directly for custom ingestion pipelines.

---

## Attaching to an agent

```go
import (
    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/store/sqlite"
)

s := sqlite.New("data/agent.db")
_ = s.Init(ctx)
defer s.Close()

a := agent.New(provider,
    agent.WithStore(s),
    agent.WithMemory(
        memory.New(embedder, s.Memory()),
    ),
)
```

`agent.WithStore` wires up conversation persistence. `s.Memory()` gives the memory orchestrator its item store. Both use the same underlying SQLite file — one connection, one `Init`, one `Close`.

---

## See also

- [store concept](index.md) — when to use SQLite vs Postgres
- [store API reference](api.md) — full interface and type definitions
