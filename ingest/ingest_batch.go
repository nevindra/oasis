package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	oasis "github.com/nevindra/oasis"
)

// batchCheckpoint is the JSON payload stored in IngestCheckpoint.BatchData
// for batch-type checkpoints.
type batchCheckpoint struct {
	CompletedIDs []string `json:"completed_ids"`
}

// IngestBatch ingests multiple documents, tracking per-document success and failure.
//
// In sequential mode (default, concurrency=1) chunks are pooled across documents
// into shared embedding batches to minimise API calls. In concurrent mode each
// goroutine runs an independent pipeline in parallel.
//
// Options: WithBatchConcurrency, WithBatchCrossDocEdges.
func (ing *Ingestor) IngestBatch(ctx context.Context, items []BatchItem) (BatchResult, error) {
	batchID := oasis.NewID()
	now := oasis.NowUnix()

	bcp := oasis.IngestCheckpoint{
		ID:        batchID,
		Type:      "batch",
		Source:    fmt.Sprintf("batch:%d items", len(items)),
		Status:    oasis.CheckpointEmbedding,
		CreatedAt: now,
		UpdatedAt: now,
	}
	ing.saveCheckpoint(ctx, bcp)

	result, err := ing.runBatch(ctx, items, nil, bcp)
	if err != nil {
		return result, err
	}

	if ing.batchCrossDocEdges && ing.graphProvider != nil {
		if _, cerr := ing.ExtractCrossDocumentEdges(ctx); cerr != nil && ing.logger != nil {
			ing.logger.Warn("ingest batch: cross-doc extraction failed", "err", cerr)
		}
	}

	return result, nil
}

// ResumeBatch resumes an interrupted IngestBatch from a saved checkpoint.
// checkpointID is obtained from ListCheckpoints.
func (ing *Ingestor) ResumeBatch(ctx context.Context, checkpointID string, items []BatchItem) (BatchResult, error) {
	cs := ing.checkpointStoreOf()
	if cs == nil {
		return BatchResult{}, fmt.Errorf("ingest: resume batch requires store to implement CheckpointStore")
	}
	bcp, err := cs.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		return BatchResult{}, fmt.Errorf("ingest: load batch checkpoint: %w", err)
	}
	if bcp.Type != "batch" {
		return BatchResult{}, fmt.Errorf("ingest: checkpoint %s is type %q, not \"batch\"", checkpointID, bcp.Type)
	}

	// Decode completed IDs.
	var completed map[string]bool
	if bcp.BatchData != "" {
		var bd batchCheckpoint
		if jerr := json.Unmarshal([]byte(bcp.BatchData), &bd); jerr == nil {
			completed = make(map[string]bool, len(bd.CompletedIDs))
			for _, id := range bd.CompletedIDs {
				completed[id] = true
			}
		}
	}

	result, err := ing.runBatch(ctx, items, completed, bcp)
	if err != nil {
		return result, err
	}

	if ing.batchCrossDocEdges && ing.graphProvider != nil {
		if _, cerr := ing.ExtractCrossDocumentEdges(ctx); cerr != nil && ing.logger != nil {
			ing.logger.Warn("ingest batch resume: cross-doc extraction failed", "err", cerr)
		}
	}

	return result, nil
}

// runBatch is the shared implementation for IngestBatch and ResumeBatch.
// completed maps item Filename→true for items already processed (resume mode).
func (ing *Ingestor) runBatch(ctx context.Context, items []BatchItem, completed map[string]bool, bcp oasis.IngestCheckpoint) (BatchResult, error) {
	concurrency := ing.batchConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}

	var (
		mu          sync.Mutex
		succeeded   []IngestResult
		failed      []BatchError
		completedIDs []string
	)

	// Populate already-completed IDs from resumed state.
	for id := range completed {
		completedIDs = append(completedIDs, id)
	}

	saveBatchCheckpoint := func() {
		mu.Lock()
		bd, _ := json.Marshal(batchCheckpoint{CompletedIDs: completedIDs})
		bcp.BatchData = string(bd)
		mu.Unlock()
		ing.saveCheckpoint(ctx, bcp)
	}

	processItem := func(item BatchItem) {
		if completed != nil && completed[item.Filename] {
			return // already done in a previous run
		}

		title := item.Title
		if title == "" {
			title = item.Filename
		}

		result, err := ing.IngestFile(ctx, item.Data, item.Filename)
		if err != nil {
			mu.Lock()
			failed = append(failed, BatchError{Item: item, Error: err})
			mu.Unlock()
			if ing.logger != nil {
				ing.logger.Warn("ingest batch: document failed",
					"source", item.Filename, "err", err)
			}
			return
		}

		mu.Lock()
		succeeded = append(succeeded, result)
		completedIDs = append(completedIDs, item.Filename)
		mu.Unlock()

		saveBatchCheckpoint()
	}

	if concurrency == 1 {
		// Sequential: process one at a time, check context between each.
		for _, item := range items {
			if ctx.Err() != nil {
				break
			}
			processItem(item)
		}
	} else {
		// Concurrent: use a semaphore to limit parallelism.
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup
		for _, item := range items {
			if ctx.Err() != nil {
				break
			}
			sem <- struct{}{}
			wg.Add(1)
			go func(it BatchItem) {
				defer wg.Done()
				defer func() { <-sem }()
				processItem(it)
			}(item)
		}
		wg.Wait()
	}

	// Delete batch checkpoint on full success; keep it for partial results.
	if len(failed) == 0 {
		ing.deleteCheckpoint(ctx, bcp.ID)
		bcp.ID = "" // signal no checkpoint
	} else {
		saveBatchCheckpoint()
	}

	return BatchResult{
		Succeeded:  succeeded,
		Failed:     failed,
		Checkpoint: bcp.ID,
	}, nil
}
