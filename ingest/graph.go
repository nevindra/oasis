package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	oasis "github.com/nevindra/oasis"
)

// validRelations maps LLM-output relation strings to typed constants.
var validRelations = map[string]oasis.RelationType{
	"references":  oasis.RelReferences,
	"elaborates":  oasis.RelElaborates,
	"depends_on":  oasis.RelDependsOn,
	"contradicts": oasis.RelContradicts,
	"part_of":     oasis.RelPartOf,
	"similar_to":  oasis.RelSimilarTo,
	"sequence":    oasis.RelSequence,
	"caused_by":   oasis.RelCausedBy,
}

const graphExtractionPrompt = `You are a knowledge graph extractor. Analyze the following text chunks and identify relationships between them.

For each relationship found, output a JSON edge with:
- "source": the chunk ID that holds the relationship
- "target": the chunk ID being referenced
- "relation": one of: references, elaborates, depends_on, contradicts, part_of, similar_to, sequence, caused_by
- "weight": confidence score from 0.0 to 1.0
- "description": a brief explanation of why this relationship exists (1 sentence)

Relationship type definitions:
- references: chunk A cites or mentions content from chunk B
- elaborates: chunk A provides more detail on chunk B's topic
- depends_on: chunk A assumes knowledge from chunk B
- contradicts: chunk A conflicts with chunk B
- part_of: chunk A is a component or subset of chunk B
- similar_to: chunks cover overlapping topics
- sequence: chunk A follows chunk B in logical order
- caused_by: chunk A is a consequence of chunk B

Output ONLY valid JSON in this format:
{"edges":[{"source":"chunk_id","target":"chunk_id","relation":"type","weight":0.0,"description":"why this relationship exists"}]}

If no relationships exist, output: {"edges":[]}

Chunks:
`

// extractGraphEdges sends chunks to an LLM in batches and extracts relationship edges.
// overlap controls how many chunks overlap between consecutive batches (0 = no overlap).
// workers controls max concurrent LLM calls (<=1 = sequential).
func extractGraphEdges(ctx context.Context, provider oasis.Provider, chunks []oasis.Chunk, batchSize, overlap, workers int, logger *slog.Logger) ([]oasis.ChunkEdge, error) {
	if len(chunks) < 2 {
		return nil, nil
	}
	if batchSize <= 0 {
		batchSize = 5
	}
	if workers <= 0 {
		workers = 1
	}

	stride := batchSize
	if overlap > 0 && overlap < batchSize {
		stride = batchSize - overlap
	}

	// Build all batch windows upfront.
	type batch struct {
		chunks []oasis.Chunk
		index  int
	}
	var batches []batch
	for i := 0; i < len(chunks); i += stride {
		end := min(i+batchSize, len(chunks))
		b := chunks[i:end]
		if len(b) < 2 {
			continue
		}
		batches = append(batches, batch{chunks: b, index: len(batches)})
	}

	if len(batches) == 0 {
		return nil, nil
	}

	// Worker pool.
	numWorkers := min(workers, len(batches))
	work := make(chan batch, len(batches))
	results := make(chan []oasis.ChunkEdge, len(batches))

	for w := 0; w < numWorkers; w++ {
		go func() {
			for b := range work {
				if ctx.Err() != nil {
					results <- nil
					continue
				}

				var prompt strings.Builder
				prompt.WriteString(graphExtractionPrompt)
				for _, c := range b.chunks {
					fmt.Fprintf(&prompt, "\n[%s]: %s\n", c.ID, c.Content)
				}

				resp, err := provider.Chat(ctx, oasis.ChatRequest{
					Messages: []oasis.ChatMessage{
						{Role: "user", Content: prompt.String()},
					},
				})
				if err != nil {
					if logger != nil {
						logger.Warn("graph extraction: LLM call failed", "batch", b.index, "err", err)
					}
					results <- nil
					continue
				}

				edges, err := parseEdgeResponse(resp.Content, b.chunks)
				if err != nil {
					if logger != nil {
						logger.Warn("graph extraction: parse failed", "batch", b.index, "err", err)
					}
					results <- nil
					continue
				}
				results <- edges
			}
		}()
	}

	// Send all batches.
	for _, b := range batches {
		work <- b
	}
	close(work)

	// Collect results.
	var allEdges []oasis.ChunkEdge
	for range batches {
		if edges := <-results; len(edges) > 0 {
			allEdges = append(allEdges, edges...)
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

	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, err
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
