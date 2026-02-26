package ingest

import (
	"log/slog"

	oasis "github.com/nevindra/oasis"
)

// Option configures an Ingestor.
type Option func(*Ingestor)

// WithChunker sets the chunker used for flat strategy.
// When set, auto-selection based on content type is disabled.
func WithChunker(c Chunker) Option {
	return func(ing *Ingestor) {
		ing.chunker = c
		ing.customChunker = true
	}
}

// WithParentChunker sets the parent-level chunker for StrategyParentChild.
func WithParentChunker(c Chunker) Option {
	return func(ing *Ingestor) { ing.parentChunker = c }
}

// WithChildChunker sets the child-level chunker for StrategyParentChild.
func WithChildChunker(c Chunker) Option {
	return func(ing *Ingestor) { ing.childChunker = c }
}

// WithStrategy sets the chunking strategy.
func WithStrategy(s ChunkStrategy) Option {
	return func(ing *Ingestor) { ing.strategy = s }
}

// WithParentTokens sets the max tokens for parent chunks (default 1024).
func WithParentTokens(n int) Option {
	return func(ing *Ingestor) {
		ing.parentChunker = NewRecursiveChunker(WithMaxTokens(n))
	}
}

// WithChildTokens sets the max tokens for child chunks (default 256).
func WithChildTokens(n int) Option {
	return func(ing *Ingestor) {
		ing.childChunker = NewRecursiveChunker(WithMaxTokens(n))
	}
}

// WithBatchSize sets the number of chunks per Embed() call (default 64).
func WithBatchSize(n int) Option {
	return func(ing *Ingestor) { ing.batchSize = n }
}

// WithMaxContentSize sets the maximum allowed content size in bytes for extraction
// (default 50 MB). Set to 0 to disable the limit.
func WithMaxContentSize(n int) Option {
	return func(ing *Ingestor) { ing.maxContentSize = n }
}

// WithExtractor registers an Extractor for a given ContentType.
func WithExtractor(ct ContentType, e Extractor) Option {
	return func(ing *Ingestor) { ing.extractors[ct] = e }
}

// WithGraphExtraction enables LLM-based graph extraction during ingestion.
func WithGraphExtraction(p oasis.Provider) Option {
	return func(ing *Ingestor) { ing.graphProvider = p }
}

// WithMinEdgeWeight sets the minimum edge weight to keep (default 0, no filtering).
func WithMinEdgeWeight(w float32) Option {
	return func(ing *Ingestor) { ing.minEdgeWeight = w }
}

// WithMaxEdgesPerChunk caps the number of edges kept per source chunk (default 0, unlimited).
func WithMaxEdgesPerChunk(n int) Option {
	return func(ing *Ingestor) { ing.maxEdgesPerChunk = n }
}

// WithGraphBatchSize sets the number of chunks per LLM graph extraction call (default 5).
func WithGraphBatchSize(n int) Option {
	return func(ing *Ingestor) { ing.graphBatchSize = n }
}

// WithGraphBatchOverlap sets the number of chunks that overlap between consecutive
// extraction batches (default 0). Overlap must be less than graphBatchSize.
// Higher overlap discovers more cross-boundary relationships at the cost of more LLM calls.
func WithGraphBatchOverlap(n int) Option {
	return func(ing *Ingestor) { ing.graphBatchOverlap = n }
}

// WithGraphExtractionWorkers sets the max concurrent LLM calls for graph extraction
// (default 3). Set to 1 for sequential extraction.
func WithGraphExtractionWorkers(n int) Option {
	return func(ing *Ingestor) { ing.graphWorkers = n }
}

// WithCrossDocumentEdges enables cross-document relationship discovery (default false).
func WithCrossDocumentEdges(b bool) Option {
	return func(ing *Ingestor) { ing.crossDocEdges = b }
}

// WithSequenceEdges enables automatic creation of sequence edges between
// consecutive chunks in the same document (default false). This is a
// lightweight, non-LLM alternative that links chunk[i] → chunk[i+1] with
// RelSequence edges, allowing GraphRetriever to walk to neighboring chunks
// for additional context. Works independently of WithGraphExtraction.
func WithSequenceEdges(b bool) Option {
	return func(ing *Ingestor) { ing.sequenceEdges = b }
}

// WithContextualEnrichment enables LLM-based contextual enrichment during
// ingestion. Each chunk is sent to the provider alongside the full document
// text, and the provider returns a 1-2 sentence context prefix that is
// prepended to chunk.Content before embedding. This improves retrieval
// precision by embedding document-level context into each chunk's vector.
// For parent-child strategy, only child chunks are enriched.
func WithContextualEnrichment(p oasis.Provider) Option {
	return func(ing *Ingestor) { ing.contextProvider = p }
}

// WithContextWorkers sets the max concurrent LLM calls for contextual
// enrichment (default 3). Set to 1 for sequential processing.
func WithContextWorkers(n int) Option {
	return func(ing *Ingestor) { ing.contextWorkers = n }
}

// WithContextMaxDocBytes sets the maximum document size in bytes sent to the
// LLM for contextual enrichment (default 100,000 ≈ ~25K tokens). Documents
// exceeding this limit are truncated at the nearest word boundary. Set to 0
// to disable truncation.
func WithContextMaxDocBytes(n int) Option {
	return func(ing *Ingestor) { ing.contextMaxDocBytes = n }
}

// WithIngestorTracer sets the Tracer for an Ingestor.
func WithIngestorTracer(t oasis.Tracer) Option {
	return func(ing *Ingestor) { ing.tracer = t }
}

// WithIngestorLogger sets the structured logger for an Ingestor.
func WithIngestorLogger(l *slog.Logger) Option {
	return func(ing *Ingestor) { ing.logger = l }
}

// WithOnSuccess registers a callback invoked after each successful ingestion.
// The callback receives the full IngestResult.
func WithOnSuccess(fn func(IngestResult)) Option {
	return func(ing *Ingestor) { ing.onSuccess = fn }
}

// WithOnError registers a callback invoked when ingestion fails.
// source is the filename (IngestFile) or source string (IngestText).
func WithOnError(fn func(source string, err error)) Option {
	return func(ing *Ingestor) { ing.onError = fn }
}

// CrossDocOption configures ExtractCrossDocumentEdges.
type CrossDocOption func(*crossDocConfig)

type crossDocConfig struct {
	documentIDs         []string
	similarityThreshold float32
	maxPairsPerChunk    int
	batchSize           int
}

// CrossDocWithDocumentIDs scopes extraction to specific documents (default: all).
func CrossDocWithDocumentIDs(ids ...string) CrossDocOption {
	return func(c *crossDocConfig) { c.documentIDs = ids }
}

// CrossDocWithSimilarityThreshold sets the minimum cosine similarity to consider
// a cross-document chunk pair (default 0.5).
func CrossDocWithSimilarityThreshold(t float32) CrossDocOption {
	return func(c *crossDocConfig) { c.similarityThreshold = t }
}

// CrossDocWithMaxPairsPerChunk caps the number of cross-document candidates per chunk (default 3).
func CrossDocWithMaxPairsPerChunk(n int) CrossDocOption {
	return func(c *crossDocConfig) { c.maxPairsPerChunk = n }
}

// CrossDocWithBatchSize sets the number of chunks per LLM call for cross-doc extraction (default 5).
func CrossDocWithBatchSize(n int) CrossDocOption {
	return func(c *crossDocConfig) { c.batchSize = n }
}
