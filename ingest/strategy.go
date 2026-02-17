package ingest

// ChunkStrategy determines how text is chunked.
type ChunkStrategy int

const (
	// StrategyFlat uses single-level chunking (default).
	StrategyFlat ChunkStrategy = iota

	// StrategyParentChild uses two-level hierarchical chunking.
	// Child chunks (small) are embedded for matching; parent chunks (large)
	// provide rich context to the LLM. On retrieval: match children, return parents.
	StrategyParentChild
)
