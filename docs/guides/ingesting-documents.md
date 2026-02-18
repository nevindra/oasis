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

## PDF Support

PDF extraction is opt-in (separate dependency):

```go
import ingestpdf "github.com/nevindra/oasis/ingest/pdf"

ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithExtractor(ingestpdf.TypePDF, ingestpdf.NewExtractor()),
)

result, _ := ingestor.IngestFile(ctx, pdfBytes, "document.pdf")
```

## Custom Extractors

Convert new content types to plain text:

```go
type CSVExtractor struct{}

func (CSVExtractor) Extract(content []byte) (string, error) {
    return convertCSVToText(content)
}

ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithExtractor("text/csv", CSVExtractor{}),
)
```

## See Also

- [Ingest Concept](../concepts/ingest.md) — pipeline architecture
- [Store Concept](../concepts/store.md) — where chunks are stored
