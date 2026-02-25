package ingest

import (
	"context"
	"fmt"

	oasis "github.com/nevindra/oasis"
)

// DocumentChunkLister is an optional Store capability for listing chunks
// belonging to a specific document. Store implementations can implement this
// to enable cross-document edge extraction. Callers discover it via type assertion.
type DocumentChunkLister interface {
	GetChunksByDocument(ctx context.Context, docID string) ([]oasis.Chunk, error)
}

// ExtractCrossDocumentEdges discovers and stores edges between chunks from
// different documents. It finds similar chunks across documents via vector search,
// then sends them to the LLM for relationship extraction.
//
// This method requires WithGraphExtraction to be configured. The Store must
// implement DocumentChunkLister (to list chunks per document). Call it after
// ingesting a batch of documents — it is not run automatically during IngestFile/IngestText.
//
// Returns the number of edges created.
func (ing *Ingestor) ExtractCrossDocumentEdges(ctx context.Context, opts ...CrossDocOption) (int, error) {
	if ing.graphProvider == nil {
		return 0, fmt.Errorf("cross-document extraction requires WithGraphExtraction")
	}

	gs, ok := ing.store.(oasis.GraphStore)
	if !ok {
		return 0, nil // store doesn't support graph, skip silently
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

	// 2. For each document, find similar chunks from other documents.
	type chunkPair struct {
		local  oasis.Chunk
		remote oasis.Chunk
	}
	var allPairs []chunkPair
	seen := make(map[string]bool) // "localID:remoteID" to avoid duplicates

	for _, doc := range docs {
		if ctx.Err() != nil {
			break
		}

		chunks, err := dcl.GetChunksByDocument(ctx, doc.ID)
		if err != nil {
			if ing.logger != nil {
				ing.logger.Warn("cross-doc: get chunks failed", "doc", doc.ID, "err", err)
			}
			continue
		}

		for _, c := range chunks {
			if len(c.Embedding) == 0 {
				continue // skip chunks without embeddings
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
				if seen[key1] || seen[key2] {
					continue
				}
				seen[key1] = true
				allPairs = append(allPairs, chunkPair{local: c, remote: cand.Chunk})
			}
		}
	}

	if len(allPairs) == 0 {
		return 0, nil
	}

	// 3. Batch pairs and extract edges.
	var batchChunks []oasis.Chunk
	added := make(map[string]bool)
	for _, p := range allPairs {
		if !added[p.local.ID] {
			batchChunks = append(batchChunks, p.local)
			added[p.local.ID] = true
		}
		if !added[p.remote.ID] {
			batchChunks = append(batchChunks, p.remote)
			added[p.remote.ID] = true
		}
	}

	edges, err := extractGraphEdges(ctx, ing.graphProvider, batchChunks, cfg.batchSize, 0, ing.graphWorkers, ing.logger)
	if err != nil {
		return 0, fmt.Errorf("extract edges: %w", err)
	}

	edges = deduplicateEdges(edges)

	if ing.minEdgeWeight > 0 || ing.maxEdgesPerChunk > 0 {
		edges = pruneEdges(edges, ing.minEdgeWeight, ing.maxEdgesPerChunk)
	}

	if len(edges) == 0 {
		return 0, nil
	}

	if err := gs.StoreEdges(ctx, edges); err != nil {
		return 0, fmt.Errorf("store edges: %w", err)
	}

	return len(edges), nil
}
