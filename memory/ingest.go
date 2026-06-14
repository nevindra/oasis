// memory/ingest.go
package memory

import (
	"context"
	"log/slog"

	"github.com/nevindra/oasis/core"
)

// IngestProcessor transforms an IngestContext as part of the write pipeline.
// Processors run sequentially; an error from any processor aborts the chain
// and is logged but not propagated to the caller's request path
// (the ingest pipeline runs in the background).
//
// Processors may append to in.Candidates, mutate fields on in, write to
// in.Store, or short-circuit by returning a non-nil error.
type IngestProcessor interface {
	Process(ctx context.Context, in *IngestContext) error
}

// IngestContext carries everything an ingest processor needs to do its job.
type IngestContext struct {
	AgentName string
	Task      core.AgentTask // user input, attachments, thread/chat IDs
	UserText  string
	AsstText  string
	Steps     []core.StepTrace

	// Candidates is the working set of items being prepared for upsert.
	// Processors append; the terminal Upserter writes them all.
	Candidates []core.MemoryItem

	// Output flags set by processors.
	ThreadCreated bool // set by EnsureThread when a new row was created

	// Wiring
	Store     core.Store           // conversation store (threads, messages)
	ItemStore core.MemoryItemStore // memory items; may be nil when store doesn't implement it
	Embedding core.EmbeddingProvider
	Provider  core.Provider
	Logger    *slog.Logger
}

// runIngestPipeline runs the processors in order, stopping on the first error.
func runIngestPipeline(ctx context.Context, in *IngestContext, procs []IngestProcessor) error {
	for _, p := range procs {
		if err := p.Process(ctx, in); err != nil {
			return err
		}
	}
	return nil
}
