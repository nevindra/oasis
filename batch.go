package oasis

import (
	"context"
	"time"
)

// --- Batch execution ---

// BatchState represents the lifecycle state of a batch job.
type BatchState string

const (
	BatchPending   BatchState = "pending"
	BatchRunning   BatchState = "running"
	BatchSucceeded BatchState = "succeeded"
	BatchFailed    BatchState = "failed"
	BatchCancelled BatchState = "cancelled"
	BatchExpired   BatchState = "expired"
)

// BatchStats holds aggregate counts for a batch job's requests.
type BatchStats struct {
	TotalCount     int `json:"total_count"`
	SucceededCount int `json:"succeeded_count"`
	FailedCount    int `json:"failed_count"`
}

// BatchJob represents an asynchronous batch processing job.
// Use BatchStatus to poll for state changes and BatchChatResults or
// BatchEmbedResults to retrieve completed output.
type BatchJob struct {
	ID          string     `json:"id"`
	State       BatchState `json:"state"`
	DisplayName string     `json:"display_name,omitempty"`
	Stats       BatchStats `json:"stats"`
	CreateTime  time.Time  `json:"create_time"`
	UpdateTime  time.Time  `json:"update_time"`
}

// BatchProvider extends Provider with asynchronous batch chat capabilities.
// Batch requests are processed offline at reduced cost. Use BatchStatus to poll
// job progress and BatchChatResults to retrieve completed responses.
type BatchProvider interface {
	// BatchChat submits multiple chat requests as a single batch job.
	// Returns the created job with its ID for status tracking.
	BatchChat(ctx context.Context, requests []ChatRequest) (BatchJob, error)

	// BatchStatus returns the current state of a batch job.
	BatchStatus(ctx context.Context, jobID string) (BatchJob, error)

	// BatchChatResults retrieves chat responses for a completed batch job.
	// Returns error if the job has not yet succeeded.
	BatchChatResults(ctx context.Context, jobID string) ([]ChatResponse, error)

	// BatchCancel requests cancellation of a running or pending batch job.
	BatchCancel(ctx context.Context, jobID string) error
}

// BatchEmbeddingProvider extends EmbeddingProvider with batch embedding capabilities.
// Each element in the texts slice passed to BatchEmbed is a group of strings to embed.
type BatchEmbeddingProvider interface {
	// BatchEmbed submits multiple embedding requests as a single batch job.
	BatchEmbed(ctx context.Context, texts [][]string) (BatchJob, error)

	// BatchEmbedStatus returns the current state of a batch embedding job.
	BatchEmbedStatus(ctx context.Context, jobID string) (BatchJob, error)

	// BatchEmbedResults retrieves embedding vectors for a completed batch job.
	// Returns one vector per input text group.
	BatchEmbedResults(ctx context.Context, jobID string) ([][]float32, error)
}
