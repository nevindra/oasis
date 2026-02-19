# Retrieval

The retrieval pipeline searches ingested documents and returns ranked, context-rich results. It composes vector search, full-text keyword search (FTS5), parent-child chunk resolution, and optional re-ranking into a single `Retriever.Retrieve()` call.

## Pipeline

```mermaid
flowchart LR
    Q["Query string"] --> EMB["EmbeddingProvider"]
    EMB --> VEC["Vector search<br>(Store.SearchChunks)"]
    EMB --> KW["Keyword search<br>(FTS5, optional)"]
    VEC --> RRF["Reciprocal Rank<br>Fusion"]
    KW --> RRF
    RRF --> PC["Parent-child<br>resolution"]
    PC --> RR["Reranker<br>(optional)"]
    RR --> FILTER["Score filter<br>+ topK trim"]
    FILTER --> OUT["[]RetrievalResult"]
```

## Retriever Interface

**File:** `retriever.go`

```go
type Retriever interface {
    Retrieve(ctx context.Context, query string, topK int) ([]RetrievalResult, error)
}
```

Any component that needs search results depends on `Retriever` — not on a specific implementation. The `KnowledgeTool`, for example, accepts any `Retriever` via its constructor.

### RetrievalResult

```go
type RetrievalResult struct {
    Content        string  `json:"content"`
    Score          float32 `json:"score"`
    ChunkID        string  `json:"chunk_id"`
    DocumentID     string  `json:"document_id"`
    DocumentTitle  string  `json:"document_title"`
    DocumentSource string  `json:"document_source"`
}
```

Score is in [0, 1]; higher means more relevant. The exact range depends on the scoring method (cosine similarity, RRF, or reranker output).

## HybridRetriever (Default)

`HybridRetriever` is the shipped implementation. It combines multiple search strategies for better recall than vector-only search.

```go
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithKeywordWeight(0.3),
    oasis.WithOverfetchMultiplier(3),
    oasis.WithReranker(oasis.NewScoreReranker(0.1)),
    oasis.WithMinRetrievalScore(0.05),
)
```

### How It Works

1. **Embed the query** — calls `EmbeddingProvider.Embed()` on the query string.
2. **Vector search** — `Store.SearchChunks()` returns top candidates by cosine similarity.
3. **Keyword search** (optional) — if the Store implements `KeywordSearcher`, FTS5 results are fetched in parallel.
4. **Reciprocal Rank Fusion** — merges vector and keyword results by rank position, not raw scores. This avoids score-scale mismatches between the two search methods.
5. **Parent-child resolution** — child chunks matched by embedding are replaced with their parent's richer content. If multiple children share a parent, the highest-scored child wins.
6. **Re-ranking** (optional) — a `Reranker` re-scores results for precision.
7. **Score filter + trim** — drops results below `minScore` and trims to `topK`.

### Overfetching

The retriever fetches `topK * overfetchMultiplier` candidates from each search method, then trims after merging and re-ranking. This ensures enough candidates survive filtering. Default multiplier is 3.

## Reciprocal Rank Fusion (RRF)

RRF merges two ranked lists using the formula:

```
score(d) = Σ weight / (k + rank + 1)
```

Where `k = 60` (standard constant). This produces stable scores regardless of the original scoring scales. The `keywordWeight` parameter (default 0.3) controls the balance — vector search gets weight `1 - keywordWeight`.

## KeywordSearcher (FTS5)

Keyword search is an optional Store capability discovered via type assertion:

```go
type KeywordSearcher interface {
    SearchChunksKeyword(ctx context.Context, query string, topK int) ([]ScoredChunk, error)
}
```

Both `store/sqlite` and `store/libsql` implement this interface using SQLite FTS5. The FTS index is populated automatically when documents are stored via `StoreDocument()`.

If a Store doesn't implement `KeywordSearcher`, `HybridRetriever` falls back to vector-only search — no error, no configuration needed.

## Reranker Interface

```go
type Reranker interface {
    Rerank(ctx context.Context, query string, results []RetrievalResult, topK int) ([]RetrievalResult, error)
}
```

The returned slice must be sorted by Score descending and trimmed to topK.

### ScoreReranker

Filters results below a minimum score and re-sorts. No external calls — useful as a baseline.

```go
reranker := oasis.NewScoreReranker(0.1) // drop results below 0.1
```

### LLMReranker

Uses an LLM to score query-document relevance on a 0-10 scale, then normalizes and re-sorts. On LLM failure, results pass through unmodified (graceful degradation).

```go
reranker := oasis.NewLLMReranker(llmProvider)
```

## Parent-Child Resolution

When using `StrategyParentChild` during [ingestion](ingest.md), child chunks are small and precisely embedded, while parent chunks are large and context-rich. The retriever resolves this automatically:

```mermaid
graph TB
    SEARCH["Vector search matches<br>child chunks"] --> RESOLVE["GetChunksByIDs()<br>find ParentID"]
    RESOLVE --> PARENT["GetChunksByIDs()<br>fetch parent content"]
    PARENT --> RESULT["Return parent content<br>with child's score"]
```

- If a child has no `ParentID`, it passes through unchanged.
- If multiple children map to the same parent, only the highest-scored child's result is kept.
- On any error, results pass through unmodified (graceful degradation).

## Configuration Options

| Option | Default | Description |
|--------|---------|-------------|
| `WithReranker(r)` | nil | Re-ranking stage after hybrid merge |
| `WithMinRetrievalScore(s)` | 0 | Drop results below this score |
| `WithKeywordWeight(w)` | 0.3 | Keyword weight in RRF (vector gets 1-w) |
| `WithOverfetchMultiplier(n)` | 3 | Fetch topK*n candidates before trim |

## See Also

- [Ingest](ingest.md) — the ingestion pipeline that creates searchable chunks
- [Store](store.md) — persistence and vector search
- [RAG Pipeline Guide](../guides/rag-pipeline.md) — end-to-end walkthrough
- [Provider](provider.md) — EmbeddingProvider for query embedding
