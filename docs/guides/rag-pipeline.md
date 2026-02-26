# RAG Pipeline

This guide walks through Oasis's complete Retrieval-Augmented Generation pipeline — from raw documents to search results in an agent's context. It covers ingestion, retrieval, and how to wire everything together.

## Architecture Overview

```mermaid
flowchart TB
    subgraph "Ingestion (write path)"
        RAW["Raw content<br>(text, HTML, MD, PDF)"] --> EXTRACT["Extractor<br>→ plain text"]
        EXTRACT --> CHUNK["Chunker<br>→ []string"]
        CHUNK --> CONTEXT["Contextual Enrichment<br>(optional, LLM-based)"]
        CONTEXT --> EMBED["EmbeddingProvider<br>→ []float32"]
        EMBED --> STORE_DOC["Store.StoreDocument()<br>→ document + chunks + FTS index"]
    end

    subgraph "Retrieval (read path)"
        QUERY["Query string"] --> QUERY_EMBED["EmbeddingProvider<br>→ query embedding"]
        QUERY_EMBED --> VECTOR["Store.SearchChunks()<br>vector search"]
        QUERY_EMBED --> FTS["Store.SearchChunksKeyword()<br>FTS5 keyword search"]
        VECTOR --> RRF["Reciprocal Rank Fusion<br>merge + score"]
        FTS --> RRF
        RRF --> PARENT["Parent-child resolution"]
        PARENT --> RERANK["Reranker (optional)"]
        RERANK --> RESULTS["[]RetrievalResult"]
    end

    STORE_DOC -.->|"stored chunks<br>+ embeddings"| VECTOR
    STORE_DOC -.->|"FTS5 index"| FTS
```

## Quick Start

**SQLite:**

```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/ingest"
    "github.com/nevindra/oasis/tools/knowledge"
    "github.com/nevindra/oasis/store/sqlite"
    "github.com/nevindra/oasis/provider/gemini"
)

// 1. Set up dependencies
store := sqlite.New("oasis.db")
store.Init(ctx)
embedding := gemini.NewEmbedding(apiKey, "text-embedding-004", 768)

// 2. Ingest documents
ingestor := ingest.NewIngestor(store, embedding)
result, _ := ingestor.IngestText(ctx, content, "https://example.com", "My Document")
fmt.Printf("Stored %d chunks\n", result.ChunkCount)

// 3. Search with the retrieval pipeline
retriever := oasis.NewHybridRetriever(store, embedding)
results, _ := retriever.Retrieve(ctx, "what is the main idea?", 5)

// 4. Give it to an agent via KnowledgeTool
knowledgeTool := knowledge.New(store, embedding)
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithTools(knowledgeTool),
)
```

**PostgreSQL (pgvector):**

```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/ingest"
    "github.com/nevindra/oasis/tools/knowledge"
    "github.com/nevindra/oasis/store/postgres"
    "github.com/nevindra/oasis/provider/gemini"
    "github.com/jackc/pgx/v5/pgxpool"
)

// 1. Set up dependencies — WithEmbeddingDimension is required for pgvector HNSW indexes
pool, _ := pgxpool.New(ctx, "postgres://user:pass@localhost:5432/mydb")
defer pool.Close()

store := postgres.New(pool,
    postgres.WithEmbeddingDimension(768), // must match your embedding model
)
store.Init(ctx)
embedding := gemini.NewEmbedding(apiKey, "text-embedding-004", 768)

// 2–4: same as SQLite — ingest, retrieve, wire to agent
ingestor := ingest.NewIngestor(store, embedding)
result, _ := ingestor.IngestText(ctx, content, "https://example.com", "My Document")
retriever := oasis.NewHybridRetriever(store, embedding)
knowledgeTool := knowledge.New(store, embedding)
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithTools(knowledgeTool),
)
```

## Ingestion (Write Path)

The ingestion pipeline converts raw content into searchable, embedded chunks stored in the database. It runs: **extract → chunk → embed → store**.

**Package:** `github.com/nevindra/oasis/ingest`

### Step 1: Extraction

Extractors convert raw bytes into plain text. Content type is detected from file extension.

| Extractor | Content Types | Notes |
|-----------|--------------|-------|
| `PlainTextExtractor` | `text/plain` | Pass-through |
| `HTMLExtractor` | `text/html` | Strips tags, scripts, styles; decodes entities |
| `MarkdownExtractor` | `text/markdown` | Strips formatting, preserves content |
| `pdf.NewExtractor()` | PDF | Opt-in, `ingest/pdf` subpackage |
| `csv.NewExtractor()` | CSV | Opt-in, `ingest/csv` subpackage. Headers → labeled paragraphs |
| `json.NewExtractor()` | JSON | Opt-in, `ingest/json` subpackage. Recursive key flattening |
| `docx.NewExtractor()` | DOCX | Opt-in, `ingest/docx` subpackage. Pure Go, implements `MetadataExtractor` |

```go
// Custom extractor for unsupported formats
type CSVExtractor struct{}
func (CSVExtractor) Extract(content []byte) (string, error) {
    return convertCSVToText(content)
}

ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithExtractor("text/csv", CSVExtractor{}),
)
```

### Step 2: Chunking

Chunkers split plain text into pieces suitable for embedding. Token counts are approximated as `tokens * 4` characters.

#### RecursiveChunker (default)

Splits hierarchically: paragraphs → sentences → words. Handles edge cases:
- Abbreviations (Mr., Dr., vs., etc., e.g., i.e.) — not treated as sentence boundaries
- Decimal numbers (3.14, $1.50) — dots preserved
- CJK punctuation (。！？) — recognized as sentence boundaries
- Overlapping chunks for context continuity

```go
chunker := ingest.NewRecursiveChunker(
    ingest.WithMaxTokens(512),    // max tokens per chunk (default)
    ingest.WithOverlapTokens(50), // overlap between chunks (default)
)
```

#### MarkdownChunker

Splits at heading boundaries, preserving heading context within each chunk. Falls back to RecursiveChunker for sections that exceed the token limit.

```go
chunker := ingest.NewMarkdownChunker(ingest.WithMaxTokens(1024))
```

Auto-selected for markdown content when no explicit chunker is set.

#### SemanticChunker

Splits text at semantic boundaries by computing cosine similarity between consecutive sentence embeddings. When similarity drops below a percentile threshold, a chunk boundary is inserted. Produces topic-coherent chunks that respect natural content transitions.

```go
chunker := ingest.NewSemanticChunker(embedding.Embed,
    ingest.WithMaxTokens(512),
    ingest.WithBreakpointPercentile(25), // split at 25th percentile of similarity drops
)

ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithChunker(chunker),
)
```

The first argument is an `EmbedFunc` — pass `embedding.Embed` directly (the signatures match). On embedding errors, falls back to `RecursiveChunker` automatically.

### Step 3: Chunking Strategy

#### Flat (default)

Single-level chunking. Every chunk gets its own embedding and is independently searchable.

```go
ingestor := ingest.NewIngestor(store, embedding)
// or explicitly:
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyFlat),
)
```

#### Parent-Child

Two-level hierarchical chunking. Small child chunks (~256 tokens) are embedded for precise matching. Large parent chunks (~1024 tokens) store richer context. On retrieval, matched children are resolved to their parent's content.

```mermaid
graph TB
    DOC[Document] --> P1["Parent chunk 1<br>(1024 tokens, no embedding)"]
    DOC --> P2["Parent chunk 2<br>(1024 tokens, no embedding)"]
    P1 --> C1["Child 1<br>(256 tok, embedded)"]
    P1 --> C2["Child 2<br>(256 tok, embedded)"]
    P1 --> C3["Child 3<br>(256 tok, embedded)"]
    P2 --> C4["Child 4<br>(256 tok, embedded)"]
    P2 --> C5["Child 5<br>(256 tok, embedded)"]

    style C1 fill:#e1f5fe
    style C2 fill:#e1f5fe
    style C3 fill:#e1f5fe
    style C4 fill:#e1f5fe
    style C5 fill:#e1f5fe
```

```go
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyParentChild),
)

// Full control over chunk sizes
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyParentChild),
    ingest.WithParentChunker(ingest.NewMarkdownChunker(ingest.WithMaxTokens(1024))),
    ingest.WithChildChunker(ingest.NewRecursiveChunker(ingest.WithMaxTokens(256))),
)
```

### Step 3b: Contextual Enrichment (Optional)

When enabled, each chunk is sent to an LLM alongside the full document text. The LLM returns a 1-2 sentence context prefix that is prepended to `chunk.Content` before embedding. This embeds document-level positional context into each chunk's vector, improving retrieval precision by ~35%.

```go
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithContextualEnrichment(llm),      // enable enrichment
    ingest.WithContextWorkers(5),               // concurrent LLM calls (default 3)
    ingest.WithContextMaxDocBytes(100_000),     // doc truncation limit (default 100KB)
)
```

**How it works:**

```
// Before enrichment:
chunk.Content = "OAuth tokens expire after 1 hour."

// After enrichment:
chunk.Content = "This chunk discusses token lifecycle in the Authentication section.\n\nOAuth tokens expire after 1 hour."
```

For parent-child strategy, only child chunks are enriched. Individual LLM failures are logged but don't block ingestion.

### Step 4: Embedding

Chunks are embedded in configurable batches (default 64 per `Embed()` call) to respect provider rate limits. Only child chunks get embeddings in parent-child strategy — parent chunks are stored without embeddings.

```go
// Adjust batch size for rate-limited providers
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithBatchSize(32),
)
```

### Step 5: Storage

`Store.StoreDocument()` persists the document, all chunks (with embeddings), and updates the FTS5 index for keyword search.

### Ingestion Methods

```go
// From plain text
result, _ := ingestor.IngestText(ctx, text, "source-url", "Title")

// From file bytes (auto-detects content type by extension)
result, _ := ingestor.IngestFile(ctx, fileBytes, "report.md")

// From io.Reader
result, _ := ingestor.IngestReader(ctx, resp.Body, "page.html")
```

All methods return `IngestResult{DocumentID, Document, ChunkCount}`.

### Ingestion Options Summary

| Option | Default | Description |
|--------|---------|-------------|
| `WithChunker(c)` | RecursiveChunker | Custom chunker for flat strategy |
| `WithParentChunker(c)` | RecursiveChunker(1024) | Parent-level chunker |
| `WithChildChunker(c)` | RecursiveChunker(256) | Child-level chunker |
| `WithStrategy(s)` | `StrategyFlat` | `StrategyFlat` or `StrategyParentChild` |
| `WithParentTokens(n)` | 1024 | Parent chunk size |
| `WithChildTokens(n)` | 256 | Child chunk size |
| `WithBatchSize(n)` | 64 | Chunks per `Embed()` call |
| `WithExtractor(ct, e)` | — | Register custom extractor for content type |
| `WithContextualEnrichment(p)` | nil | Enable LLM-based contextual enrichment per chunk |
| `WithContextWorkers(n)` | 3 | Max concurrent LLM calls for enrichment |
| `WithContextMaxDocBytes(n)` | 100,000 | Max document bytes sent to LLM (0 = unlimited) |
| `WithGraphExtraction(p)` | nil | Enable LLM-based graph extraction |
| `WithSequenceEdges(b)` | false | Auto-create sequence edges between consecutive chunks |
| `WithIngestorTracer(t)` | nil | Attach a `Tracer` for span creation |
| `WithIngestorLogger(l)` | nil | Attach a `*slog.Logger` for structured logging |

---

## Retrieval (Read Path)

The retrieval pipeline searches ingested chunks and returns ranked results. It composes vector search, keyword search, rank fusion, parent resolution, and optional re-ranking.

**Package:** root `oasis` package

### Basic Retrieval

```go
retriever := oasis.NewHybridRetriever(store, embedding)
results, _ := retriever.Retrieve(ctx, "how does authentication work?", 5)

for _, r := range results {
    fmt.Printf("[%.3f] %s\n", r.Score, r.Content[:100])
}
```

### Hybrid Search (Vector + Keyword)

When the Store implements `KeywordSearcher` (both `store/sqlite` and `store/libsql` do), `HybridRetriever` automatically uses both vector and keyword search, merging results with Reciprocal Rank Fusion (RRF).

Vector search excels at semantic similarity ("What does the auth module do?"). Keyword search excels at exact matches ("JWT", "OAuth2", specific error codes). Combined, they provide better recall than either alone.

```go
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithKeywordWeight(0.3), // keyword gets 30%, vector gets 70%
)
```

If the Store doesn't implement `KeywordSearcher`, the retriever falls back to vector-only — no error.

### Re-ranking

Re-ranking improves precision by re-scoring the merged results after initial retrieval.

#### ScoreReranker (no external calls)

Filters below a minimum score and re-sorts. Useful as a baseline when no LLM reranker is available.

```go
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithReranker(oasis.NewScoreReranker(0.1)),
)
```

#### LLMReranker (LLM-powered)

Uses an LLM to score query-document relevance on a 0-10 scale. More accurate but costs an extra LLM call per retrieval. Degrades gracefully on LLM failure — results pass through unmodified.

```go
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithReranker(oasis.NewLLMReranker(llmProvider)),
)
```

#### Custom Reranker

Implement the `Reranker` interface for custom logic (cross-encoders, external APIs, etc.):

```go
type Reranker interface {
    Rerank(ctx context.Context, query string, results []RetrievalResult, topK int) ([]RetrievalResult, error)
}
```

### Parent-Child Resolution

When documents are ingested with `StrategyParentChild`, the retriever automatically resolves child chunks to their parent's content:

1. Vector search matches small, precisely-embedded child chunks
2. Retriever looks up each child's `ParentID`
3. Child is replaced with the parent's richer content
4. If multiple children share a parent, only the highest-scored survives

This gives you the best of both worlds: precise matching via small chunks, rich context via large chunks.

### Filtering by Metadata

`WithFilters` scopes retrieval to specific documents, sources, metadata, or time ranges. Filters are applied at the store level — both vector and keyword search paths respect them.

```go
// Retrieve only from a specific document
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithFilters(oasis.ByDocument("doc-abc")),
)

// Retrieve from a specific source, created recently
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithFilters(
        oasis.BySource("https://docs.example.com"),
        oasis.CreatedAfter(1700000000),
    ),
)

// Retrieve chunks with specific metadata
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithFilters(oasis.ByMeta("section_heading", "API Reference")),
)
```

Available filter constructors: `ByDocument(ids...)`, `BySource(source)`, `ByMeta(key, value)`, `CreatedAfter(unix)`, `CreatedBefore(unix)`. Multiple filters are AND-combined.

### Retrieval Options Summary

| Option | Default | Description |
|--------|---------|-------------|
| `WithReranker(r)` | nil | Re-ranking stage after hybrid merge |
| `WithMinRetrievalScore(s)` | 0 | Drop results below this score |
| `WithKeywordWeight(w)` | 0.3 | Keyword weight in RRF merge |
| `WithOverfetchMultiplier(n)` | 3 | Fetch topK*n candidates before trim |
| `WithFilters(f...)` | none | Metadata filters passed to both search paths |
| `WithRetrieverTracer(t)` | nil | Attach a `Tracer` for span creation |
| `WithRetrieverLogger(l)` | nil | Attach a `*slog.Logger` for structured logging |

---

## Graph RAG

Graph RAG adds a knowledge graph layer on top of vector search. During ingestion, relationships between chunks are discovered and stored as weighted, typed edges. During retrieval, `GraphRetriever` traverses these edges via multi-hop BFS to discover contextually related content that vector similarity alone would miss — prerequisite concepts, contradictions, causal chains, and elaborations.

> **Full reference:** See **[Graph RAG Concept](../concepts/graph-rag.md)** for the complete deep-dive — extraction internals, edge validation, score blending math, decision guides, and the full data model.

### Quick Setup

```go
// Ingest with both LLM extraction and sequence edges
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithGraphExtraction(llm),
    ingest.WithSequenceEdges(true),
    ingest.WithMinEdgeWeight(0.3),
    ingest.WithMaxEdgesPerChunk(5),
    ingest.WithIngestorLogger(slog.Default()), // log extraction warnings
)
result, _ := ingestor.IngestFile(ctx, docBytes, "architecture.md")

// Retrieve with graph traversal
retriever := oasis.NewGraphRetriever(store, embedding,
    oasis.WithMaxHops(2),
    oasis.WithBidirectional(true),
)
results, _ := retriever.Retrieve(ctx, "what are the system dependencies?", 5)

// Wire to agent via KnowledgeTool
knowledgeTool := knowledge.New(store, embedding,
    knowledge.WithRetriever(retriever),
)
agent := oasis.NewLLMAgent("assistant", "Answer using your knowledge base.", llm,
    oasis.WithTools(knowledgeTool),
)
```

### Two Edge Sources

| Source | Option | LLM Cost | What It Does |
|--------|--------|----------|-------------|
| LLM extraction | `WithGraphExtraction(llm)` | Yes | Discovers 8 relationship types (references, elaborates, depends_on, etc.) with confidence weights |
| Sequence edges | `WithSequenceEdges(true)` | No | Links consecutive chunks sharing the same parent — `chunk[0] → chunk[1] → chunk[2]` |

Use sequence edges alone for zero-cost graph traversal, or combine both for the richest graph.

### Relationship Types

| Type | Meaning |
|------|---------|
| `references` | Chunk A cites or mentions content from chunk B |
| `elaborates` | Chunk A provides more detail on chunk B's topic |
| `depends_on` | Chunk A assumes knowledge from chunk B |
| `contradicts` | Chunk A conflicts with chunk B |
| `part_of` | Chunk A is a component or subset of chunk B |
| `similar_to` | Chunks cover overlapping topics |
| `sequence` | Chunk A follows chunk B in logical order |
| `caused_by` | Chunk A is a consequence of chunk B |

Each edge carries a confidence weight (0.0–1.0). Use `WithMinEdgeWeight` to prune low-confidence edges.

### Cross-Document Edge Extraction

After ingesting multiple documents, discover relationships between them:

```go
count, err := ingestor.ExtractCrossDocumentEdges(ctx,
    ingest.CrossDocWithSimilarityThreshold(0.5),
    ingest.CrossDocWithMaxPairsPerChunk(3),
)
fmt.Printf("Created %d cross-document edges\n", count)
```

### Graph Ingestion Options

| Option | Default | Description |
|--------|---------|-------------|
| `WithGraphExtraction(p)` | nil | Enable LLM-based graph extraction |
| `WithGraphBatchSize(n)` | 5 | Chunks per LLM extraction call |
| `WithGraphBatchOverlap(n)` | 0 | Overlapping chunks between consecutive batches |
| `WithGraphExtractionWorkers(n)` | 3 | Max concurrent LLM calls for graph extraction |
| `WithMinEdgeWeight(w)` | 0 | Minimum edge weight to keep |
| `WithMaxEdgesPerChunk(n)` | 0 (unlimited) | Cap edges per source chunk (top N by weight) |
| `WithSequenceEdges(b)` | false | Auto-create sequence edges between consecutive chunks |
| `WithCrossDocumentEdges(b)` | false | Enable automatic cross-document edge discovery during ingestion |

### GraphRetriever Options

| Option | Default | Description |
|--------|---------|-------------|
| `WithMaxHops(n)` | 2 | Maximum graph traversal depth |
| `WithGraphWeight(w)` | 0.3 | Weight for graph-derived scores |
| `WithVectorWeight(w)` | 0.7 | Weight for vector similarity scores |
| `WithHopDecay([]float32)` | {1.0, 0.7, 0.5} | Score decay per hop level |
| `WithBidirectional(b)` | false | Traverse both outgoing and incoming edges |
| `WithRelationFilter(types...)` | all | Restrict traversal to specific relation types |
| `WithMinTraversalScore(s)` | 0 | Minimum edge weight to follow |
| `WithSeedTopK(k)` | 10 | Seed chunks from initial vector search |
| `WithSeedKeywordWeight(w)` | 0 (disabled) | Keyword weight for hybrid seed selection via RRF |
| `WithGraphFilters(f...)` | none | Metadata filters for vector search |
| `WithGraphRetrieverTracer(t)` | nil | Attach a `Tracer` for span creation |
| `WithGraphRetrieverLogger(l)` | nil | Attach a `*slog.Logger` for structured logging |

### When to Use Graph RAG

**Good fit:** Technical docs with dependencies, legal texts with cross-references, scientific papers with citations, tutorials where concepts build on each other.

**Overkill:** FAQ lists, product catalogs, independent documents with no inter-chunk relationships. Use `HybridRetriever` instead.

---

## Wiring RAG to Agents

### Using KnowledgeTool

`KnowledgeTool` wraps a `Retriever` and exposes it as an agent tool. The tool itself is intentionally thin — retrieval configuration belongs on the `Retriever`, not the tool.

#### Minimal setup

With no options, `KnowledgeTool` creates a default `HybridRetriever` internally:

```go
knowledgeTool := knowledge.New(store, embedding)
```

This gives you hybrid search (vector + keyword) with default settings (top-5 results, keyword weight 0.3, no score threshold, no filters).

#### Configuring retrieval behavior

To control top-K, score thresholds, chunk filters, keyword weight, or re-ranking, construct a `Retriever` with the options you need and inject it via `WithRetriever`:

```go
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithMinRetrievalScore(0.05),            // drop low-relevance results
    oasis.WithKeywordWeight(0.4),                 // 40% keyword, 60% vector
    oasis.WithFilters(oasis.BySource("legal/")),  // scope to specific documents
    oasis.WithReranker(oasis.NewLLMReranker(llm)), // LLM re-ranking for precision
)
knowledgeTool := knowledge.New(store, embedding,
    knowledge.WithRetriever(retriever),
    knowledge.WithTopK(10),
)
```

The same pattern works with `GraphRetriever` or any custom `Retriever` implementation — the tool doesn't need to know which retriever it's using.

#### Vector-only search (no keyword/FTS)

To disable keyword search, set keyword weight to zero:

```go
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithKeywordWeight(0), // vector-only, no FTS
)
knowledgeTool := knowledge.New(store, embedding,
    knowledge.WithRetriever(retriever),
)
```

#### KnowledgeTool options

| Option | Default | Description |
|--------|---------|-------------|
| `WithRetriever(r)` | auto-created `HybridRetriever` | Inject a custom `Retriever` |
| `WithTopK(n)` | 5 | Number of results to retrieve |

All other retrieval behavior (score threshold, filters, keyword weight, re-ranking, overfetch) is configured on the `Retriever` — see the [Retrieval Options Summary](#retrieval-options-summary) above.

#### Wiring to an agent

```go
agent := oasis.NewLLMAgent("assistant", "Knowledge-augmented assistant", llm,
    oasis.WithTools(knowledgeTool),
)
```

The agent can then call `knowledge_search` with a query, and results are returned as formatted text including both knowledge base chunks and relevant past conversations.

### Using Retriever Directly

For workflows or custom agents that need raw retrieval results:

```go
retriever := oasis.NewHybridRetriever(store, embedding)
results, _ := retriever.Retrieve(ctx, userQuery, 5)

// Format results into agent context
var context strings.Builder
for _, r := range results {
    fmt.Fprintf(&context, "---\n%s\n", r.Content)
}

// Inject into agent prompt
agent := oasis.NewLLMAgent("assistant", "...", llm,
    oasis.WithPrompt("Use the following context:\n" + context.String()),
)
```

### Custom Retriever

Implement the `Retriever` interface for specialized search strategies:

```go
type Retriever interface {
    Retrieve(ctx context.Context, query string, topK int) ([]RetrievalResult, error)
}

// Example: external vector database
type PineconeRetriever struct { /* ... */ }

func (p *PineconeRetriever) Retrieve(ctx context.Context, query string, topK int) ([]oasis.RetrievalResult, error) {
    // Query Pinecone, map to RetrievalResult
}

knowledgeTool := knowledge.New(store, embedding,
    knowledge.WithRetriever(&PineconeRetriever{...}),
)
```

---

## Full Example

End-to-end: ingest a document, configure the retrieval pipeline, wire it to an agent.

```go
package main

import (
    "context"
    "fmt"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/ingest"
    "github.com/nevindra/oasis/provider/gemini"
    "github.com/nevindra/oasis/store/sqlite"
    "github.com/nevindra/oasis/tools/knowledge"
)

func main() {
    ctx := context.Background()

    // Dependencies
    store := sqlite.New("knowledge.db")
    store.Init(ctx)
    defer store.Close()

    llm := gemini.New(apiKey, "gemini-2.0-flash")
    embedding := gemini.NewEmbedding(apiKey, "text-embedding-004", 768)

    // Ingest with parent-child strategy for rich context
    ingestor := ingest.NewIngestor(store, embedding,
        ingest.WithStrategy(ingest.StrategyParentChild),
        ingest.WithParentTokens(1024),
        ingest.WithChildTokens(256),
    )
    result, _ := ingestor.IngestText(ctx, longDocument, "source", "My Document")
    fmt.Printf("Ingested: %d chunks\n", result.ChunkCount)

    // Configure retrieval with hybrid search + re-ranking
    retriever := oasis.NewHybridRetriever(store, embedding,
        oasis.WithKeywordWeight(0.3),
        oasis.WithOverfetchMultiplier(3),
        oasis.WithReranker(oasis.NewScoreReranker(0.05)),
        oasis.WithMinRetrievalScore(0.01),
    )

    // Wire to agent via KnowledgeTool
    knowledgeTool := knowledge.New(store, embedding,
        knowledge.WithRetriever(retriever),
        knowledge.WithTopK(5),
    )

    agent := oasis.NewLLMAgent("assistant",
        "You answer questions using your knowledge base. Always cite sources.",
        llm,
        oasis.WithTools(knowledgeTool),
    )

    result, _ := agent.Execute(ctx, oasis.AgentTask{
        Input: "Summarize the main points of the document",
    })
    fmt.Println(result.Output)
}
```

## Strategy Guide

Oasis's RAG pipeline has six independent knobs. Each can be enabled or combined freely. This section helps you pick the right combination for your use case.

### Decision Flowchart

```mermaid
flowchart TD
    START["What are you building?"] --> SIZE{"Document size?"}

    SIZE -->|"Short (< 1 page)"| FLAT["Flat chunking"]
    SIZE -->|"Long (multi-page)"| PC["Parent-Child chunking"]

    FLAT --> ENRICH{"Need better retrieval<br>precision? (+1 LLM call/chunk)"}
    PC --> ENRICH

    ENRICH -->|"Yes"| CE["Enable Contextual Enrichment"]
    ENRICH -->|"No / budget constrained"| GRAPH

    CE --> GRAPH{"Do chunks reference<br>each other?"}

    GRAPH -->|"No"| HYBRID["Use HybridRetriever"]
    GRAPH -->|"Sequential only"| SEQ["Add Sequence Edges<br>→ GraphRetriever"]
    GRAPH -->|"Rich relationships"| LLM_GRAPH["Add LLM Graph Extraction<br>→ GraphRetriever"]

    HYBRID --> RERANK{"Precision critical?"}
    SEQ --> RERANK
    LLM_GRAPH --> RERANK

    RERANK -->|"Yes"| LLM_RR["Add LLM Reranker"]
    RERANK -->|"No"| SCORE_RR["Score filter or none"]
```

### Feature Reference

| Feature | What It Does | When to Use | Cost |
|---------|-------------|-------------|------|
| **Parent-Child** | Small chunks for matching, large chunks for context | Long documents where surrounding context matters | None |
| **Contextual Enrichment** | LLM adds document-level context prefix to each chunk before embedding | Most use cases — ~35% retrieval improvement | 1 LLM call per chunk |
| **Sequence Edges** | Links consecutive chunks for graph traversal | Sequential/narrative content (tutorials, specs) | None |
| **LLM Graph Extraction** | Discovers typed relationships (references, depends_on, etc.) | Interconnected content with cross-references | 1 LLM call per batch |
| **Hybrid Search** | Combines vector + keyword (FTS5) via RRF | Default — good for most use cases | None |
| **Graph Retrieval** | Multi-hop BFS over chunk relationships | When relationships between chunks add value | None (edges already stored) |
| **LLM Reranker** | Re-scores results with an LLM for precision | High-stakes answers, noisy result sets | 1 LLM call per retrieval |
| **Score Reranker** | Filters below a threshold, re-sorts | Baseline filtering without LLM cost | None |

### Cost Spectrum

From cheapest to most expensive at ingestion time:

```
Flat + Recursive                      → 0 LLM calls (embedding only)
Flat + Recursive + Sequence Edges     → 0 LLM calls
Parent-Child + Markdown parents       → 0 LLM calls
+ Contextual Enrichment               → +1 LLM call per chunk
+ LLM Graph Extraction                → +1 LLM call per batch of 5 chunks
+ Both                                → +1 per chunk + 1 per batch
```

At retrieval time:

```
HybridRetriever                       → 1 embedding call
GraphRetriever                        → 1 embedding call + graph traversal (no LLM)
+ LLM Reranker                        → +1 LLM call per retrieval
```

---

## Recipes

Copy-paste configurations for common use cases. Each recipe includes both ingestion and retrieval setup.

### Recipe 1: Simple FAQ / Knowledge Base

**Best for:** FAQ pages, product catalogs, independent Q&A pairs, short documents.

Small, self-contained documents with no cross-references. Prioritize speed and simplicity.

```go
// Ingestion — flat chunks, no enrichment, no graph
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyFlat),
    // defaults: RecursiveChunker, 512 tokens, 50 overlap
)
result, _ := ingestor.IngestText(ctx, faqContent, "faq", "FAQ")

// Retrieval — hybrid search with basic score filter
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithKeywordWeight(0.3),
    oasis.WithReranker(oasis.NewScoreReranker(0.05)),
)
knowledgeTool := knowledge.New(store, embedding,
    knowledge.WithRetriever(retriever),
    knowledge.WithTopK(5),
)
```

**Why this works:** FAQ entries are short and self-contained — flat chunking keeps them as single chunks. Keyword search helps match exact product names or error codes. No LLM overhead at ingestion.

---

### Recipe 2: Technical Documentation

**Best for:** API docs, SDK references, engineering specs, developer guides.

Long, structured documents with markdown headings. Readers search for specific APIs or concepts, but need surrounding context to understand the answer.

```go
// Ingestion — parent-child with markdown parents, contextual enrichment, sequence edges
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyParentChild),
    ingest.WithParentChunker(ingest.NewMarkdownChunker(ingest.WithMaxTokens(1024))),
    ingest.WithChildChunker(ingest.NewRecursiveChunker(ingest.WithMaxTokens(256))),
    ingest.WithContextualEnrichment(llm),
    ingest.WithSequenceEdges(true),
    ingest.WithIngestorLogger(slog.Default()),
)
result, _ := ingestor.IngestFile(ctx, docBytes, "api-reference.md")

// Retrieval — hybrid search with LLM reranker for precision
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithKeywordWeight(0.3),
    oasis.WithReranker(oasis.NewLLMReranker(llm)),
    oasis.WithMinRetrievalScore(0.03),
)
knowledgeTool := knowledge.New(store, embedding,
    knowledge.WithRetriever(retriever),
    knowledge.WithTopK(5),
)
```

**Why this works:** Markdown-aware parent chunking preserves section structure. Small child chunks match precise queries ("what does `WithMaxTokens` do?"), while parent chunks provide the full section context. Contextual enrichment adds "this chunk is from the Authentication section" — so searching "auth" finds relevant chunks even if the word doesn't appear in the chunk text. Sequence edges let the retriever walk to neighboring sections.

---

### Recipe 3: Legal / Regulatory Corpus

**Best for:** Contracts, compliance docs, regulations, policies with numbered sections and cross-references.

Dense, interconnected documents where sections reference each other ("as defined in Section 3.2"). Accuracy is critical — wrong answers have real consequences.

```go
// Ingestion — flat chunking, contextual enrichment, full LLM graph extraction
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyFlat),
    ingest.WithChunker(ingest.NewRecursiveChunker(
        ingest.WithMaxTokens(512),
        ingest.WithOverlapTokens(100), // higher overlap for legal text continuity
    )),
    ingest.WithContextualEnrichment(llm),
    ingest.WithContextWorkers(5),
    ingest.WithGraphExtraction(llm),
    ingest.WithSequenceEdges(true),
    ingest.WithMinEdgeWeight(0.3),
    ingest.WithMaxEdgesPerChunk(8),
    ingest.WithIngestorLogger(slog.Default()),
)
result, _ := ingestor.IngestFile(ctx, policyBytes, "privacy-policy.pdf")

// After ingesting multiple documents, discover cross-document references
count, _ := ingestor.ExtractCrossDocumentEdges(ctx,
    ingest.CrossDocWithSimilarityThreshold(0.5),
    ingest.CrossDocWithMaxPairsPerChunk(3),
)

// Retrieval — graph traversal with bidirectional edges + LLM reranker
retriever := oasis.NewGraphRetriever(store, embedding,
    oasis.WithMaxHops(2),
    oasis.WithBidirectional(true),
    oasis.WithRelationFilter(
        oasis.RelReferences,
        oasis.RelDependsOn,
        oasis.RelPartOf,
    ),
    oasis.WithMinTraversalScore(0.3),
    oasis.WithSeedKeywordWeight(0.4), // legal terms need strong keyword matching
)
knowledgeTool := knowledge.New(store, embedding,
    knowledge.WithRetriever(retriever),
    knowledge.WithTopK(8),
)
```

**Why this works:** Legal text is dense — flat chunks with higher overlap prevent cutting mid-clause. Graph extraction discovers "Section 3.1 references Section 5.2" relationships. Bidirectional traversal finds both "what does this section reference?" and "what references this section?". Cross-document edges connect related policies. High keyword weight catches exact legal terms and section numbers.

---

### Recipe 4: Multi-Format Document Library

**Best for:** Intranets, knowledge bases with mixed PDFs, DOCX, HTML, CSV, and markdown files uploaded by users.

Heterogeneous content where you can't predict the format or structure. Needs to handle everything gracefully.

```go
// Ingestion — parent-child with auto-detected chunkers, contextual enrichment
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyParentChild),
    // Don't set WithChunker — auto-selects MarkdownChunker for .md, RecursiveChunker for others
    ingest.WithContextualEnrichment(llm),
    ingest.WithContextWorkers(3),
    ingest.WithSequenceEdges(true),
    ingest.WithBatchSize(32), // smaller batches for rate-limited providers
    ingest.WithOnSuccess(func(r ingest.IngestResult) {
        log.Printf("Ingested %s: %d chunks", r.Document.Title, r.ChunkCount)
    }),
    ingest.WithOnError(func(source string, err error) {
        log.Printf("Failed to ingest %s: %v", source, err)
    }),
    ingest.WithIngestorLogger(slog.Default()),
)

// Ingest anything — content type auto-detected from extension
result, _ := ingestor.IngestFile(ctx, pdfBytes, "report.pdf")
result, _ = ingestor.IngestFile(ctx, docxBytes, "memo.docx")
result, _ = ingestor.IngestFile(ctx, csvBytes, "data.csv")
result, _ = ingestor.IngestFile(ctx, htmlBytes, "page.html")

// Retrieval — hybrid search with score filter
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithKeywordWeight(0.3),
    oasis.WithReranker(oasis.NewScoreReranker(0.05)),
    oasis.WithOverfetchMultiplier(3),
)
knowledgeTool := knowledge.New(store, embedding,
    knowledge.WithRetriever(retriever),
    knowledge.WithTopK(5),
)
```

**Why this works:** The ingestor auto-detects content type from file extensions and uses the right extractor (PDF, DOCX, CSV, JSON, HTML, Markdown, plain text). Parent-child chunking provides context regardless of format. Contextual enrichment is especially valuable here — chunks from different document types benefit from the LLM adding "this is from the Q3 financial report" context. Lifecycle hooks (`OnSuccess`, `OnError`) help monitor a pipeline processing many files.

---

### Recipe 5: Research Papers / Academic Corpus

**Best for:** Scientific papers, arXiv collections, literature reviews, academic knowledge bases.

Long documents with complex internal structure, citation chains, and concepts that build on each other across papers.

```go
// Ingestion — parent-child with semantic parent boundaries, full graph extraction
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyParentChild),
    ingest.WithParentChunker(ingest.NewSemanticChunker(embedding.Embed,
        ingest.WithMaxTokens(1024),
        ingest.WithBreakpointPercentile(25),
    )),
    ingest.WithChildChunker(ingest.NewRecursiveChunker(ingest.WithMaxTokens(256))),
    ingest.WithContextualEnrichment(llm),
    ingest.WithContextWorkers(5),
    ingest.WithGraphExtraction(llm),
    ingest.WithSequenceEdges(true),
    ingest.WithMinEdgeWeight(0.3),
    ingest.WithMaxEdgesPerChunk(10),
    ingest.WithGraphBatchSize(8),
    ingest.WithIngestorLogger(slog.Default()),
)
result, _ := ingestor.IngestFile(ctx, paperBytes, "paper.pdf")

// After ingesting multiple papers, discover cross-paper relationships
count, _ := ingestor.ExtractCrossDocumentEdges(ctx,
    ingest.CrossDocWithSimilarityThreshold(0.4), // lower threshold for broader discovery
    ingest.CrossDocWithMaxPairsPerChunk(5),
)

// Retrieval — graph traversal for citation chains and concept dependencies
retriever := oasis.NewGraphRetriever(store, embedding,
    oasis.WithMaxHops(3),                          // deeper traversal for citation chains
    oasis.WithGraphWeight(0.4),                    // higher graph weight — relationships matter more
    oasis.WithVectorWeight(0.6),
    oasis.WithHopDecay([]float32{1.0, 0.8, 0.6, 0.4}),
    oasis.WithBidirectional(true),
    oasis.WithSeedTopK(15),
    oasis.WithSeedKeywordWeight(0.2),              // catch paper titles, author names
    oasis.WithGraphRetrieverLogger(slog.Default()),
)
knowledgeTool := knowledge.New(store, embedding,
    knowledge.WithRetriever(retriever),
    knowledge.WithTopK(8),
)
```

**Why this works:** Semantic parent chunking splits papers at natural topic boundaries (Introduction, Methods, Results) rather than arbitrary token limits. Contextual enrichment adds "this chunk discusses methodology for protein folding" — critical when multiple papers cover overlapping topics. LLM graph extraction discovers "Paper A's results depend on Paper B's methodology". Higher maxHops (3) follows citation chains: Paper A → cites Paper B → which builds on Paper C. Cross-document edges connect the corpus into a navigable knowledge graph.

---

### Recipe 6: Chatbot with Uploaded Files

**Best for:** Customer support bots, personal assistants where users upload files and ask questions immediately.

Low-latency requirements. Users upload a file and expect instant answers. Minimize ingestion time — no LLM enrichment overhead.

```go
// Ingestion — flat, small chunks, no enrichment, no graph. Fast.
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyFlat),
    ingest.WithChunker(ingest.NewRecursiveChunker(
        ingest.WithMaxTokens(256), // smaller chunks for precise matching
        ingest.WithOverlapTokens(25),
    )),
    ingest.WithBatchSize(64),
)
result, _ := ingestor.IngestFile(ctx, uploadedBytes, filename)

// Retrieval — hybrid with higher keyword weight for user queries
retriever := oasis.NewHybridRetriever(store, embedding,
    oasis.WithKeywordWeight(0.4),    // users often search with exact terms
    oasis.WithOverfetchMultiplier(2), // less overfetch for speed
    oasis.WithFilters(oasis.ByDocument(result.DocumentID)), // scope to uploaded doc
)
knowledgeTool := knowledge.New(store, embedding,
    knowledge.WithRetriever(retriever),
    knowledge.WithTopK(5),
)
```

**Why this works:** No LLM calls at ingestion — only embedding. This means near-instant processing even for large files. Smaller chunks (256 tokens) match user questions more precisely. Higher keyword weight (0.4) handles the common pattern where users search with exact terms from the document ("what does section 5 say?"). Filtering by `DocumentID` scopes results to just the uploaded file.

---

### Recipe Comparison

| Recipe | Ingestion LLM Calls | Retrieval LLM Calls | Latency | Best For |
|--------|---------------------|---------------------|---------|----------|
| 1. FAQ | 0 | 0 | Fastest | Short, independent content |
| 2. Tech Docs | 1 per chunk | 1 per retrieval | Medium | Structured long-form docs |
| 3. Legal | 1 per chunk + 1 per batch | 0 | Slower ingestion | Cross-referenced dense text |
| 4. Multi-Format | 1 per chunk | 0 | Medium | Mixed content types |
| 5. Research | 1 per chunk + 1 per batch | 0 | Slowest ingestion | Academic/scientific papers |
| 6. Chatbot | 0 | 0 | Fastest | Real-time file Q&A |

## See Also

- [Graph RAG Concept](../concepts/graph-rag.md) — graph extraction internals, score blending, decision guides
- [Ingest Concept](../concepts/ingest.md) — pipeline architecture details
- [Retrieval Concept](../concepts/retrieval.md) — interfaces and internals
- [Store Concept](../concepts/store.md) — persistence and vector search
- [Ingesting Documents Guide](ingesting-documents.md) — more ingestion examples
- [Memory and Recall Guide](memory-and-recall.md) — conversation memory (separate from RAG)
