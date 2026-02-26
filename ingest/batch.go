package ingest

// BatchItem is a single document submitted for batch ingestion.
type BatchItem struct {
	Data     []byte // raw file content
	Filename string // used for content-type detection and as source
	Title    string // optional; defaults to Filename when empty
}

// BatchResult is the outcome of an IngestBatch or ResumeBatch call.
type BatchResult struct {
	Succeeded  []IngestResult // documents that completed successfully
	Failed     []BatchError   // documents that failed after all retries
	Checkpoint string         // checkpoint ID for resuming an interrupted batch
}

// BatchError pairs a failed BatchItem with the error that caused the failure.
type BatchError struct {
	Item  BatchItem
	Error error
}
