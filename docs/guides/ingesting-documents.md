# Ingesting Documents

This guide shows how to use the ingest pipeline to chunk and embed documents for RAG search.

## Basic Ingestion

```go
import "github.com/nevindra/oasis/ingest"

ingestor := ingest.NewIngestor(store, embedding)

// From text
result, _ := ingestor.IngestText(ctx, content, "https://example.com", "Page Title")

// From file
result, _ := ingestor.IngestFile(ctx, fileBytes, "report.md")

// From io.Reader
result, _ := ingestor.IngestReader(ctx, resp.Body, "page.html")

fmt.Printf("Stored %d chunks for document %s\n", result.ChunkCount, result.DocumentID)
```

## Markdown-aware Chunking

For markdown documents, use MarkdownChunker to split at heading boundaries:

```go
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithChunker(ingest.NewMarkdownChunker(ingest.WithMaxTokens(1024))),
)
```

## Semantic Chunking

For content with mixed topics, use SemanticChunker to split at natural topic boundaries. It embeds consecutive sentences and splits where cosine similarity drops sharply.

```go
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithChunker(ingest.NewSemanticChunker(embedding.Embed,
        ingest.WithMaxTokens(512),
        ingest.WithBreakpointPercentile(25),
    )),
)
```

The first argument (`embedding.Embed`) provides the embedding function — the signature matches `EmbedFunc` directly. A lower percentile means fewer splits (only the most significant topic shifts). On embedding failures, falls back to `RecursiveChunker` automatically.

## Parent-Child Strategy

For large documents, use two-level chunking. Small chunks for precise matching, large chunks for full context:

```go
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyParentChild),
)

// Full control over sizes
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyParentChild),
    ingest.WithParentChunker(ingest.NewMarkdownChunker(ingest.WithMaxTokens(1024))),
    ingest.WithChildChunker(ingest.NewRecursiveChunker(ingest.WithMaxTokens(256))),
    ingest.WithBatchSize(32),
)
```

## Supported Formats

All seven extractors are registered by default — no imports or `WithExtractor` calls needed:

```go
ingestor := ingest.NewIngestor(store, embedding)

result, _ := ingestor.IngestFile(ctx, pdfBytes, "report.pdf")
result, _ := ingestor.IngestFile(ctx, csvBytes, "data.csv")
result, _ := ingestor.IngestFile(ctx, jsonBytes, "config.json")
result, _ := ingestor.IngestFile(ctx, docxBytes, "document.docx")
```

| Format | Extractor | Behavior |
|--------|-----------|----------|
| Plain text, HTML, Markdown | `PlainTextExtractor`, `HTMLExtractor`, `MarkdownExtractor` | Built-in, always available |
| PDF | `PDFExtractor` | Page-by-page extraction with per-page metadata (pure Go) |
| CSV | `CSVExtractor` | First row as headers, subsequent rows become labeled paragraphs (`Header: Value`) |
| JSON | `JSONExtractor` | Recursive flattening with dotted key paths |
| DOCX | `DOCXExtractor` | Paragraphs, headings, tables, and images (pure Go, no CGO). Implements `MetadataExtractor` for heading-level metadata |

The ingestor recovers from extractor panics and converts them into errors, preventing a misbehaving parser from crashing the process.

## Custom Extractors

Convert new content types to plain text:

```go
type MyExtractor struct{}

func (MyExtractor) Extract(content []byte) (string, error) {
    return convertToText(content)
}

ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithExtractor("application/custom", MyExtractor{}),
)
```

Optionally implement `MetadataExtractor` to provide page/section metadata:

```go
func (e MyExtractor) ExtractWithMeta(content []byte) (ingest.ExtractResult, error) {
    return ingest.ExtractResult{
        Text: extractedText,
        Meta: []ingest.PageMeta{
            {PageNumber: 1, Heading: "Section 1", StartByte: 0, EndByte: 100},
        },
    }, nil
}
```

## Graph Extraction

Enable LLM-based knowledge graph extraction during ingestion. The LLM analyzes chunk pairs and identifies semantic relationships (references, elaborates, depends_on, etc.) stored as edges for `GraphRetriever`.

```go
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithGraphExtraction(llm),
    ingest.WithMinEdgeWeight(0.3),
    ingest.WithMaxEdgesPerChunk(5),
)
```

For lightweight graph traversal without LLM cost, use sequence edges:

```go
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithSequenceEdges(true), // auto-creates edges between consecutive chunks
)
```

See [RAG Pipeline: Graph RAG](rag-pipeline.md#graph-rag) for the full Graph RAG walkthrough.

## Managing Ingested Documents

List and delete documents after ingestion:

```go
// List all ingested documents
docs, _ := store.ListDocuments(ctx)
for _, doc := range docs {
    fmt.Printf("%s: %s (%s)\n", doc.ID, doc.Title, doc.Source)
}

// Delete a document (also deletes its chunks and edges)
store.DeleteDocument(ctx, "doc-abc")
```

## Observability

Attach a tracer and logger to the ingestor for production monitoring:

```go
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithIngestorTracer(tracer),
    ingest.WithIngestorLogger(slog.Default()),
)
```

## See Also

- [Ingest Concept](../concepts/ingest.md) — pipeline architecture
- [Store Concept](../concepts/store.md) — where chunks are stored
- [RAG Pipeline](rag-pipeline.md) — end-to-end RAG walkthrough
