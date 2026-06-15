# Store

## TL;DR

A Store is the persistence backbone for an Oasis agent: one interface that holds conversation threads, chat messages, document chunks, memory items, scheduled actions, and a key-value config bag. Two ready-to-use drivers — SQLite for local or single-server use, Postgres for production scale — are swapped by changing one constructor call.

| Compartment | What lives there | Typical use |
|---|---|---|
| **Threads** | Named conversation containers | Group messages by session or user |
| **Messages** | Individual user/assistant turns + optional embedding | Chat history, semantic recall |
| **Documents + Chunks** | Ingested content split into searchable pieces | RAG pipelines |
| **Memory items** | Typed key/value facts about an agent's world | Long-term agent memory |
| **Scheduled actions** | Cron-style tasks the agent owns | "Remind me every Monday" |
| **Config** | Arbitrary key-value pairs | Per-agent settings, feature flags |
| **Scores** | Scorer results (eval) | Quality-eval history |

---

## When to use which

- **SQLite** — local development, prototypes, single-process deployments, datasets under ~50 k vector entries. Pure-Go, no CGO, no external service needed. One file path gets you running.
- **Postgres** — production systems, multi-instance deployments, large corpora (millions of vectors), teams already running Postgres. Requires the `pgvector` extension. Gives you HNSW approximate nearest-neighbor search and native full-text indexing.
- **Rule of thumb** — start with SQLite. When you hit 50 k vector entries, a second writer, or a managed cloud database, switch to Postgres. The rest of your code does not change.

| Capability | SQLite | Postgres |
|---|---|---|
| Zero-dependency binary | Yes | No (needs `pgvector`) |
| CGO required | No | No |
| Vector search engine | Brute-force cosine (in-process) | HNSW (pgvector, on-disk) |
| Vector scale | Up to ~50 k entries before slowdown | Millions of entries |
| Full-text search engine | FTS5 | `tsvector` GIN |
| Multi-process writes | No (single writer) | Yes |
| Managed cloud DB | No | Yes (RDS, Supabase, Neon, …) |
| Pool warm-up on restart | Yes (in-memory index rebuilt) | No |
| Setup | One file path | DSN + `pgvector` extension |
| Constructor | `sqlite.New(path)` | `postgres.Open(ctx, dsn, opts...)` |

---

## Architecture

```mermaid
flowchart LR
    Agent["Agent\n(agent.WithStore)"]
    Memory["Memory layer\n(memory.New)"]
    RAG["RAG pipeline\n(rag package)"]
    SI["core.Store\ninterface"]
    SQLite["store/sqlite\ndriver"]
    Postgres["store/postgres\ndriver"]
    SQLDB[("SQLite\nfile")]
    PGDB[("Postgres\n+ pgvector")]

    KW["KeywordSearcher\n(FTS)"]
    GR["GraphStore\n(chunk edges)"]
    CP["CheckpointStore\n(ingest resume)"]

    Agent --> SI
    Memory --> SI
    RAG --> SI
    SI --> SQLite
    SI --> Postgres
    SQLite --> SQLDB
    Postgres --> PGDB

    SQLite -. "type assert" .-> KW
    SQLite -. "type assert" .-> GR
    SQLite -. "type assert" .-> CP
    Postgres -. "type assert" .-> KW
    Postgres -. "type assert" .-> GR
    Postgres -. "type assert" .-> CP
```

`core.Store` is the single contract that agents, the memory orchestrator, and the RAG pipeline all speak. Both drivers satisfy it fully. Optional capability interfaces — `KeywordSearcher`, `GraphStore`, `CheckpointStore`, and others — plug in via runtime type assertion: you call `store.(oasis.KeywordSearcher)` and check the boolean. If the backend supports it you get the richer API; if not, your code degrades gracefully. Neither branch requires importing a different package.

The solid arrows show data flow for every agent run. The dotted arrows show capability discovery — those paths are only taken when your code explicitly type-asserts. The RAG pipeline touches the Store directly for chunk writes and reads; the agent touches it for thread and message persistence; the memory layer uses the `ItemStore` sub-surface exposed via `s.Memory()`.

Both drivers run the same DDL on `Init` and expose the same call shapes. Internally, SQLite keeps an in-process vector index rebuilt from the chunk table on first query; Postgres delegates to pgvector's HNSW structures inside the database. The abstraction holds: swap the constructor, keep the code.

---

## Mental model

**Store as protocol.** `core.Store` is an interface, not a struct. You never implement it yourself — you pick a driver and pass it wherever a `core.Store` is expected: `agent.WithStore`, `memory.New`, the ingest pipeline. Everything downstream speaks the protocol; nothing downstream cares whether the cabinet is a file or a database cluster. This is the same boundary principle Oasis applies to every integration point (Provider, Tool, Processor): interfaces face outward, implementations stay in satellites.

**Capability split via optional interfaces.** The base `Store` interface covers what every backend must do: CRUD for threads, messages, documents/chunks, config, and scheduled actions. Richer capabilities — full-text keyword search, knowledge-graph edges, bidirectional edge traversal, checkpoint-based ingest resume — live on separate interfaces. Backends opt in by implementing those interfaces. Your code discovers support at runtime with a type assertion. This follows the same pattern Oasis uses everywhere: optional capabilities never land in the base interface, so adding a new capability never breaks existing Store implementations.

**Drivers implement what they support.** Both `store/sqlite` and `store/postgres` currently implement all optional interfaces. A future lightweight driver might skip graph storage; that is valid and safe — callers check, not assume. The pattern future-proofs code against custom Store implementations.

**Pluggable persistence backbone.** From the agent's perspective, `WithStore` is a single call. All memory reads, all message appends, all vector searches flow through that one handle. You can replace SQLite with Postgres (or a custom backend) without touching agent code, memory code, or RAG code. The interface is the only coupling point.

**Scheduled actions live in the Store.** Cron-style agent tasks — "remind me every Monday", "run this check every hour" — are stored as `ScheduledAction` rows alongside everything else. `GetDueScheduledActions(ctx, now)` returns the subset whose next-run timestamp has passed; the scheduler layer calls this on a tick, not on every agent invocation. This means scheduled tasks survive restarts: they are durable records, not in-memory timers.

**Key-value config is a first-class compartment.** `GetConfig` / `SetConfig` provide per-agent or per-deployment settings storage without requiring a separate table or service. Useful for feature flags, last-run timestamps, and tunable parameters that need to persist across restarts.

**Checkpoints for resumable work.** Long-running ingest pipelines can crash mid-flight. `CheckpointStore` — an optional interface on both drivers — saves progress at each pipeline stage (`Extracting`, `Chunking`, `Enriching`, `Embedding`, `Storing`, `Graphing`). The `ingest` package uses this automatically when the store supports it. If the store does not implement it, ingestion retries from the beginning — no crash, just a slower recovery. For custom pipelines, you interact with `CheckpointStore` directly.

---

## How it works step by step

1. **Construct the driver.** Call `sqlite.New("data/agent.db")` or `postgres.New(pool)` / `postgres.Open(ctx, dsn)`. No I/O happens yet. For Postgres, `WithEmbeddingDimension(n)` is required before `Init`.
2. **Run migrations.** Call `store.Init(ctx)`. This creates all tables and indexes using `IF NOT EXISTS` DDL — safe to call on every restart. Skipping `Init` means your tables do not exist.
3. **Attach to the agent.** Pass the store via `agent.WithStore(s)`. The agent uses it to persist conversation history for the active thread.
4. **Thread creation.** Create a `Thread` with `store.CreateThread`. A `Thread` groups messages by session or user, identified by its `ChatID`.
5. **Message writes.** Each assistant/user turn calls `store.StoreMessage`. The message carries a `ThreadID`, role, content, and an optional `Embedding` for semantic recall later.
6. **Message reads.** `store.GetMessages(ctx, threadID, limit)` returns the last N messages in ascending order (oldest first), used by the agent to reconstruct context window.
7. **Semantic message recall.** `store.SearchMessages(ctx, queryEmbedding, topK, chatID)` returns the most semantically similar past messages for a given user. The `chatID` parameter scopes results to a single user's threads, preventing cross-user leakage in multi-tenant deployments. Pass `""` to search globally (admin use only).
8. **Memory item persistence.** `s.Memory()` returns an `ItemStore` (lazily initialized, thread-safe). The memory orchestrator calls `Upsert` and `List` on it to maintain typed facts — preferences, summaries, entity records — scoped per user or session. This sub-surface is separate from the main `Store` interface; it lives on the concrete driver type (`*sqlite.Store`, `*postgres.Store`) and is not part of `core.Store`.
9. **Document ingestion.** `store.StoreDocument(ctx, doc, chunks)` writes a document and all its chunks atomically. Chunks carry pre-computed embeddings from the ingest pipeline. Deleting a document with `DeleteDocument` cascades to all its chunks automatically.
10. **Vector search.** `store.SearchChunks(ctx, embedding, topK, filters...)` runs similarity search — brute-force cosine in SQLite, HNSW in Postgres. `ChunkFilter` helpers (`ByDocument`, `BySource`, `ByMeta`, `CreatedAfter`, `ByExcludeDocument`) narrow the search space without touching the embedding. Multiple filters compose as AND.
11. **Keyword search (optional).** Type-assert to `oasis.KeywordSearcher` and call `SearchChunksKeyword` for FTS5 (SQLite) or `tsvector` GIN (Postgres) ranked keyword results. The same `ChunkFilter` values work here too.
12. **Checkpoint writes (optional).** Type-assert to `oasis.CheckpointStore` and call `SaveCheckpoint` / `LoadCheckpoint` to record ingest progress per pipeline stage. The `ingest` package does this automatically; for custom pipelines you call it yourself.
13. **Close on shutdown.** Call `store.Close()`. For SQLite this closes the connection pool. For Postgres created via `Open`, it closes the owned pool. For Postgres created via `New`, it is a no-op — the caller closes their pool.

---

## Capability interfaces

Both `store/sqlite` and `store/postgres` implement all of these. Discover them at runtime via type assertion — existing code never breaks when a custom backend does not implement an optional interface.

| Interface | What it adds | Discover via |
|---|---|---|
| `oasis.KeywordSearcher` | `SearchChunksKeyword` — FTS over chunk content | `s.(oasis.KeywordSearcher)` |
| `oasis.GraphStore` | `StoreEdges` / `GetEdges` / `GetIncomingEdges` / `PruneOrphanEdges` — knowledge-graph edges between chunks | `s.(oasis.GraphStore)` |
| `oasis.BidirectionalGraphStore` | `GetBothEdges` — fetch incoming + outgoing edges in one query (extends `GraphStore`) | `s.(oasis.BidirectionalGraphStore)` |
| `oasis.DocumentGetter` | `GetDocumentsByIDs` — batch document lookup, avoids N+1 | `s.(oasis.DocumentGetter)` |
| `oasis.DocumentMetaLister` | `ListDocumentMeta` — titles and timestamps without loading `Content` | `s.(oasis.DocumentMetaLister)` |
| `oasis.CheckpointStore` | `SaveCheckpoint` / `LoadCheckpoint` / `DeleteCheckpoint` / `ListCheckpoints` — resumable ingest | `s.(oasis.CheckpointStore)` |
| `core.ScoreStore` | `SaveScores` / `ListScores` / `GetScore` / `DeleteScores` — quality-eval score persistence (see [eval](../eval/index.md)) | `s.(core.ScoreStore)` |

The `oasis.*` interfaces above are re-exported from the root `oasis` package — you do not need to import `core` directly for those. `core.ScoreStore` is the exception: it lives in `github.com/nevindra/oasis/core` and is not re-exported at root; import `core` directly when type-asserting it.

### Choosing between `SearchMessages` and `SearchChunks`

Both methods perform vector similarity search, but they serve different layers:

- **`SearchMessages`** — semantic recall over conversation history. The memory orchestrator calls this to surface relevant past exchanges when building the context window for a new turn. Scoped by `chatID` to prevent cross-user leakage.
- **`SearchChunks`** — RAG retrieval over ingested document corpus. The RAG pipeline calls this to find the most relevant document fragments to include in a prompt. Supports rich `ChunkFilter` composition and an optional keyword-search path via `KeywordSearcher`.

In a typical agent both are active: `SearchMessages` brings in relevant conversation memory, `SearchChunks` brings in relevant knowledge-base content. The store handles both through the same interface with a single `Init` and `Close`.

---

## Common patterns and gotchas

- **Always call `Init` before use.** Both drivers require `Init(ctx)` to create tables and indexes. It is idempotent — calling it on every startup is safe and the recommended pattern.
- **`ThreadID` partitions conversation history.** Messages are scoped to a thread. `SearchMessages` accepts a `chatID` parameter to prevent cross-user contamination in multi-tenant systems — pass `""` only for admin tooling, not for per-user agents.
- **SQLite is single-writer.** WAL mode and a 5-second busy timeout handle short contention, but concurrent writers from separate processes will collide. SQLite is for single-process deployments only.
- **Postgres needs the `pgvector` extension.** Run `CREATE EXTENSION IF NOT EXISTS vector` before calling `Init`. Without it, `Init` will fail. Managed services like Supabase and Neon include it by default; RDS requires enabling it manually.
- **`WithEmbeddingDimension` is required for Postgres `Init`.** Omitting it returns an error. The dimension must match your embedding model's output size (e.g. 1536 for `text-embedding-3-small`, 3072 for `text-embedding-3-large`).
- **`Open` vs `New` for Postgres.** `postgres.Open` owns its pool and closes it on `store.Close()`. `postgres.New` borrows an externally managed pool — `store.Close()` is a no-op, and `pool.Close()` is the caller's responsibility. Mismatching these leaks or double-closes the pool.
- **`s.Memory()` is lazy.** The `ItemStore` is initialized on first call to `s.Memory()`. It is safe to call concurrently from multiple goroutines; initialization happens exactly once. Do not hold the returned pointer across `store.Close()`.
- **SQLite in-memory vector index is rebuilt on first query.** After a process restart, the first `SearchChunks` call (or `SearchMessages` with an embedding) will scan the chunk table to rebuild the in-process vector index. For large datasets this adds latency to the first query. Postgres has no such warm-up cost — HNSW is on-disk and always ready.

---

## Quick example

```go
import (
    "context"
    "log"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/memory"
    "github.com/nevindra/oasis/store/sqlite"
)

func main() {
    ctx := context.Background()

    // 1. Construct the driver and run migrations (idempotent).
    s := sqlite.New("data/agent.db")
    if err := s.Init(ctx); err != nil {
        log.Fatal(err)
    }
    defer s.Close()

    // 2. Build the agent — store and memory share the same SQLite file.
    a := agent.New(provider,
        agent.WithStore(s),
        agent.WithMemory(memory.New(embedder, s.Memory())),
    )

    // 3. Execute — conversation history is persisted automatically.
    result, err := a.Execute(ctx, agent.Task{Input: "Hello"})
    _ = result
    _ = err
}
```

`s.Memory()` is lazily initialized on first call and backed by the same file. One `Init`, one `Close`, one file — the agent, the memory orchestrator, and the RAG pipeline all share the same handle.

To swap in Postgres, replace the three `sqlite` lines with:

```go
import "github.com/nevindra/oasis/store/postgres"

s, err := postgres.Open(ctx,
    "postgres://user:pass@localhost:5432/agentdb",
    postgres.WithEmbeddingDimension(1536), // must match your embedder's output dimension
)
if err != nil {
    log.Fatal(err)
}
defer s.Close() // Open owns the pool; Close releases it
```

Everything else — `agent.WithStore(s)`, `s.Memory()`, `SearchChunks`, `SearchMessages` — stays identical. The `WithEmbeddingDimension` value must match the dimension of the vectors your embedder produces; passing the wrong value causes `Init` to return an error with a diagnostic message.

---

## Next

- [API reference](./api.md)
- [Examples](./examples.md)
