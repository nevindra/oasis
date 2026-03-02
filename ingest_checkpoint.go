package oasis

// IngestCheckpoint persists the state of an in-progress document ingestion so
// it can be resumed after a crash or context cancellation.
//
// Checkpoints are written after each pipeline stage and deleted on successful
// completion. If the Store does not implement CheckpointStore, checkpointing is
// silently disabled.
type IngestCheckpoint struct {
	ID     string           `json:"id"`      // UUIDv7
	Type   string           `json:"type"`    // "document", "batch", "crossdoc"
	Source string           `json:"source"`  // filename or source URL
	Status CheckpointStatus `json:"status"`

	// DocumentID is the ID assigned to the document when StoreDocument succeeds.
	// Used on resume to avoid creating orphan duplicates.
	DocumentID string `json:"document_id,omitempty"`

	// Populated after extract stage.
	ContentType   string `json:"content_type,omitempty"`
	ExtractedText string `json:"extracted_text,omitempty"`

	// Populated after chunk stage.
	// Serialised as JSON inside the store blob; ingest package owns the concrete type.
	ChunksJSON string `json:"chunks_json,omitempty"`

	// Per-batch embedding progress: number of batches whose embeddings have
	// been written back into ChunksJSON.
	EmbeddedBatches int `json:"embedded_batches,omitempty"`

	// PageMetaJSON holds the serialised []PageMeta from the extractor.
	PageMetaJSON string `json:"page_meta_json,omitempty"`

	// BatchData holds type-specific payload serialised as JSON
	// (e.g. completed document IDs for batch checkpoints).
	BatchData string `json:"batch_data,omitempty"`

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// CheckpointStatus identifies which pipeline stage a checkpoint represents.
type CheckpointStatus string

const (
	CheckpointExtracting CheckpointStatus = "extracting"
	CheckpointChunking   CheckpointStatus = "chunking"
	CheckpointEnriching  CheckpointStatus = "enriching"
	CheckpointEmbedding  CheckpointStatus = "embedding"
	CheckpointStoring    CheckpointStatus = "storing"
	CheckpointGraphing   CheckpointStatus = "graphing"
)
