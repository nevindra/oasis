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

## CSV, JSON, and DOCX Support

These extractors are opt-in via separate subpackages:

```go
import (
    "github.com/nevindra/oasis/ingest"
    ingestcsv "github.com/nevindra/oasis/ingest/csv"
    ingestjson "github.com/nevindra/oasis/ingest/json"
    ingestdocx "github.com/nevindra/oasis/ingest/docx"
)

ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithExtractor(ingestcsv.TypeCSV, ingestcsv.NewExtractor()),
    ingest.WithExtractor(ingestjson.TypeJSON, ingestjson.NewExtractor()),
    ingest.WithExtractor(ingestdocx.TypeDOCX, ingestdocx.NewExtractor()),
)
```

- **CSV**: First row as headers, subsequent rows become labeled paragraphs (`Header: Value`)
- **JSON**: Recursive flattening with dotted key paths
- **DOCX**: Extracts paragraphs, headings, tables, and images (pure Go, no CGO). Implements `MetadataExtractor` for heading-level metadata.

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

## See Also

- [Ingest Concept](../concepts/ingest.md) — pipeline architecture
- [Store Concept](../concepts/store.md) — where chunks are stored
