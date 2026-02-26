package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	oasis "github.com/nevindra/oasis"
)

// DocumentChunkLister is an optional Store capability for listing chunks
// belonging to a specific document. Store implementations can implement this
// to enable cross-document edge extraction. Callers discover it via type assertion.
type DocumentChunkLister interface {
	GetChunksByDocument(ctx context.Context, docID string) ([]oasis.Chunk, error)
}

// crossDocState is the JSON payload persisted in an IngestCheckpoint of type "crossdoc".
type crossDocState struct {
	ProcessedDocIDs []string `json:"processed_doc_ids"`
}

// ExtractCrossDocumentEdges discovers and stores edges between chunks from
// different documents. It finds similar chunks across documents via vector search,
// then sends them to the LLM for relationship extraction.
//
// This method requires WithGraphExtraction to be configured. The Store must
// implement DocumentChunkLister (to list chunks per document). Call it after
// ingesting a batch of documents — it is not run automatically during IngestFile/IngestText.
//
// When CrossDocWithResume(true) is set, processed documents are tracked via
// CheckpointStore (if available) and skipped on subsequent runs. Progress is
// saved after each document so interrupted runs can be resumed.
//
// Returns the number of edges created.
func (ing *Ingestor) ExtractCrossDocumentEdges(ctx context.Context, opts ...CrossDocOption) (int, error) {
	if ing.graphProvider == nil {
		return 0, fmt.Errorf("cross-document extraction requires WithGraphExtraction")
	}

	gs, ok := ing.store.(oasis.GraphStore)
	if !ok {
		if ing.logger != nil {
			ing.logger.Warn("cross-doc: store does not implement GraphStore, skipping")
		}
		return 0, nil
	}

	dcl, ok := ing.store.(DocumentChunkLister)
	if !ok {
		return 0, fmt.Errorf("cross-document extraction requires store to implement DocumentChunkLister")
	}

	cfg := crossDocConfig{
		similarityThreshold: 0.5,
		maxPairsPerChunk:    3,
		batchSize:           5,
	}
	for _, o := range opts {
		o(&cfg)
	}

	if ing.logger != nil {
		ing.logger.Info("cross-doc extraction started",
			"similarity_threshold", cfg.similarityThreshold,
			"max_pairs_per_chunk", cfg.maxPairsPerChunk,
			"batch_size", cfg.batchSize,
			"resume", cfg.resume,
			"document_filter_count", len(cfg.documentIDs))
	}

	// Load or create the crossdoc checkpoint.
	var cpID string
	processedDocs := make(map[string]bool)

	if cfg.resume {
		cs := ing.checkpointStoreOf()
		if cs != nil {
			cps, err := cs.ListCheckpoints(ctx)
			if err == nil {
				for _, cp := range cps {
					if cp.Type == "crossdoc" {
						cpID = cp.ID
						var state crossDocState
						if cp.BatchData != "" {
							if jerr := json.Unmarshal([]byte(cp.BatchData), &state); jerr == nil {
								for _, id := range state.ProcessedDocIDs {
									processedDocs[id] = true
								}
							}
						}
						break
					}
				}
			}
		}
	}

	totalEdges, err := ing.runCrossDoc(ctx, cfg, gs, dcl, cpID, processedDocs)
	return totalEdges, err
}

// ResumeCrossDocExtraction resumes a previously interrupted cross-document
// extraction using a checkpoint ID from ListCheckpoints.
func (ing *Ingestor) ResumeCrossDocExtraction(ctx context.Context, checkpointID string, opts ...CrossDocOption) (int, error) {
	cs := ing.checkpointStoreOf()
	if cs == nil {
		return 0, fmt.Errorf("ingest: resume cross-doc requires store to implement CheckpointStore")
	}
	cp, err := cs.LoadCheckpoint(ctx, checkpointID)
	if err != nil {
		return 0, fmt.Errorf("ingest: load cross-doc checkpoint: %w", err)
	}
	if cp.Type != "crossdoc" {
		return 0, fmt.Errorf("ingest: checkpoint %s is type %q, not \"crossdoc\"", checkpointID, cp.Type)
	}

	var state crossDocState
	if cp.BatchData != "" {
		_ = json.Unmarshal([]byte(cp.BatchData), &state)
	}
	processedDocs := make(map[string]bool, len(state.ProcessedDocIDs))
	for _, id := range state.ProcessedDocIDs {
		processedDocs[id] = true
	}

	if ing.graphProvider == nil {
		return 0, fmt.Errorf("cross-document extraction requires WithGraphExtraction")
	}
	gs, ok := ing.store.(oasis.GraphStore)
	if !ok {
		return 0, nil
	}
	dcl, ok := ing.store.(DocumentChunkLister)
	if !ok {
		return 0, fmt.Errorf("cross-document extraction requires store to implement DocumentChunkLister")
	}

	cfg := crossDocConfig{
		similarityThreshold: 0.5,
		maxPairsPerChunk:    3,
		batchSize:           5,
		resume:              true,
	}
	for _, o := range opts {
		o(&cfg)
	}

	return ing.runCrossDoc(ctx, cfg, gs, dcl, checkpointID, processedDocs)
}

// runCrossDoc is the shared implementation for ExtractCrossDocumentEdges and
// ResumeCrossDocExtraction.
func (ing *Ingestor) runCrossDoc(
	ctx context.Context,
	cfg crossDocConfig,
	gs oasis.GraphStore,
	dcl DocumentChunkLister,
	cpID string,
	processedDocs map[string]bool,
) (int, error) {
	// 1. Get documents to process.
	docs, err := ing.store.ListDocuments(ctx, 0)
	if err != nil {
		return 0, fmt.Errorf("list documents: %w", err)
	}

	// Filter to requested document IDs if specified.
	if len(cfg.documentIDs) > 0 {
		idSet := make(map[string]bool, len(cfg.documentIDs))
		for _, id := range cfg.documentIDs {
			idSet[id] = true
		}
		var filtered []oasis.Document
		for _, d := range docs {
			if idSet[d.ID] {
				filtered = append(filtered, d)
			}
		}
		docs = filtered
	}

	// Filter out already-processed docs.
	if len(processedDocs) > 0 {
		var remaining []oasis.Document
		for _, d := range docs {
			if !processedDocs[d.ID] {
				remaining = append(remaining, d)
			}
		}
		if ing.logger != nil {
			ing.logger.Info("cross-doc: resume filtering",
				"total", len(docs), "already_processed", len(docs)-len(remaining), "remaining", len(remaining))
		}
		docs = remaining
	}

	if ing.logger != nil {
		ing.logger.Info("cross-doc: documents to process", "doc_count", len(docs))
	}

	// Ensure a checkpoint exists for resume tracking.
	if cfg.resume {
		cs := ing.checkpointStoreOf()
		if cs != nil && cpID == "" {
			now := oasis.NowUnix()
			cp := oasis.IngestCheckpoint{
				ID:        oasis.NewID(),
				Type:      "crossdoc",
				Source:    "cross-doc extraction",
				Status:    oasis.CheckpointEmbedding,
				CreatedAt: now,
				UpdatedAt: now,
			}
			ing.saveCheckpoint(ctx, cp)
			cpID = cp.ID
		}
	}

	saveCrossDocProgress := func() {
		if !cfg.resume || cpID == "" {
			return
		}
		ids := make([]string, 0, len(processedDocs))
		for id := range processedDocs {
			ids = append(ids, id)
		}
		data, _ := json.Marshal(crossDocState{ProcessedDocIDs: ids})
		cp := oasis.IngestCheckpoint{
			ID:        cpID,
			Type:      "crossdoc",
			Source:    "cross-doc extraction",
			Status:    oasis.CheckpointEmbedding,
			BatchData: string(data),
			UpdatedAt: oasis.NowUnix(),
		}
		ing.saveCheckpoint(ctx, cp)
	}

	// 2. Process each document.
	type chunkPair struct {
		local  oasis.Chunk
		remote oasis.Chunk
	}

	var mu sync.Mutex
	globalSeen := make(map[string]bool)
	var totalEdges atomic.Int64

	processDoc := func(doc oasis.Document) {
		chunks, err := dcl.GetChunksByDocument(ctx, doc.ID)
		if err != nil {
			if ing.logger != nil {
				ing.logger.Warn("cross-doc: get chunks failed", "doc", doc.Source, "err", err)
			}
			return
		}

		var pairs []chunkPair
		for _, c := range chunks {
			if len(c.Embedding) == 0 {
				continue
			}
			candidates, err := ing.store.SearchChunks(ctx, c.Embedding, cfg.maxPairsPerChunk, oasis.ByExcludeDocument(doc.ID))
			if err != nil {
				continue
			}
			for _, cand := range candidates {
				if cand.Score < cfg.similarityThreshold {
					continue
				}
				key1 := c.ID + ":" + cand.ID
				key2 := cand.ID + ":" + c.ID
				mu.Lock()
				seen := globalSeen[key1] || globalSeen[key2]
				if !seen {
					globalSeen[key1] = true
				}
				mu.Unlock()
				if seen {
					continue
				}
				pairs = append(pairs, chunkPair{local: c, remote: cand.Chunk})
			}
		}

		if len(pairs) == 0 {
			mu.Lock()
			processedDocs[doc.ID] = true
			saveCrossDocProgress()
			mu.Unlock()
			return
		}

		var batchChunks []oasis.Chunk
		added := make(map[string]bool)
		for _, p := range pairs {
			if !added[p.local.ID] {
				batchChunks = append(batchChunks, p.local)
				added[p.local.ID] = true
			}
			if !added[p.remote.ID] {
				batchChunks = append(batchChunks, p.remote)
				added[p.remote.ID] = true
			}
		}

		edges, err := extractGraphEdges(ctx, ing.graphProvider, batchChunks, cfg.batchSize, 0, ing.graphWorkers, "", ing.logger)
		if err != nil {
			if ing.logger != nil {
				ing.logger.Error("cross-doc: edge extraction failed", "doc", doc.Source, "err", err)
			}
			return
		}

		edges = deduplicateEdges(edges)
		if ing.minEdgeWeight > 0 || ing.maxEdgesPerChunk > 0 {
			edges = pruneEdges(edges, ing.minEdgeWeight, ing.maxEdgesPerChunk)
		}

		if len(edges) > 0 {
			if err := gs.StoreEdges(ctx, edges); err != nil {
				if ing.logger != nil {
					ing.logger.Error("cross-doc: store edges failed", "doc", doc.Source, "err", err)
				}
				return
			}
		}

		totalEdges.Add(int64(len(edges)))

		if ing.logger != nil {
			ing.logger.Info("cross-doc: document processed",
				"doc", doc.Source, "pairs", len(pairs), "edges", len(edges))
		}

		mu.Lock()
		processedDocs[doc.ID] = true
		saveCrossDocProgress()
		mu.Unlock()
	}

	numWorkers := max(cfg.workers, 1)
	if numWorkers == 1 {
		// Sequential processing.
		for i, doc := range docs {
			if ctx.Err() != nil {
				if ing.logger != nil {
					ing.logger.Warn("cross-doc: context cancelled", "processed", i, "remaining", len(docs)-i)
				}
				break
			}
			processDoc(doc)
		}
	} else {
		// Parallel worker pool.
		if ing.logger != nil {
			ing.logger.Info("cross-doc: parallel processing",
				"workers", numWorkers, "documents", len(docs))
		}
		work := make(chan oasis.Document, len(docs))
		var wg sync.WaitGroup
		for w := 0; w < min(numWorkers, len(docs)); w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for doc := range work {
					if ctx.Err() != nil {
						return
					}
					processDoc(doc)
				}
			}()
		}
		for _, doc := range docs {
			work <- doc
		}
		close(work)
		wg.Wait()
	}

	// Delete checkpoint on successful completion.
	if cpID != "" {
		ing.deleteCheckpoint(ctx, cpID)
	}

	total := int(totalEdges.Load())
	if ing.logger != nil {
		ing.logger.Info("cross-doc extraction completed",
			"edges_stored", total,
			"documents_processed", len(docs))
	}

	return total, nil
}
