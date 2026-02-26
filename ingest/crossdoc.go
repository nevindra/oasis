package ingest

import (
	"context"
	"encoding/json"
	"fmt"

	oasis "github.com/nevindra/oasis"
)

// DocumentChunkLister is an optional Store capability for listing chunks
// belonging to a specific document. Store implementations can implement this
// to enable cross-document edge extraction. Callers discover it via type assertion.
type DocumentChunkLister interface {
	GetChunksByDocument(ctx context.Context, docID string) ([]oasis.Chunk, error)
}

const crossDocConfigKey = "crossdoc:processed"

// ExtractCrossDocumentEdges discovers and stores edges between chunks from
// different documents. It finds similar chunks across documents via vector search,
// then sends them to the LLM for relationship extraction.
//
// This method requires WithGraphExtraction to be configured. The Store must
// implement DocumentChunkLister (to list chunks per document). Call it after
// ingesting a batch of documents — it is not run automatically during IngestFile/IngestText.
//
// When CrossDocWithResume(true) is set, processed documents are tracked in the
// Store's config and skipped on subsequent runs. Progress is saved after each
// document, so interrupted runs can be resumed without losing progress.
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

	// Load processed doc IDs for resume.
	processedDocs := make(map[string]bool)
	if cfg.resume {
		processedDocs, err = ing.loadProcessedDocs(ctx)
		if err != nil && ing.logger != nil {
			ing.logger.Warn("cross-doc: failed to load resume state, starting fresh", "err", err)
		}
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

	// 2. Process each document: discover pairs → extract edges → store → save progress.
	type chunkPair struct {
		local  oasis.Chunk
		remote oasis.Chunk
	}
	globalSeen := make(map[string]bool) // "localID:remoteID" across all docs
	totalEdges := 0

	for i, doc := range docs {
		if ctx.Err() != nil {
			if ing.logger != nil {
				ing.logger.Warn("cross-doc: context cancelled", "processed", i, "remaining", len(docs)-i)
			}
			break
		}

		chunks, err := dcl.GetChunksByDocument(ctx, doc.ID)
		if err != nil {
			if ing.logger != nil {
				ing.logger.Warn("cross-doc: get chunks failed", "doc", doc.Source, "err", err)
			}
			continue
		}

		// Discover pairs for this document.
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
				if globalSeen[key1] || globalSeen[key2] {
					continue
				}
				globalSeen[key1] = true
				pairs = append(pairs, chunkPair{local: c, remote: cand.Chunk})
			}
		}

		if len(pairs) == 0 {
			// No pairs — mark as processed and move on.
			if cfg.resume {
				processedDocs[doc.ID] = true
				_ = ing.saveProcessedDocs(ctx, processedDocs)
			}
			continue
		}

		// Collect unique chunks for LLM extraction.
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

		// Extract edges via LLM.
		edges, err := extractGraphEdges(ctx, ing.graphProvider, batchChunks, cfg.batchSize, 0, ing.graphWorkers, ing.logger)
		if err != nil {
			if ing.logger != nil {
				ing.logger.Error("cross-doc: edge extraction failed", "doc", doc.Source, "err", err)
			}
			continue
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
				continue
			}
		}

		totalEdges += len(edges)

		if ing.logger != nil {
			ing.logger.Info("cross-doc: document processed",
				"doc", doc.Source, "pairs", len(pairs), "edges", len(edges),
				"progress", fmt.Sprintf("%d/%d", i+1, len(docs)))
		}

		// Save progress.
		if cfg.resume {
			processedDocs[doc.ID] = true
			if err := ing.saveProcessedDocs(ctx, processedDocs); err != nil && ing.logger != nil {
				ing.logger.Warn("cross-doc: failed to save progress", "err", err)
			}
		}
	}

	if ing.logger != nil {
		ing.logger.Info("cross-doc extraction completed",
			"edges_stored", totalEdges,
			"documents_processed", len(docs))
	}

	return totalEdges, nil
}

func (ing *Ingestor) loadProcessedDocs(ctx context.Context) (map[string]bool, error) {
	raw, err := ing.store.GetConfig(ctx, crossDocConfigKey)
	if err != nil || raw == "" {
		return make(map[string]bool), err
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return make(map[string]bool), err
	}
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m, nil
}

func (ing *Ingestor) saveProcessedDocs(ctx context.Context, docs map[string]bool) error {
	ids := make([]string, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return ing.store.SetConfig(ctx, crossDocConfigKey, string(data))
}
