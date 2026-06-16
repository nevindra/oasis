# RAG Examples

---

## Recipe 1: Ingest a PDF and search it

**Goal:** Extract text from a PDF, chunk it, embed it, store it, then retrieve the top 5 passages for a query.

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/nevindra/oasis/ingest"
    "github.com/nevindra/oasis/rag"
    // your store and embedding provider — e.g. sqlite store, OpenAI embeddings
)

func main() {
    ctx := context.Background()

    ing := ingest.NewIngestor(store, embeddingProvider)

    data, err := os.ReadFile("annual-report.pdf")
    if err != nil {
        panic(err)
    }
    result, err := ing.IngestFile(ctx, data, "annual-report.pdf")
    if err != nil {
        panic(err)
    }
    fmt.Printf("stored doc %s with %d chunks\n", result.DocumentID, result.ChunkCount)

    retriever := rag.NewHybridRetriever(store, embeddingProvider)
    results, err := retriever.Retrieve(ctx, "What were the key risks in 2024?", 5)
    if err != nil {
        panic(err)
    }
    for _, r := range results {
        fmt.Printf("[%.2f] %s\n", r.Score, r.Content[:100])
    }
}
```

**Plain-English walkthrough:**

- `NewIngestor` auto-registers all built-in extractors. The `.pdf` extension triggers `PDFExtractor`.
- `IngestFile` runs the full pipeline (extract → chunk → embed → store) in one call. No intermediate steps to manage.
- `NewHybridRetriever` with no options runs vector-only search if the store lacks FTS, or vector + keyword search if it supports `core.KeywordSearcher`.
- Each `RetrievalResult.Content` contains the raw chunk text. `Score` is in [0, 1].

**Variations:**

- Replace `IngestFile` with `IngestText(ctx, text, "source-url", "My Title")` to ingest pre-extracted text.
- Replace `IngestFile` with `IngestReader(ctx, file, "report.pdf")` to stream from an `io.Reader`.
- Add `ingest.WithMinRetrievalScore(0.5)` — wait, that's a retriever option; use `rag.WithMinRetrievalScore(0.5)` on the retriever to drop weak matches.

---

## Recipe 2: Parent-child chunking for better LLM context

**Goal:** Embed small chunks for precise matching, but return larger parent chunks to the LLM so it has more context.

```go
ing := ingest.NewIngestor(store, embeddingProvider,
    ingest.WithStrategy(ingest.StrategyParentChild),
    ingest.WithParentTokens(1024),
    ingest.WithChildTokens(256),
)

data, _ := os.ReadFile("technical-spec.md")
ing.IngestFile(ctx, data, "technical-spec.md")

retriever := rag.NewHybridRetriever(store, embeddingProvider)
results, _ := retriever.Retrieve(ctx, "how does the auth handshake work?", 5)
// results[0].Content is the parent chunk (up to ~1024 tokens)
// results[0].ParentID is non-empty
```

**Plain-English walkthrough:**

- `StrategyParentChild` creates two levels: parent chunks at ~1024 tokens and child chunks at ~256 tokens. Only child chunks get embeddings.
- At retrieval time, `HybridRetriever` matches on child embeddings (good precision) then transparently replaces children with their parent content before returning results. You see parent text, not child text.
- Markdown files automatically use `MarkdownChunker` for the parent level (splits at headings) regardless of the chunker option, unless you use `WithChunker()` to override.

**Variations:**

- For plain text, the defaults (1024/256 tokens) work well. For long-form docs with sections > 1024 tokens, increase `WithParentTokens(2048)`.
- Pair with `WithContextualEnrichment` (Recipe 4) to further improve retrieval precision.

---

## Recipe 3: Graph RAG — follow conceptual links

**Goal:** Build a knowledge graph during ingestion so the retriever can walk relationships like "this chunk depends on that one" to surface non-obvious context.

```go
// --- Ingestion ---
ing := ingest.NewIngestor(store, embeddingProvider,
    ingest.WithGraphExtraction(llmProvider, ingest.GraphExtractionConfig{
        SequenceEdges: true,  // free sequential links, no LLM needed
        BatchSize:     5,
        MinEdgeWeight: 0.5,   // drop low-confidence edges
    }),
)
ing.IngestFile(ctx, data, "architecture-guide.md")

// --- Retrieval ---
retriever := rag.NewGraphRetriever(store, embeddingProvider, rag.GraphRetrieverConfig{
    MaxHops:        2,
    GraphWeight:    0.3,
    GraphTopK:      3, // guarantee 3 graph-discovered slots in top-10
    RelationFilter: []core.RelationType{
        core.RelDependsOn,
        core.RelElaborates,
    },
})
results, _ := retriever.Retrieve(ctx, "what must I configure before enabling TLS?", 10)

for _, r := range results {
    for _, ec := range r.GraphContext {
        fmt.Printf("  <- %s via %s\n", ec.FromChunkID, ec.Relation)
    }
}
```

**Plain-English walkthrough:**

- `WithGraphExtraction(llmProvider, cfg)` runs an LLM after chunking that reads batches of chunks and outputs a JSON edge list. Each edge has a type (e.g. `depends_on`) and a confidence weight.
- `SequenceEdges: true` in the config adds cheap `RelSequence` edges linking chunk[i] → chunk[i+1], so `GraphRetriever` can walk to neighboring chunks for continuity without any LLM calls.
- `MinEdgeWeight: 0.5` discards edges where the LLM was less than 50% confident.
- `GraphRetriever.Retrieve` first does a vector search for seed chunks, then BFS-traverses the stored edges up to 2 hops. Chunks discovered via graph traversal get a score of `GraphWeight * edge.weight * hopDecay`.
- `GraphTopK: 3` reserves 3 of the 10 result slots for graph-discovered chunks, preventing seed chunks from crowding them out.
- `r.GraphContext` tells you which chunk pointed here and what the relationship was.

**Variations:**

- Set `Bidirectional: true` in `GraphRetrieverConfig` when you want to walk both `A→B` and `B→A` edges (useful for `contradicts` relationships).
- Set `SeedKeywordWeight: 0.3` in `GraphRetrieverConfig` to diversify the seed set with keyword results before graph traversal begins.
- For large documents, set `DocContextBytes: 50_000` in `GraphExtractionConfig` to include document structure in the LLM prompt for better relationship detection.

---

## Recipe 4: Contextual enrichment for noisy corpora

**Goal:** Improve retrieval precision by prepending an LLM-generated context prefix to each chunk before embedding.

```go
ing := ingest.NewIngestor(store, embeddingProvider,
    ingest.WithContextualEnrichment(llmProvider),
    ingest.WithContextWorkers(5),
    ingest.WithContextMaxDocBytes(80_000),
)
ing.IngestFile(ctx, data, "support-tickets.json")
```

**Plain-English walkthrough:**

- After chunking, each chunk is sent to the LLM alongside the first `80_000` bytes of the source document. The LLM writes a 1-2 sentence prefix like "This chunk describes the error handling policy for payment failures." That prefix is prepended to the chunk text before embedding.
- The vector then encodes both the document-level context and the local chunk content, making it much easier to match queries like "what happens when a payment fails?" to the right passage.
- `WithContextWorkers(5)` runs 5 LLM calls concurrently to keep ingestion fast.
- On LLM failure for any chunk, the original content is used (non-fatal).

**Variations:**

- With `StrategyParentChild`, only child chunks are enriched (parent chunks are not embedded).
- Lower `WithContextMaxDocBytes` for very large documents where full context exceeds your provider's context window.

---

## Recipe 5: Batch ingestion of a document library

**Goal:** Ingest a folder of files efficiently, sharing embedding API calls across documents.

```go
items := []ingest.BatchItem{
    {Data: pdfBytes, Filename: "q1-report.pdf"},
    {Data: docxBytes, Filename: "strategy.docx"},
    {Data: mdBytes, Filename: "runbook.md"},
}

ing := ingest.NewIngestor(store, embeddingProvider,
    ingest.WithBatchSize(128),
    ingest.WithBatchConcurrency(3),
    ingest.WithBatchCrossDocEdges(true),
    ingest.WithOnSuccess(func(r ingest.IngestResult) {
        log.Printf("ok: %s (%d chunks)", r.Document.Source, r.ChunkCount)
    }),
    ingest.WithOnError(func(source string, err error) {
        log.Printf("fail: %s: %v", source, err)
    }),
)

result, err := ing.IngestBatch(ctx, items)
fmt.Printf("ok: %d, failed: %d\n", len(result.Succeeded), len(result.Failed))
```

**Plain-English walkthrough:**

- `WithBatchConcurrency(3)` runs 3 ingestion pipelines in parallel. Without it, documents are processed one at a time but embedding calls are pooled across documents (fewer API calls).
- `WithBatchCrossDocEdges(true)` runs cross-document relationship extraction after all documents are ingested. The framework compares chunk embeddings across documents and asks the LLM to label cross-document relationships. This only works when `WithGraphExtraction` is also set.
- `OnSuccess` and `OnError` fire per document, so you get real-time feedback without waiting for the entire batch.

**Variations:**

- For large libraries (hundreds of documents), consider `WithBatchConcurrency(1)` (default) so embedding batches are pooled and you make fewer API calls.
- Add `WithExtractRetries(3)` if any extractor calls a remote OCR or document conversion service that may transiently fail.

---

## Recipe 6: LLM reranking for high-precision queries

**Goal:** After hybrid retrieval, send candidates to an LLM for relevance scoring instead of relying on vector similarity alone.

```go
reranker := rag.NewLLMReranker(llmProvider)
// optionally: rag.WithRerankerTimeout(30 * time.Second)(reranker)

retriever := rag.NewHybridRetriever(store, embeddingProvider,
    rag.WithOverfetchMultiplier(5),    // fetch 5x candidates before reranking
    rag.WithReranker(reranker),
)

results, _ := retriever.Retrieve(ctx, "compare the security models of approach A and B", 5)
```

**Plain-English walkthrough:**

- `WithOverfetchMultiplier(5)` tells the retriever to fetch `5 * topK` candidates first. More candidates = better reranking quality, at the cost of a larger LLM prompt.
- `LLMReranker` builds a prompt listing all candidates and asks the LLM to rate each 0-10 for relevance to the query. Scores are normalized to [0, 1] and results are re-sorted.
- If the LLM call fails or returns unparseable JSON, the original candidates are returned unmodified — you never get a hard error from reranking.
- Use `NewScoreReranker(0.5)` instead for a zero-LLM alternative that simply drops results below a fixed score threshold.

**Variations:**

- For latency-sensitive paths, use `WithRerankerTimeout(15 * time.Second)` to bound the LLM call.
- Combine with `WithMinRetrievalScore(0.3)` on the retriever to pre-filter obvious junk before reranking, reducing the prompt size.

---

## Recipe 7: Semantic chunking for unstructured prose

**Goal:** Use embedding-based split detection instead of fixed-size chunking for narrative text where paragraph boundaries don't align with topic boundaries.

```go
chunker := ingest.NewSemanticChunker(
    embeddingProvider.Embed,            // EmbedFunc — pass provider.Embed directly
    ingest.WithMaxTokens(600),
    ingest.WithBreakpointPercentile(20), // split at the largest 20% of similarity drops
)

ing := ingest.NewIngestor(store, embeddingProvider,
    ingest.WithChunker(chunker),
)
ing.IngestText(ctx, novelText, "novel-draft", "My Novel")
```

**Plain-English walkthrough:**

- `SemanticChunker` splits the text into sentences, embeds them all in one call, computes cosine similarity between consecutive sentences, and sets chunk boundaries where similarity drops sharply (below the 20th percentile).
- Lower `WithBreakpointPercentile` → fewer, larger chunks. Higher → more, smaller chunks.
- `WithMaxTokens(600)` ensures no chunk exceeds ~2400 bytes even if the semantic detector doesn't split it.
- If the embedding call fails, the chunker falls back to `RecursiveChunker` automatically.

**Variations:**

- For technical documents with clear heading structure, `NewMarkdownChunker()` is usually better than `SemanticChunker` and requires no embedding calls at ingest time.
- Pair `SemanticChunker` with `WithContextualEnrichment` for maximum retrieval precision on noisy long-form content.

---

## Recipe 8: Delegate PDF parsing to an external parser (liteparse / LlamaParse)

**Goal:** Use a best-in-class document parser for scanned pages, tables, and multi-column layouts instead of the built-in `PDFExtractor`.

The built-in `PDFExtractor` does pure-Go text extraction — fast and dependency-light, and the right default for clean, digital-native PDFs. But Go has no strong story for OCR or layout reconstruction, so for scanned documents, complex tables, or multi-column pages, delegate to a parser built for the job. Tools like [liteparse](https://github.com/run-llama/liteparse) (Rust + PDFium + Tesseract, exposed via its Node/Python bindings) or LlamaParse (cloud API) run as a sidecar; you reach them through the `Extractor` seam.

```go
import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
    "time"

    "github.com/nevindra/oasis/core"
    "github.com/nevindra/oasis/ingest"
)

// LiteparseExtractor sends raw bytes to a liteparse sidecar over HTTP and
// returns the parsed text. It satisfies ingest.Extractor.
type LiteparseExtractor struct {
    Endpoint string       // e.g. "http://liteparse:8080/parse"
    Client   *http.Client
}

func (e LiteparseExtractor) Extract(content []byte) (string, error) {
    resp, err := e.Client.Post(e.Endpoint, "application/octet-stream", bytes.NewReader(content))
    if err != nil {
        return "", fmt.Errorf("liteparse: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return "", fmt.Errorf("liteparse: status %d", resp.StatusCode)
    }
    var out struct {
        Text string `json:"text"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        return "", fmt.Errorf("liteparse decode: %w", err)
    }
    return out.Text, nil
}

func newIngestor(store core.Store, emb core.EmbeddingProvider) *ingest.Ingestor {
    parser := LiteparseExtractor{
        Endpoint: "http://liteparse:8080/parse",
        Client:   &http.Client{Timeout: 60 * time.Second},
    }
    return ingest.NewIngestor(store, emb,
        ingest.WithExtractor(ingest.TypePDF, parser),   // overrides the built-in PDFExtractor
        ingest.WithExtractor(ingest.TypeDOCX, parser),  // route DOCX through the same parser
        ingest.WithExtractRetries(2),                   // sidecar may transiently fail
    )
}
```

**Plain-English walkthrough:**

- `WithExtractor(ct, e)` overwrites the extractor for that one content type. Everything else (`.md`, `.csv`, `.json`, `.html`, `.txt`) keeps using the built-in extractors, which Go handles well. You opt out of in-process parsing only where it's weak.
- `Extract` has no `context.Context`, so propagate timeouts via the `http.Client` (`Timeout: 60s` above), not `ctx`. `WithExtractRetries(2)` retries transient sidecar failures with exponential backoff.
- The rest of the pipeline (chunk → embed → store → retrieve) is unchanged — the parser swap is invisible downstream because every stage talks through `core.Chunk`.

**Variations:**

- **Preserve page metadata.** If the sidecar returns per-page text, implement `MetadataExtractor` (`ExtractWithMeta(content []byte) (ingest.ExtractResult, error)`) instead of `Extract`. The ingestor prefers it automatically and attaches page numbers/headings to chunks.
- **LlamaParse / any cloud API.** Same shape — point `Endpoint` at the API and add an auth header in `Extract`. The `Extractor` seam doesn't care whether the parser is a local sidecar or a remote service.
- **Keep both.** Leaving the default in place means an ingestor constructed *without* the override still parses PDFs (with the weaker built-in). Register `WithExtractor` at every ingestor construction site if PDF quality matters across your app.
