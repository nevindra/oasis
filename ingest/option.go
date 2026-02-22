package ingest

import (
	"log/slog"

	oasis "github.com/nevindra/oasis"
)

// Option configures an Ingestor.
type Option func(*Ingestor)

// WithChunker sets the chunker used for flat strategy.
func WithChunker(c Chunker) Option {
	return func(ing *Ingestor) { ing.chunker = c }
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
