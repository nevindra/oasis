package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	oasis "github.com/nevindra/oasis"
)

// validRelations maps LLM-output relation strings to typed constants.
var validRelations = map[string]oasis.RelationType{
	"references":  oasis.RelReferences,
	"elaborates":  oasis.RelElaborates,
	"depends_on":  oasis.RelDependsOn,
	"contradicts": oasis.RelContradicts,
	"part_of":     oasis.RelPartOf,
	"sequence":    oasis.RelSequence,
	"caused_by":   oasis.RelCausedBy,
}

const graphExtractionPrompt = `You are a knowledge graph extractor. Analyze the following text chunks and identify relationships between them.

For each relationship found, output a JSON edge with:
- "source": the source chunk ID (see directionality below)
- "target": the target chunk ID (see directionality below)
- "relation": one of: references, elaborates, depends_on, contradicts, part_of, caused_by
- "weight": confidence score from 0.0 to 1.0
- "description": a brief explanation of why this relationship exists (1 sentence)

Relationship types and directionality (source → target):
- references: source cites or mentions content from target
- elaborates: source provides more detail on target's topic (source is the detailed chunk, target is the summary)
- depends_on: source assumes knowledge from target (source is the dependent chunk, target is the prerequisite)
- contradicts: source conflicts with target (either direction is valid; pick the chunk making the stronger claim as source)
- part_of: source is a component or subset of target (source is the part, target is the whole)
- caused_by: source is a consequence of target (source is the effect, target is the cause)

Output ONLY valid JSON in this format:
{"edges":[{"source":"chunk_id","target":"chunk_id","relation":"type","weight":0.0,"description":"why this relationship exists"}]}

If no relationships exist, output: {"edges":[]}
`

const graphDocContextSection = `
The chunks below come from the following document. Use the document's structure (headings, sections) to identify cross-section relationships that the chunks alone might not reveal.

<document_context>
%s
</document_context>
`

// extractGraphEdges sends chunks to an LLM in sliding-window batches and extracts
// relationship edges. overlap controls how many chunks overlap between consecutive
// batches (0 = no overlap). workers controls max concurrent LLM calls (<=1 = sequential).
// docContext, when non-empty, is included in the prompt to give the LLM structural
// context about the source document.
func extractGraphEdges(ctx context.Context, provider oasis.Provider, chunks []oasis.Chunk, batchSize, overlap, workers int, docContext string, logger *slog.Logger) ([]oasis.ChunkEdge, error) {
	if len(chunks) < 2 {
		if logger != nil {
			logger.Info("graph extraction skipped: fewer than 2 chunks",
				"chunk_count", len(chunks))
		}
		return nil, nil
	}
	if batchSize <= 0 {
		batchSize = 5
	}

	stride := batchSize
	if overlap > 0 && overlap < batchSize {
		stride = batchSize - overlap
	}

	// Build sliding-window batches.
	var batches [][]oasis.Chunk
	for i := 0; i < len(chunks); i += stride {
		end := min(i+batchSize, len(chunks))
		b := chunks[i:end]
		if len(b) < 2 {
			continue
		}
		batches = append(batches, b)
	}

	if logger != nil {
		logger.Debug("graph extraction: sliding window batches built",
			"total_batches", len(batches), "stride", stride,
			"chunk_count", len(chunks))
	}

	return extractFromBatches(ctx, provider, batches, workers, docContext, logger)
}

// extractFromBatches runs pre-formed chunk batches through an LLM worker pool
// for relationship extraction. Each batch is sent as one prompt.
// docContext, when non-empty, is prepended to each prompt for structural awareness.
func extractFromBatches(ctx context.Context, provider oasis.Provider, batches [][]oasis.Chunk, workers int, docContext string, logger *slog.Logger) ([]oasis.ChunkEdge, error) {
	if len(batches) == 0 {
		if logger != nil {
			logger.Debug("graph extraction skipped: no valid batches")
		}
		return nil, nil
	}
	if workers <= 0 {
		workers = 1
	}

	type indexedBatch struct {
		chunks []oasis.Chunk
		index  int
	}
	type batchResult struct {
		edges  []oasis.ChunkEdge
		failed bool
	}

	numWorkers := min(workers, len(batches))

	if logger != nil {
		logger.Info("graph extraction worker pool started",
			"batches", len(batches), "workers", numWorkers)
	}

	work := make(chan indexedBatch, len(batches))
	results := make(chan batchResult, len(batches))

	for w := 0; w < numWorkers; w++ {
		go func() {
			for b := range work {
				if ctx.Err() != nil {
					if logger != nil {
						logger.Warn("graph extraction: context cancelled, skipping batch",
							"batch", b.index)
					}
					results <- batchResult{failed: true}
					continue
				}

				var prompt strings.Builder
				prompt.WriteString(graphExtractionPrompt)
				if docContext != "" {
					fmt.Fprintf(&prompt, graphDocContextSection, docContext)
				}
				prompt.WriteString("\nChunks:\n")
				for _, c := range b.chunks {
					fmt.Fprintf(&prompt, "\n[%s]: %s\n", c.ID, c.Content)
				}

				const maxBatchRetries = 3
				var edges []oasis.ChunkEdge
				succeeded := false

				for attempt := 0; attempt < maxBatchRetries; attempt++ {
					if ctx.Err() != nil {
						break
					}

					if attempt > 0 {
						backoff := time.Second * time.Duration(1<<(attempt-1)) // 1s, 2s
						if logger != nil {
							logger.Info("graph extraction: retrying batch",
								"batch", b.index,
								"attempt", attempt+1,
								"backoff", backoff)
						}
						select {
						case <-time.After(backoff):
						case <-ctx.Done():
						}
						if ctx.Err() != nil {
							break
						}
					}

					if logger != nil {
						logger.Debug("graph extraction: sending LLM request",
							"batch", b.index,
							"chunk_count", len(b.chunks),
							"attempt", attempt+1,
							"prompt_bytes", prompt.Len())
					}

					temp := 0.0
					resp, err := provider.Chat(ctx, oasis.ChatRequest{
						Messages: []oasis.ChatMessage{
							{Role: "user", Content: prompt.String()},
						},
						GenerationParams: &oasis.GenerationParams{Temperature: &temp},
					})
					if err != nil {
						if logger != nil {
							logger.Warn("graph extraction: LLM call failed",
								"batch", b.index,
								"attempt", attempt+1,
								"err", err)
						}
						continue
					}

					if logger != nil {
						logger.Debug("graph extraction: LLM response received",
							"batch", b.index,
							"attempt", attempt+1,
							"response_bytes", len(resp.Content))
					}

					edges, err = parseEdgeResponse(resp.Content, b.chunks)
					if err != nil {
						if logger != nil {
							logger.Warn("graph extraction: parse failed",
								"batch", b.index,
								"attempt", attempt+1,
								"response_bytes", len(resp.Content),
								"err", err)
						}
						continue
					}

					succeeded = true
					break
				}

				if !succeeded {
					if logger != nil {
						logger.Warn("graph extraction: batch failed after retries",
							"batch", b.index,
							"max_retries", maxBatchRetries)
					}
					results <- batchResult{failed: true}
					continue
				}

				if logger != nil {
					logger.Debug("graph extraction: batch completed",
						"batch", b.index,
						"edges_extracted", len(edges))
				}
				results <- batchResult{edges: edges}
			}
		}()
	}

	for i, b := range batches {
		work <- indexedBatch{chunks: b, index: i}
	}
	close(work)

	var allEdges []oasis.ChunkEdge
	failedBatches := 0
	for range batches {
		r := <-results
		if r.failed {
			failedBatches++
		} else {
			allEdges = append(allEdges, r.edges...)
		}
	}

	if logger != nil {
		if failedBatches > 0 {
			logger.Warn("graph extraction completed with failures",
				"total_edges", len(allEdges),
				"successful_batches", len(batches)-failedBatches,
				"failed_batches", failedBatches)
		} else {
			logger.Info("graph extraction completed",
				"total_edges", len(allEdges),
				"batches_processed", len(batches))
		}
	}

	return allEdges, nil
}

// deduplicateEdges merges edges with the same (source, target, relation) key,
// keeping the highest weight and its description.
func deduplicateEdges(edges []oasis.ChunkEdge) []oasis.ChunkEdge {
	type key struct {
		source, target string
		relation       oasis.RelationType
	}
	best := make(map[key]oasis.ChunkEdge)
	for _, e := range edges {
		k := key{e.SourceID, e.TargetID, e.Relation}
		if existing, ok := best[k]; !ok || e.Weight > existing.Weight {
			best[k] = e
		}
	}
	result := make([]oasis.ChunkEdge, 0, len(best))
	for _, e := range best {
		result = append(result, e)
	}
	return result
}

// parseEdgeResponse parses LLM JSON output into ChunkEdge values.
// Only edges referencing valid chunk IDs from the batch are kept.
func parseEdgeResponse(content string, chunks []oasis.Chunk) ([]oasis.ChunkEdge, error) {
	var parsed struct {
		Edges []struct {
			Source      string  `json:"source"`
			Target      string  `json:"target"`
			Relation    string  `json:"relation"`
			Weight      float32 `json:"weight"`
			Description string  `json:"description"`
		} `json:"edges"`
	}

	raw := strings.TrimSpace(content)
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		// LLM sometimes wraps JSON in markdown fences — find the object.
		start := strings.Index(raw, "{")
		end := strings.LastIndex(raw, "}")
		if start >= 0 && end > start {
			if err2 := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err2 != nil {
				return nil, err2
			}
		} else {
			return nil, err
		}
	}

	validIDs := make(map[string]bool, len(chunks))
	for _, c := range chunks {
		validIDs[c.ID] = true
	}

	var edges []oasis.ChunkEdge
	for _, e := range parsed.Edges {
		if !validIDs[e.Source] || !validIDs[e.Target] || e.Source == e.Target {
			continue
		}
		rel, ok := validRelations[e.Relation]
		if !ok {
			continue
		}
		if e.Weight <= 0 || e.Weight > 1 {
			continue
		}
		edges = append(edges, oasis.ChunkEdge{
			ID:          oasis.NewID(),
			SourceID:    e.Source,
			TargetID:    e.Target,
			Relation:    rel,
			Weight:      e.Weight,
			Description: e.Description,
		})
	}

	return edges, nil
}

// buildSequenceEdges creates sequence edges between consecutive chunks
// (sorted by ChunkIndex). Only chunks that share the same ParentID are
// linked — this covers both flat chunks (ParentID == "") and children
// within the same parent group.
func buildSequenceEdges(chunks []oasis.Chunk) []oasis.ChunkEdge {
	if len(chunks) < 2 {
		return nil
	}

	// Sort by ChunkIndex to ensure correct ordering.
	sorted := make([]oasis.Chunk, len(chunks))
	copy(sorted, chunks)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ChunkIndex < sorted[j].ChunkIndex
	})

	edges := make([]oasis.ChunkEdge, 0, len(sorted)-1)
	for i := 0; i < len(sorted)-1; i++ {
		// Only link chunks that share the same parent (or both are flat/root).
		if sorted[i].ParentID != sorted[i+1].ParentID {
			continue
		}
		edges = append(edges, oasis.ChunkEdge{
			ID:       oasis.NewID(),
			SourceID: sorted[i].ID,
			TargetID: sorted[i+1].ID,
			Relation: oasis.RelSequence,
			Weight:   1.0,
		})
	}
	return edges
}

// pruneEdges removes edges below minWeight and caps edges per source chunk to maxPerChunk.
func pruneEdges(edges []oasis.ChunkEdge, minWeight float32, maxPerChunk int) []oasis.ChunkEdge {
	// Filter by min weight.
	var filtered []oasis.ChunkEdge
	for _, e := range edges {
		if e.Weight >= minWeight {
			filtered = append(filtered, e)
		}
	}

	if maxPerChunk <= 0 {
		return filtered
	}

	// Group by source, keep top N by weight.
	bySource := make(map[string][]oasis.ChunkEdge)
	for _, e := range filtered {
		bySource[e.SourceID] = append(bySource[e.SourceID], e)
	}

	var result []oasis.ChunkEdge
	for _, group := range bySource {
		sort.Slice(group, func(i, j int) bool {
			return group[i].Weight > group[j].Weight
		})
		if len(group) > maxPerChunk {
			group = group[:maxPerChunk]
		}
		result = append(result, group...)
	}
	return result
}

// buildSemanticBatches groups chunks into batches based on embedding similarity
// rather than sequential position. Uses centroid-based assignment: each chunk is
// compared against existing batch centroids (O(N·K) where K = batches) instead
// of all-pairs comparison (O(N²)). Produces batches where chunks are semantically
// related — improving LLM extraction quality.
func buildSemanticBatches(chunks []oasis.Chunk, batchSize int, logger *slog.Logger) [][]oasis.Chunk {
	if len(chunks) < 2 {
		return nil
	}
	if batchSize <= 0 {
		batchSize = 5
	}

	// Filter to chunks that have embeddings.
	var embedded []oasis.Chunk
	for _, c := range chunks {
		if len(c.Embedding) > 0 {
			embedded = append(embedded, c)
		}
	}
	if len(embedded) < 2 {
		return nil
	}

	// Centroid-based greedy assignment: for each chunk, find the most similar
	// non-full batch by comparing against batch centroids (mean embedding).
	// This is O(N·K·dim) where K grows as batches are created, vs O(N²·dim)
	// for the all-pairs approach.
	type centroidBatch struct {
		chunks   []oasis.Chunk
		centroid []float32
	}

	dim := len(embedded[0].Embedding)
	var batches []*centroidBatch

	for _, c := range embedded {
		bestIdx := -1
		bestSim := float64(-1)

		for i, b := range batches {
			if len(b.chunks) >= batchSize {
				continue
			}
			sim := cosineSimilarity(c.Embedding, b.centroid)
			if sim > bestSim {
				bestSim = sim
				bestIdx = i
			}
		}

		if bestIdx >= 0 {
			b := batches[bestIdx]
			b.chunks = append(b.chunks, c)
			// Update running centroid: new_centroid = old_centroid + (new_vec - old_centroid) / n
			n := float32(len(b.chunks))
			for d := range dim {
				b.centroid[d] += (c.Embedding[d] - b.centroid[d]) / n
			}
		} else {
			// Start new batch with this chunk as seed centroid.
			centroid := make([]float32, dim)
			copy(centroid, c.Embedding)
			batches = append(batches, &centroidBatch{
				chunks:   []oasis.Chunk{c},
				centroid: centroid,
			})
		}
	}

	// Collect batches with >= 2 chunks; singletons are excluded from
	// graph extraction since relationship extraction needs at least 2 chunks.
	var result [][]oasis.Chunk
	var droppedChunks int
	for _, b := range batches {
		if len(b.chunks) >= 2 {
			result = append(result, b.chunks)
		} else {
			droppedChunks += len(b.chunks)
		}
	}
	if droppedChunks > 0 && logger != nil {
		logger.Warn("semantic batching: singleton chunks excluded from graph extraction",
			"dropped_chunks", droppedChunks,
			"batches_kept", len(result))
	}
	return result
}

// cosineSimilarity computes the cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

