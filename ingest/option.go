package ingest

import (
	"log/slog"
	"time"

	oasis "github.com/nevindra/oasis/core"
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

// Default graph-extraction tuning. The zero value of GraphExtractionConfig is
// expanded to these at construction time so callers can pass an empty config
// and get the historical defaults.
const (
	defaultGraphBatchSize = 5
	defaultGraphWorkers   = 3
)

// GraphExtractionConfig tunes LLM-based (and sequence) graph extraction during
// ingestion. It is the single configuration surface for the feature — pass it
// to WithGraphExtraction.
//
// The zero value reproduces the framework defaults: BatchSize 5, Workers 3, no
// overlap, no edge filtering, no document context, sliding-window batching, and
// no sequence edges. Any field left zero falls back to its default; set a field
// to override it.
//
// Field interactions (applied at construction time):
//   - SemanticBatching forces BatchOverlap to 0 — overlap is meaningful only for
//     the sliding-window batcher, not embedding-based batching.
//   - BatchOverlap is clamped to be strictly less than BatchSize; an overlap
//     greater than or equal to the batch size would stall the sliding window.
type GraphExtractionConfig struct {
	// BatchSize is the number of chunks per LLM graph extraction call (default 5).
	BatchSize int
	// BatchOverlap is the number of chunks shared between consecutive sliding-window
	// batches (default 0). Must be < BatchSize. Higher overlap discovers more
	// cross-boundary relationships at the cost of more LLM calls. Ignored when
	// SemanticBatching is true.
	BatchOverlap int
	// Workers is the max concurrent LLM calls for graph extraction (default 3).
	// Set to 1 for sequential extraction.
	Workers int
	// MinEdgeWeight is the minimum edge weight to keep (default 0, no filtering).
	MinEdgeWeight float32
	// MaxEdgesPerChunk caps the number of edges kept per source chunk (default 0, unlimited).
	MaxEdgesPerChunk int
	// DocContextBytes is the max document text (in bytes) included in the extraction
	// prompt (default 0, disabled). When > 0, each LLM call includes a truncated copy
	// of the source document, giving the LLM structural context (headings, hierarchy)
	// for better relationship decisions. Recommended: 50_000 (~12K tokens) for
	// structured technical documents.
	DocContextBytes int
	// SemanticBatching enables embedding-based batching instead of the sliding window.
	// Each chunk's nearest intra-document neighbors (by embedding similarity) are grouped
	// into batches, producing higher-quality extraction with fewer wasted calls. When
	// true, BatchOverlap is ignored (forced to 0).
	SemanticBatching bool
	// SequenceEdges enables automatic creation of RelSequence edges between consecutive
	// chunks in the same document (default false). This is a lightweight, non-LLM
	// alternative that links chunk[i] → chunk[i+1], allowing GraphRetriever to walk to
	// neighboring chunks for additional context. Works without a provider.
	SequenceEdges bool
}

// WithGraphExtraction configures graph extraction during ingestion using cfg.
//
// Pass a non-nil provider p to enable LLM-based relationship extraction. Pass a
// nil provider together with GraphExtractionConfig{SequenceEdges: true} to emit
// only deterministic sequence edges (no LLM calls). An empty config uses the
// framework defaults (see GraphExtractionConfig).
//
// Why: a single config struct replaces nine separate With* options whose
// interactions (overlap vs. semantic batching, overlap vs. batch size) were
// invisible at the call site and only validated implicitly deep in extraction.
// Validation now happens once, here, at construction time.
func WithGraphExtraction(p oasis.Provider, cfg GraphExtractionConfig) Option {
	return func(ing *Ingestor) {
		ing.graphProvider = p

		// Expand zero-value fields to defaults so an empty config reproduces the
		// historical NewIngestor behaviour.
		if cfg.BatchSize <= 0 {
			cfg.BatchSize = defaultGraphBatchSize
		}
		if cfg.Workers <= 0 {
			cfg.Workers = defaultGraphWorkers
		}

		// Why: overlap is meaningless under semantic batching (no sliding window),
		// so a non-zero overlap there is a configuration mistake — zero it and note it.
		if cfg.SemanticBatching && cfg.BatchOverlap != 0 {
			if ing.logger != nil {
				ing.logger.Info("graph extraction: BatchOverlap ignored because SemanticBatching is enabled",
					"requested_overlap", cfg.BatchOverlap)
			}
			cfg.BatchOverlap = 0
		}

		// Why: an overlap >= batch size would never advance the sliding window
		// (stride <= 0), so clamp it to batchSize-1.
		if cfg.BatchOverlap >= cfg.BatchSize {
			clamped := cfg.BatchSize - 1
			if clamped < 0 {
				clamped = 0
			}
			if ing.logger != nil {
				ing.logger.Info("graph extraction: BatchOverlap clamped below BatchSize",
					"requested_overlap", cfg.BatchOverlap, "batch_size", cfg.BatchSize,
					"clamped_overlap", clamped)
			}
			cfg.BatchOverlap = clamped
		}

		ing.graphBatchSize = cfg.BatchSize
		ing.graphBatchOverlap = cfg.BatchOverlap
		ing.graphWorkers = cfg.Workers
		ing.minEdgeWeight = cfg.MinEdgeWeight
		ing.maxEdgesPerChunk = cfg.MaxEdgesPerChunk
		ing.graphDocContextBytes = cfg.DocContextBytes
		ing.semanticBatching = cfg.SemanticBatching
		ing.sequenceEdges = cfg.SequenceEdges
	}
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

// WithImageEmbedding enables image embedding during ingestion.
// When set, extracted images are stored as separate image chunks with
// embeddings from the multimodal provider, enabling cross-modal retrieval
// (e.g. text query "black shirt" finds photos of black shirts).
// The provider must produce vectors in the same space as the text embedding
// provider (e.g. Qwen3-VL-Embedding handles both).
func WithImageEmbedding(p oasis.MultimodalEmbeddingProvider) Option {
	return func(ing *Ingestor) { ing.imageEmbedding = p }
}

// WithBlobStore sets an external blob store for image data. When set,
// image binary data is stored via BlobStore instead of inline base64
// in ChunkMeta.Images. The chunk's ChunkMeta.BlobRef holds the opaque
// reference returned by BlobStore.StoreBlob.
// Without this option, images are stored inline in ChunkMeta.Images.
func WithBlobStore(bs oasis.BlobStore) Option {
	return func(ing *Ingestor) { ing.blobStore = bs }
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

// WithExtractRetries sets the maximum number of attempts for custom extractor
// calls (default 1, meaning no retry). Custom extractors may call external
// services (OCR, LLM-based conversion, document APIs) that can fail transiently.
// Built-in extractors are deterministic and do not benefit from retry.
// Retries use exponential backoff with jitter; context cancellation is respected.
func WithExtractRetries(n int) Option {
	return func(ing *Ingestor) { ing.extractRetries = n }
}

// WithBatchConcurrency sets the number of documents that are ingested
// concurrently during IngestBatch (default 1, sequential).
// In sequential mode chunks are pooled across documents into shared embedding
// batches; in concurrent mode each goroutine runs an independent pipeline.
func WithBatchConcurrency(n int) Option {
	return func(ing *Ingestor) { ing.batchConcurrency = n }
}

// WithBatchCrossDocEdges enables cross-document edge extraction automatically
// at the end of an IngestBatch call (default false).
func WithBatchCrossDocEdges(b bool) Option {
	return func(ing *Ingestor) { ing.batchCrossDocEdges = b }
}

// WithLLMTimeout sets the maximum duration for individual LLM calls during
// graph extraction and contextual enrichment (default 2 minutes). This prevents
// a hung provider.ChatStream / core.Chat call from blocking workers indefinitely,
// which can cause deadlocks in the worker pool.
func WithLLMTimeout(d time.Duration) Option {
	return func(ing *Ingestor) { ing.llmTimeout = d }
}

// CrossDocOption configures ExtractCrossDocumentEdges.
type CrossDocOption func(*crossDocConfig)

type crossDocConfig struct {
	documentIDs         []string
	similarityThreshold float32
	maxPairsPerChunk    int
	batchSize           int
	workers             int
	resume              bool
	progressFunc        func(processed, total int)
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

// CrossDocWithResume enables resume mode. When enabled, documents that were
// successfully processed in a previous run are skipped. Progress is tracked
// via the Store's config mechanism (key: "crossdoc:processed").
func CrossDocWithResume(resume bool) CrossDocOption {
	return func(c *crossDocConfig) { c.resume = resume }
}

// CrossDocWithWorkers sets the number of documents processed concurrently
// during cross-document extraction (default 1, sequential).
func CrossDocWithWorkers(n int) CrossDocOption {
	return func(c *crossDocConfig) { c.workers = n }
}

// CrossDocWithProgressFunc sets a callback invoked after each document is
// processed. The callback receives the number of documents processed so far
// and the total number of documents to process.
func CrossDocWithProgressFunc(fn func(processed, total int)) CrossDocOption {
	return func(c *crossDocConfig) { c.progressFunc = fn }
}
