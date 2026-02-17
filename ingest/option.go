package ingest

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
