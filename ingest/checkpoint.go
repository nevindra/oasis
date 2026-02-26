package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"time"

	oasis "github.com/nevindra/oasis"
)

// checkpointStoreOf returns the CheckpointStore from ing.store if available, or nil.
func (ing *Ingestor) checkpointStoreOf() oasis.CheckpointStore {
	cs, _ := ing.store.(oasis.CheckpointStore)
	return cs
}

// saveCheckpoint writes cp to the store, silently ignoring stores that don't
// implement CheckpointStore.
func (ing *Ingestor) saveCheckpoint(ctx context.Context, cp oasis.IngestCheckpoint) {
	cs := ing.checkpointStoreOf()
	if cs == nil {
		return
	}
	cp.UpdatedAt = oasis.NowUnix()
	if err := cs.SaveCheckpoint(ctx, cp); err != nil && ing.logger != nil {
		ing.logger.Warn("ingest: failed to save checkpoint",
			"checkpoint_id", cp.ID, "status", cp.Status, "err", err)
	}
}

// deleteCheckpoint removes a checkpoint on successful completion.
func (ing *Ingestor) deleteCheckpoint(ctx context.Context, id string) {
	cs := ing.checkpointStoreOf()
	if cs == nil {
		return
	}
	if err := cs.DeleteCheckpoint(ctx, id); err != nil && ing.logger != nil {
		ing.logger.Warn("ingest: failed to delete checkpoint",
			"checkpoint_id", id, "err", err)
	}
}

// ListCheckpoints returns all incomplete ingest checkpoints from the store.
// Returns nil if the store does not implement CheckpointStore.
func (ing *Ingestor) ListCheckpoints(ctx context.Context) ([]oasis.IngestCheckpoint, error) {
	cs := ing.checkpointStoreOf()
	if cs == nil {
		return nil, nil
	}
	return cs.ListCheckpoints(ctx)
}

// ResumeIngest resumes a single-document ingestion from a saved checkpoint.
// checkpointID is obtained from ListCheckpoints.
func (ing *Ingestor) ResumeIngest(ctx context.Context, checkpointID string) (IngestResult, error) {
	cs := ing.checkpointStoreOf()
	if cs == nil {
		return IngestResult{}, fmt.Errorf("ingest: resume requires store to implement CheckpointStore")
	}
	cp, err := cs.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		return IngestResult{}, fmt.Errorf("ingest: load checkpoint: %w", err)
	}
	if cp.Type != "document" {
		return IngestResult{}, fmt.Errorf("ingest: checkpoint %s is type %q, not \"document\"", checkpointID, cp.Type)
	}
	if ing.logger != nil {
		ing.logger.Info("ingest: resuming from checkpoint",
			"checkpoint_id", cp.ID, "source", cp.Source, "status", cp.Status)
	}
	return ing.resumeFromCheckpoint(ctx, cp)
}

// resumeFromCheckpoint continues the pipeline from the stage recorded in cp.
func (ing *Ingestor) resumeFromCheckpoint(ctx context.Context, cp oasis.IngestCheckpoint) (IngestResult, error) {
	now := oasis.NowUnix()
	docID := oasis.NewID()
	source := cp.Source

	// --- re-extract if needed ---
	var text string
	var pageMeta []PageMeta

	switch cp.Status {
	case oasis.CheckpointExtracting:
		// Extract stage never completed; need the original file data which we
		// don't have. The caller must call IngestFile again.
		return IngestResult{}, fmt.Errorf("ingest: checkpoint %s stalled at extracting stage — re-run IngestFile", cp.ID)
	default:
		// Extracted text was persisted.
		text = cp.ExtractedText
		if cp.PageMetaJSON != "" {
			if err := json.Unmarshal([]byte(cp.PageMetaJSON), &pageMeta); err != nil && ing.logger != nil {
				ing.logger.Warn("ingest: resume: failed to unmarshal page meta", "err", err)
			}
		}
	}

	ct := ContentType(cp.ContentType)
	doc := oasis.Document{
		ID:        docID,
		Title:     source,
		Source:    source,
		Content:   text,
		CreatedAt: now,
	}

	var chunks []oasis.Chunk

	switch cp.Status {
	case oasis.CheckpointChunking, oasis.CheckpointEnriching:
		// Chunking did not finish or enrichment stalled; redo chunk+enrich+embed.
		var err error
		chunks, err = ing.chunkAndEmbed(ctx, text, docID, ct, source, pageMeta)
		if err != nil {
			ing.notifyError(source, err)
			return IngestResult{}, err
		}

	case oasis.CheckpointEmbedding:
		// Chunking is done; chunks are in ChunksJSON. Resume embedding from
		// EmbeddedBatches.
		if cp.ChunksJSON == "" {
			// Fallback: re-chunk and embed from scratch.
			var err error
			chunks, err = ing.chunkAndEmbed(ctx, text, docID, ct, source, pageMeta)
			if err != nil {
				ing.notifyError(source, err)
				return IngestResult{}, err
			}
		} else {
			if err := json.Unmarshal([]byte(cp.ChunksJSON), &chunks); err != nil {
				return IngestResult{}, fmt.Errorf("ingest: resume: unmarshal chunks: %w", err)
			}
			// Assign fresh IDs and docID so we don't collide with previous attempt.
			for i := range chunks {
				chunks[i].DocumentID = docID
				chunks[i].ID = oasis.NewID()
			}
			// Re-embed only the batches that didn't finish.
			done := cp.EmbeddedBatches * ing.batchSize
			if done < len(chunks) {
				if err := ing.batchEmbed(ctx, chunks[done:]); err != nil {
					ing.notifyError(source, err)
					return IngestResult{}, err
				}
			}
		}

	case oasis.CheckpointStoring, oasis.CheckpointGraphing:
		// Everything up to store/graph is done; just re-store and re-graph.
		if cp.ChunksJSON != "" {
			if err := json.Unmarshal([]byte(cp.ChunksJSON), &chunks); err != nil {
				return IngestResult{}, fmt.Errorf("ingest: resume: unmarshal chunks: %w", err)
			}
			for i := range chunks {
				chunks[i].DocumentID = docID
				chunks[i].ID = oasis.NewID()
			}
		}
	}

	// --- store ---
	if cp.Status != oasis.CheckpointGraphing {
		cp.Status = oasis.CheckpointStoring
		ing.saveCheckpoint(ctx, cp)
		if err := ing.store.StoreDocument(ctx, doc, chunks); err != nil {
			err = fmt.Errorf("store: %w", err)
			ing.notifyError(source, err)
			return IngestResult{}, err
		}
	}

	// --- graph ---
	cp.Status = oasis.CheckpointGraphing
	ing.saveCheckpoint(ctx, cp)
	if err := ing.extractAndStoreEdges(ctx, chunks); err != nil {
		ing.notifyError(source, err)
		return IngestResult{}, fmt.Errorf("graph extraction: %w", err)
	}

	ing.deleteCheckpoint(ctx, cp.ID)

	result := IngestResult{
		DocumentID: docID,
		Document:   doc,
		ChunkCount: len(chunks),
	}
	if ing.logger != nil {
		ing.logger.Info("ingest: resume completed",
			"doc_id", docID, "source", source, "chunk_count", len(chunks))
	}
	if ing.onSuccess != nil {
		ing.onSuccess(result)
	}
	return result, nil
}

// extractWithRetry calls the extractor, retrying on any non-cancellation error
// up to ing.extractRetries attempts with exponential backoff.
func (ing *Ingestor) extractWithRetry(extractor Extractor, content []byte) (string, error) {
	maxAttempts := ing.extractRetries
	if maxAttempts <= 1 {
		return safeExtract(extractor, content)
	}
	base := time.Second
	var last error
	for i := 0; i < maxAttempts; i++ {
		text, err := safeExtract(extractor, content)
		if err == nil {
			return text, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		last = err
		if i < maxAttempts-1 {
			delay := extractRetryBackoff(base, i)
			time.Sleep(delay)
		}
	}
	return "", last
}

// extractWithMetaRetry is the retry-aware variant for MetadataExtractor.
func (ing *Ingestor) extractWithMetaRetry(me MetadataExtractor, content []byte) (ExtractResult, error) {
	maxAttempts := ing.extractRetries
	if maxAttempts <= 1 {
		return safeExtractWithMeta(me, content)
	}
	base := time.Second
	var last error
	for i := 0; i < maxAttempts; i++ {
		result, err := safeExtractWithMeta(me, content)
		if err == nil {
			return result, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ExtractResult{}, err
		}
		last = err
		if i < maxAttempts-1 {
			delay := extractRetryBackoff(base, i)
			time.Sleep(delay)
		}
	}
	return ExtractResult{}, last
}

// extractRetryBackoff returns the delay before retry attempt i.
func extractRetryBackoff(base time.Duration, i int) time.Duration {
	exp := base * (1 << i)
	jitter := time.Duration(rand.Int63n(int64(exp)/2 + 1))
	return exp + jitter
}
