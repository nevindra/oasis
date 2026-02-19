# Ingest

The ingest pipeline handles the full journey from raw content to searchable vector-indexed chunks: **extract → chunk → embed → store**.

## Pipeline

```mermaid
flowchart LR
    RAW["Raw content<br>(text, HTML, MD, PDF,<br>CSV, JSON, DOCX)"] --> EXTRACT[Extractor]
    EXTRACT --> PLAIN["Plain text<br>+ metadata"]
    PLAIN --> CHUNK[Chunker]
    CHUNK --> CHUNKS["Chunks []string"]
    CHUNKS --> EMBED["EmbeddingProvider<br>(batched)"]
    EMBED --> VECTORS["Chunks + embeddings"]
    VECTORS --> STORE["Store<br>StoreDocument()"]
```

## Quick Usage

**Package:** `github.com/nevindra/oasis/ingest`

```go
ingestor := ingest.NewIngestor(store, embedding)

// From text
result, _ := ingestor.IngestText(ctx, content, "source-url", "Document Title")

// From file (auto-detects content type by extension)
result, _ := ingestor.IngestFile(ctx, fileBytes, "report.md")

// From io.Reader
result, _ := ingestor.IngestReader(ctx, resp.Body, "page.html")
```

Returns `IngestResult`:

```go
type IngestResult struct {
    DocumentID string          // unique ID for the stored document
    Document   oasis.Document  // full Document (ID, Title, Source, Content, CreatedAt)
    ChunkCount int             // total chunks created (flat) or parents + children combined
}
```

Use `result.DocumentID` to reference the document later (e.g., in upload responses or `Store.DeleteDocument`).

## Extractors

Convert raw bytes to plain text:

| Extractor | Content Types |
|-----------|--------------|
| `PlainTextExtractor` | `text/plain` |
| `HTMLExtractor` | `text/html` — strips tags, scripts, styles |
| `MarkdownExtractor` | `text/markdown` |
| `pdf.NewExtractor()` | PDF (opt-in, `ingest/pdf` subpackage) |
| `csv.NewExtractor()` | CSV (opt-in, `ingest/csv` subpackage) |
| `json.NewExtractor()` | JSON (opt-in, `ingest/json` subpackage) |
| `docx.NewExtractor()` | DOCX (opt-in, `ingest/docx` subpackage) |

Content type is detected from file extension via `ContentTypeFromExtension()`.

**Binary formats (PDF, DOCX) require explicit registration.** If you call `IngestFile` with a `.pdf` or `.docx` file without registering the corresponding extractor, it returns an error. Import the subpackage and register with `WithExtractor()`:

```go
import ingestpdf "github.com/nevindra/oasis/ingest/pdf"

ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithExtractor(ingest.TypePDF, ingestpdf.NewExtractor()),
)
```

### MetadataExtractor

Extractors may optionally implement `MetadataExtractor` to return structured metadata alongside text. When an extractor provides `ExtractWithMeta()`, the ingestor uses it instead of `Extract()` and assigns page-level metadata (page number, section heading, images) to each chunk via byte-range overlap matching.

Built-in metadata extractors: `pdf.Extractor`, `docx.Extractor`.

## Chunk Metadata

Each chunk can carry a `ChunkMeta` with:

- **PageNumber** — source page (from PDF or DOCX)
- **SectionHeading** — nearest heading
- **SourceURL** — file path or URL
- **Images** — extracted images (base64-encoded)

Metadata is stored as JSON in the `metadata` column and flows through the retrieval pipeline.

## Chunkers

Split text into chunks suitable for embedding. All chunkers implement the `Chunker` interface:

```go
type Chunker interface {
    Chunk(text string) []string
}
```

### RecursiveChunker (default)

Splits by paragraphs → sentences → words. Improved sentence boundaries: abbreviation-aware (Mr., Dr.), decimal-safe (3.14), CJK punctuation.

```go
ingest.NewRecursiveChunker(
    ingest.WithMaxTokens(512),
    ingest.WithOverlapTokens(50),
)
```

### MarkdownChunker

Splits at heading boundaries, preserves headings in chunks for LLM context. Falls back to RecursiveChunker for oversized sections.

```go
ingest.NewMarkdownChunker(ingest.WithMaxTokens(1024))
```

### SemanticChunker

Splits text at semantic boundaries detected by embedding similarity drops between consecutive sentences. Uses percentile-based breakpoint detection: when cosine similarity between two consecutive sentences falls below the Nth percentile of all consecutive similarities, the chunker inserts a boundary.

```go
ingest.NewSemanticChunker(embedding.Embed,
    ingest.WithMaxTokens(512),
    ingest.WithBreakpointPercentile(25), // default: split at 25th percentile
)
```

The first argument is an `EmbedFunc` — a function with signature `func(context.Context, []string) ([][]float32, error)`. This matches `EmbeddingProvider.Embed`, so you can pass `embedding.Embed` directly.

`SemanticChunker` implements `ContextChunker` (see below), which means the `Ingestor` will automatically pass context through to the embedding call. On embedding errors, it falls back to `RecursiveChunker` — no error is returned.

### ContextChunker

`ContextChunker` extends `Chunker` with a context-aware method for chunkers that call external services (embedding APIs, databases):

```go
type ContextChunker interface {
    Chunker
    ChunkContext(ctx context.Context, text string) ([]string, error)
}
```

The `Ingestor` auto-detects this capability via type assertion. When the chunker implements `ContextChunker`, the ingestor calls `ChunkContext` (passing request context for cancellation and tracing). Otherwise it falls back to `Chunk`.

## Chunking Strategies

### Flat (default)

Single-level chunking. Each chunk is independently embedded and searched.

### Parent-Child

Two-level hierarchical. Small child chunks (~256 tokens) are embedded for precise matching. Large parent chunks (~1024 tokens) provide full context on retrieval.

```mermaid
graph TB
    DOC[Document] --> P1[Parent chunk 1<br>1024 tokens]
    DOC --> P2[Parent chunk 2<br>1024 tokens]
    P1 --> C1[Child 1<br>256 tok]
    P1 --> C2[Child 2<br>256 tok]
    P1 --> C3[Child 3<br>256 tok]
    P2 --> C4[Child 4<br>256 tok]
    P2 --> C5[Child 5<br>256 tok]

    style C1 fill:#e1f5fe
    style C2 fill:#e1f5fe
    style C3 fill:#e1f5fe
    style C4 fill:#e1f5fe
    style C5 fill:#e1f5fe
```

On retrieval: match children → resolve `ParentID` → return parent content.

```go
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyParentChild),
)
```

## Ingestor Options

| Option | Default | Description |
|--------|---------|-------------|
| `WithChunker(c)` | RecursiveChunker | Custom chunker for flat strategy |
| `WithParentChunker(c)` | — | Parent-level chunker |
| `WithChildChunker(c)` | — | Child-level chunker |
| `WithStrategy(s)` | `StrategyFlat` | `StrategyFlat` or `StrategyParentChild` |
| `WithParentTokens(n)` | 1024 | Parent chunk size |
| `WithChildTokens(n)` | 256 | Child chunk size |
| `WithBatchSize(n)` | 64 | Chunks per `Embed()` call |
| `WithExtractor(ct, e)` | — | Register custom extractor for content type |

Chunker options (shared by all chunker constructors):

| Option | Default | Description |
| --- | --- | --- |
| `WithMaxTokens(n)` | 512 | Max tokens per chunk (approximated as n*4 chars) |
| `WithOverlapTokens(n)` | 50 | Overlap between consecutive chunks |
| `WithBreakpointPercentile(p)` | 25 | Similarity percentile for semantic split detection (SemanticChunker only) |

## Batched Embedding

Large documents are embedded in configurable batches (default 64 chunks per `Embed()` call) to respect provider rate limits.

## See Also

- [Retrieval](retrieval.md) — the search pipeline that reads ingested chunks
- [Store](store.md) — where documents and chunks are stored
- [RAG Pipeline Guide](../guides/rag-pipeline.md) — end-to-end walkthrough
- [Ingesting Documents Guide](../guides/ingesting-documents.md)
