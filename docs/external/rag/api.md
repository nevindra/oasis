# RAG API

Two packages make up the RAG surface:

- `github.com/nevindra/oasis/rag` — retrieval (root module)
- `github.com/nevindra/oasis/ingest` — ingestion (satellite module, own `go.mod`)

---

## Types

### `rag.RetrievalResult`

A single ranked result from a retrieval call.

| Field | Type | Description |
|---|---|---|
| `Content` | `string` | The chunk text (or parent text when parent-child strategy is used). |
| `Score` | `float32` | Relevance score in [0, 1]. Higher is more relevant. |
| `ChunkID` | `string` | ID of the stored chunk. |
| `ParentID` | `string` | Non-empty when this chunk has a parent (parent-child strategy). |
| `DocumentID` | `string` | ID of the source document. |
| `DocumentTitle` | `string` | Human-readable document title. Empty when the store doesn't implement `core.DocumentGetter`. |
| `DocumentSource` | `string` | The source path or URL used at ingest time. |
| `GraphContext` | `[]EdgeContext` | Non-nil when the chunk was discovered via graph traversal (`GraphRetriever` only). |

### `rag.EdgeContext`

Describes the graph edge that led to a graph-discovered chunk.

| Field | Type | Description |
|---|---|---|
| `FromChunkID` | `string` | ID of the chunk that pointed here. |
| `Relation` | `core.RelationType` | Edge type: one of `RelReferences`, `RelElaborates`, `RelDependsOn`, `RelContradicts`, `RelPartOf`, `RelSequence`, `RelCausedBy`. |
| `Description` | `string` | One-sentence description of the relationship, written by the LLM at ingest time. |

### `ingest.IngestResult`

Returned by all `Ingestor.Ingest*` methods.

| Field | Type | Description |
|---|---|---|
| `DocumentID` | `string` | Stable ID for the stored document. |
| `Document` | `core.Document` | Full document record. |
| `ChunkCount` | `int` | Number of chunks stored (includes both parent and child chunks for `StrategyParentChild`). |

### `ingest.ContentType`

Typed string identifying the document format.

| Constant | Extension |
|---|---|
| `TypePlainText` | `.txt` (default for unknown extensions) |
| `TypeHTML` | `.html`, `.htm` |
| `TypeMarkdown` | `.md`, `.markdown` |
| `TypeCSV` | `.csv` |
| `TypeJSON` | `.json` |
| `TypeDOCX` | `.docx` |
| `TypePDF` | `.pdf` |

`ContentTypeFromExtension(ext string) ContentType` maps a bare extension (no dot) to the matching constant.

### `ingest.ChunkStrategy`

| Constant | Behavior |
|---|---|
| `StrategyFlat` | Single-level chunks. Default. |
| `StrategyParentChild` | Two-level hierarchy. Children (256 tokens) are embedded for precise matching; parents (1024 tokens) are returned at retrieval time. |

---

## Interfaces

### `rag.Retriever`

```go
type Retriever interface {
    Retrieve(ctx context.Context, query string, topK int) ([]RetrievalResult, error)
}
```

Both `HybridRetriever` and `GraphRetriever` satisfy this interface. Thread-safe.

### `rag.Reranker`

```go
type Reranker interface {
    Rerank(ctx context.Context, query string, results []RetrievalResult, topK int) ([]RetrievalResult, error)
}
```

Must return results sorted by `Score` descending and trimmed to `topK`. On failure, implementations should return the original results unmodified (degrade gracefully).

### `ingest.Chunker` / `ingest.ContextChunker`

```go
type Chunker interface {
    Chunk(text string) []string
}

type ContextChunker interface {
    Chunker
    ChunkContext(ctx context.Context, text string) ([]string, error)
}
```

The `Ingestor` calls `ChunkContext` when available and falls back to `Chunk` otherwise. Implement `ContextChunker` for chunkers that call external services.

### `ingest.Extractor` / `ingest.MetadataExtractor`

```go
type Extractor interface {
    Extract(content []byte) (string, error)
}

type MetadataExtractor interface {
    ExtractWithMeta(content []byte) (ExtractResult, error)
}
```

If an `Extractor` also implements `MetadataExtractor`, the ingestor uses `ExtractWithMeta` to capture per-page metadata (page numbers, headings, images).

---

## Constructors

### `ingest.NewIngestor`

```go
func NewIngestor(store core.Store, emb core.EmbeddingProvider, opts ...Option) *Ingestor
```

Defaults: `StrategyFlat`, `RecursiveChunker` (512 tokens, 50-token overlap), batch size 64, max content 50 MB. All built-in extractors are registered automatically.

### `rag.NewHybridRetriever`

```go
func NewHybridRetriever(store core.Store, embedding core.EmbeddingProvider, opts ...RetrieverOption) *HybridRetriever
```

Defaults: keyword weight 0.3, overfetch multiplier 3. Keyword search runs automatically when the store implements `core.KeywordSearcher`.

### `rag.NewGraphRetriever`

```go
func NewGraphRetriever(store core.Store, embedding core.EmbeddingProvider, cfg GraphRetrieverConfig) *GraphRetriever
```

Accepts a `GraphRetrieverConfig` struct (not variadic options). The zero value reproduces all defaults: `MaxHops=2`, `VectorWeight=0.7`, `GraphWeight=0.3`, `HopDecay={1.0, 0.7, 0.5}`, `SeedTopK=10`. Falls back to vector-only when the store doesn't implement `core.GraphStore`.

### `rag.NewScoreReranker`

```go
func NewScoreReranker(minScore float32) *ScoreReranker
```

Drops results below `minScore`, sorts descending, trims to topK. No external calls.

### `rag.NewLLMReranker`

```go
func NewLLMReranker(provider core.Provider) *LLMReranker
```

Sends candidates to the LLM for 0-10 relevance scoring. Default timeout: 2 minutes. Degrades gracefully on LLM failure.

### Built-in chunkers

| Constructor | Strategy |
|---|---|
| `ingest.NewRecursiveChunker(opts...)` | Paragraph → sentence → word (handles abbreviations, CJK punctuation). |
| `ingest.NewMarkdownChunker(opts...)` | Splits at heading boundaries (`#`, `##`, etc.); merges small sections. |
| `ingest.NewSemanticChunker(embed, opts...)` | Splits where consecutive-sentence cosine similarity drops below the Nth percentile. |

---

## Methods

### `Ingestor.IngestFile`

```go
func (ing *Ingestor) IngestFile(ctx context.Context, content []byte, filename string) (IngestResult, error)
```

Detects content type from the filename extension. Enforces `maxContentSize`. Returns a wrapped error on extraction, embedding, or storage failure. Thread-safe.

### `Ingestor.IngestText`

```go
func (ing *Ingestor) IngestText(ctx context.Context, text, source, title string) (IngestResult, error)
```

Ingests pre-extracted plain text. `source` is stored as the document source URL/path; `title` as the document title.

### `Ingestor.IngestReader`

```go
func (ing *Ingestor) IngestReader(ctx context.Context, r io.Reader, filename string) (IngestResult, error)
```

Reads all bytes from `r` then delegates to `IngestFile`. Content type is detected from `filename`.

### `Ingestor.IngestBatch`

```go
func (ing *Ingestor) IngestBatch(ctx context.Context, items []BatchItem) (BatchResult, error)
```

Ingests multiple documents. Sequential mode (default) pools embedding calls across documents. Concurrent mode (`WithBatchConcurrency`) runs independent pipelines in parallel. Per-document outcomes are in `BatchResult`; partial success is possible.

### `HybridRetriever.Retrieve`

```go
func (h *HybridRetriever) Retrieve(ctx context.Context, query string, topK int) ([]RetrievalResult, error)
```

Embeds the query, runs vector + keyword search in parallel, merges via RRF, resolves parent-child chunks, applies the optional reranker, applies `minScore`, returns at most `topK` results sorted by score descending. Also implements `core.Sourced`: call `h.Sources()` after retrieval to get `[]core.Source` for agent citation.

### `HybridRetriever.RetrieveWithEmbedding`

```go
func (h *HybridRetriever) RetrieveWithEmbedding(ctx context.Context, queryEmbedding []float32, query string, topK int) ([]RetrievalResult, error)
```

Like `Retrieve` but accepts a pre-computed embedding, avoiding a redundant `Embed()` call.

### `GraphRetriever.Retrieve`

Same signature as `HybridRetriever.Retrieve`. Also implements `core.Sourced`.

---

## Options

### Ingestor options (`ingest.Option`)

| Option | Default | Description |
|---|---|---|
| `WithChunker(c)` | `RecursiveChunker` | Override the flat-strategy chunker. Disables auto-selection by content type. |
| `WithStrategy(s)` | `StrategyFlat` | Switch to `StrategyParentChild`. |
| `WithParentTokens(n)` | 1024 | Max tokens per parent chunk. |
| `WithChildTokens(n)` | 256 | Max tokens per child chunk. |
| `WithBatchSize(n)` | 64 | Chunks per `Embed()` call. |
| `WithMaxContentSize(n)` | 50 MB | Reject files larger than this. `0` disables. |
| `WithExtractor(ct, e)` | — | Register or override an extractor for a `ContentType`. Use this to delegate PDF/DOCX parsing to an external parser (liteparse, LlamaParse) — see Recipe 8 in [examples.md](examples.md). |
| `WithGraphExtraction(p, cfg)` | disabled | LLM-based (and optional sequence) relationship extraction. `p` is the `core.Provider`; `cfg` is an `ingest.GraphExtractionConfig` struct. Pass `nil` for `p` with `cfg.SequenceEdges = true` to emit only deterministic sequence edges. The zero-value `GraphExtractionConfig` reproduces all framework defaults. |
| `WithContextualEnrichment(p)` | disabled | Prepend LLM-generated context to each chunk before embedding. |
| `WithBatchConcurrency(n)` | 1 | Parallel pipelines during `IngestBatch`. |
| `WithBatchCrossDocEdges(true)` | `false` | Auto-run cross-document edge extraction after `IngestBatch`. |
| `WithImageEmbedding(p)` | disabled | Embed page images as chunks via a multimodal embedding provider. |
| `WithBlobStore(bs)` | disabled | Store image binary data externally (not inline in `ChunkMeta`). |
| `WithLLMTimeout(d)` | 2 min | Max duration per LLM call. |
| `WithExtractRetries(n)` | 0 | Retry failed extractor calls with exponential backoff + jitter. |
| `WithOnSuccess(fn)` | nil | Callback after each successful ingestion. |
| `WithOnError(fn)` | nil | Callback after each failed ingestion. |
| `WithIngestorTracer(t)` | nil | `core.Tracer` for spans. |
| `WithIngestorLogger(l)` | nil | `*slog.Logger`. |

### HybridRetriever options (`rag.RetrieverOption`)

| Option | Default | Description |
|---|---|---|
| `WithReranker(r)` | nil | `Reranker` run after hybrid merge. |
| `WithMinRetrievalScore(s)` | 0 | Drop results below this score. |
| `WithKeywordWeight(w)` | 0.3 | Keyword weight in RRF; vector weight is `1 - w`. Must be in [0, 1]. |
| `WithOverfetchMultiplier(n)` | 3 | Fetch `topK * n` candidates before reranking. |
| `WithFilters(f...)` | nil | `core.ChunkFilter` values passed to the store. |
| `WithRetrieverTracer(t)` | nil | `core.Tracer`. |
| `WithRetrieverLogger(l)` | nil | `*slog.Logger`. |

### `ingest.GraphExtractionConfig`

Configuration struct passed as the second argument to `WithGraphExtraction`. The zero value reproduces all framework defaults.

```go
type GraphExtractionConfig struct {
    BatchSize        int     // chunks per LLM extraction call (default 5)
    BatchOverlap     int     // chunks shared between consecutive sliding-window batches (default 0); ignored when SemanticBatching is true
    Workers          int     // max concurrent LLM calls (default 3)
    MinEdgeWeight    float32 // drop edges below this weight (default 0, no filtering)
    MaxEdgesPerChunk int     // cap edges per source chunk (default 0, unlimited)
    DocContextBytes  int     // max source-document bytes included in each extraction prompt (default 0, disabled)
    SemanticBatching bool    // group nearest-neighbor chunks instead of sliding window (forces BatchOverlap=0)
    SequenceEdges    bool    // add RelSequence edges between consecutive chunks (no LLM required)
}
```

### `rag.GraphRetrieverConfig`

Configuration struct passed directly to `NewGraphRetriever`. The zero value reproduces all defaults.

```go
type GraphRetrieverConfig struct {
    MaxHops           int                  // BFS traversal depth (default 2)
    GraphWeight       float32              // weight for graph scores in final blend (default 0.3)
    VectorWeight      float32              // weight for vector scores in final blend (default 0.7)
    HopDecay          []float32            // per-hop score multiplier (default {1.0, 0.7, 0.5})
    Bidirectional     bool                 // traverse outgoing AND incoming edges (default false)
    RelationFilter    []core.RelationType  // restrict traversal to these relation types (nil = all)
    MinTraversalScore float32              // skip edges below this weight (default 0)
    SeedTopK          int                  // seed chunks from initial vector search (default 10)
    SeedKeywordWeight float32              // keyword search weight in seed RRF merge (default 0, disabled)
    GraphTopK         int                  // reserved slots for graph-discovered chunks (default 0, single pool)
    MaxFrontierSize   int                  // cap BFS frontier per hop (default 0, unlimited)
    Reranker          Reranker             // optional reranker after graph score blending
    Filters           []core.ChunkFilter   // metadata filters for initial vector search
    Tracer            core.Tracer
    Logger            *slog.Logger
}
```

Weight normalization: if `GraphWeight + VectorWeight` differs from `1.0` by more than `0.01`, `NewGraphRetriever` normalizes both proportionally. If `HopDecay` is shorter than `MaxHops`, the last element is repeated to pad it.

### Chunker options (`ingest.ChunkerOption`)

| Option | Default | Applies to |
|---|---|---|
| `WithMaxTokens(n)` | 512 | All chunkers (1 token ≈ 4 bytes). |
| `WithOverlapTokens(n)` | 50 | `RecursiveChunker`. |
| `WithBreakpointPercentile(p)` | 25 | `SemanticChunker`: lower = fewer splits. |
| `WithChunkerLogger(l)` | nil | `SemanticChunker`. |

---

## Errors

| Scenario | Behavior |
|---|---|
| File exceeds `maxContentSize` | `IngestFile` returns a descriptive error; `onError` hook fires. |
| Unknown file extension | Falls back to `PlainTextExtractor`; warning logged. |
| Embedding API failure | `IngestFile` / `Retrieve` return a wrapped error. No partial state written. |
| Graph extraction LLM failure | Warning logged; ingestion completes without graph edges. |
| Contextual enrichment failure | Chunk stored with original content. Non-fatal. |
| Store write failure | `IngestFile` returns the wrapped error. |
| Keyword search failure | `HybridRetriever` logs a warning and falls back to vector-only. |
| Parent chunk fetch failure | Child content returned instead. Non-fatal. |
| LLM reranker failure | Original results returned unmodified (no Go error returned to caller). |

---

## Thread Safety

- `Ingestor` methods are safe to call concurrently on separate documents.
- `HybridRetriever.Retrieve` and `GraphRetriever.Retrieve` are safe to call concurrently. `Sources()` is thread-safe; concurrent callers may observe results from any completed Retrieve call.
- `ScoreReranker` and `LLMReranker` are stateless; all methods are safe to call concurrently.

---

## Utility

### `rag.CosineSimilarity`

```go
func CosineSimilarity(a, b []float32) float32
```

Returns cosine similarity in [0, 1]. Returns 0 for empty, mismatched-length, or zero-magnitude vectors.
