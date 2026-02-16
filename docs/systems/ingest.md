# oasis-ingest

Text extraction and chunking pipeline. Converts files, URLs, and raw text into `Document` + `Chunk` structs ready for embedding.

## Key Files

- `src/pipeline.rs` - `IngestPipeline` struct, high-level ingestion methods
- `src/chunker.rs` - Recursive text chunking with overlap
- `src/extractor.rs` - Text extraction from HTML, Markdown, plain text

## Architecture

```mermaid
flowchart LR
    Input[Raw Content] --> Detect[Detect Content Type]
    Detect --> Extract[Extract Text]
    Extract --> Chunk[Recursive Chunking]
    Chunk --> Output[Document + Chunks]

    Note1[Embedding is NOT done here] -.-> Output
```

The pipeline intentionally does NOT embed or store — Brain handles that by calling the embedding provider and VectorStore separately.

## Ingestion Methods

| Method | Input | source_type |
|--------|-------|-------------|
| `ingest_text()` | Plain text string | caller-specified |
| `ingest_html()` | HTML string | "url" |
| `ingest_file()` | File content + filename | "file" |
| `fetch_url()` | URL (static) | - (returns HTML) |

## Content Type Detection

`ingest_file()` determines content type from the file extension:

| Extension | Content Type |
|-----------|-------------|
| `.html`, `.htm` | HTML |
| `.md`, `.markdown` | Markdown |
| Everything else | Plain text |

## Text Extraction

The extractor strips formatting from content:
- **HTML**: Removes tags, scripts, styles; decodes entities; collapses whitespace
- **Markdown**: Strips markdown syntax (`**`, `*`, `#`, etc.) to plain text
- **Plain text**: Passed through as-is

## Chunking Strategy

```mermaid
flowchart TD
    Text[Input Text] --> Fit{Fits in one chunk?}
    Fit -->|Yes| Single[Return as single chunk]
    Fit -->|No| Split1[Split on paragraph boundaries]

    Split1 --> ParaFit{Each paragraph fits?}
    ParaFit -->|Yes| Merge[Merge with overlap]
    ParaFit -->|No| Split2[Split on sentence boundaries]

    Split2 --> SentFit{Each sentence fits?}
    SentFit -->|Yes| Merge
    SentFit -->|No| Split3[Split on word boundaries]

    Split3 --> Merge
    Merge --> Output[Overlapping Chunks]
```

### Configuration

| Parameter | Default | Purpose |
|-----------|---------|---------|
| `max_tokens` | 512 | Max tokens per chunk |
| `overlap_tokens` | 50 | Overlap between consecutive chunks |

Tokens are approximated as `tokens * 4 = characters`.

Default: max 2048 chars per chunk, 200 chars overlap.

### Overlap

Each chunk (except the first) starts with the last `overlap_chars` characters from the previous chunk. This ensures no information is lost at chunk boundaries and improves retrieval quality.

## Data Flow: File Upload

```mermaid
sequenceDiagram
    participant User
    participant Brain
    participant Bot as TelegramBot
    participant Pipeline as IngestPipeline
    participant VS as VectorStore
    participant Embed as EmbeddingProvider

    User->>Bot: Upload file
    Bot->>Brain: handle_file(doc)
    Brain->>Bot: get_file() + download_file()
    Brain->>Pipeline: ingest_file(content, filename)
    Pipeline-->>Brain: (Document, Vec<Chunk>)
    Brain->>VS: insert_document(doc)

    Brain->>Embed: embed_text(chunk_texts[])
    Embed-->>Brain: Vec<Vec<f32>>

    loop Each chunk
        Brain->>VS: insert_chunk(chunk, embedding)
    end

    Brain->>Bot: "File ingested: N chunks indexed"
```

## Data Flow: URL Ingestion

Same as file upload, except:
1. `IngestPipeline::fetch_url()` downloads the HTML
2. `ingest_html()` extracts text from HTML before chunking
3. source_type is "url" instead of "file"
