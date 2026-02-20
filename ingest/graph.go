package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	oasis "github.com/nevindra/oasis"
)

const graphExtractionPrompt = `You are a knowledge graph extractor. Analyze the following text chunks and identify relationships between them.

For each relationship found, output a JSON edge with:
- "source": the chunk ID that holds the relationship
- "target": the chunk ID being referenced
- "relation": one of: references, elaborates, depends_on, contradicts, part_of, similar_to, sequence, caused_by
- "weight": confidence score from 0.0 to 1.0

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
{"edges":[{"source":"chunk_id","target":"chunk_id","relation":"type","weight":0.0}]}

If no relationships exist, output: {"edges":[]}

Chunks:
`

// extractGraphEdges sends chunks to an LLM in batches and extracts relationship edges.
func extractGraphEdges(ctx context.Context, provider oasis.Provider, chunks []oasis.Chunk, batchSize int) ([]oasis.ChunkEdge, error) {
	if len(chunks) < 2 {
		return nil, nil
	}

	var allEdges []oasis.ChunkEdge

	for i := 0; i < len(chunks); i += batchSize {
		end := min(i+batchSize, len(chunks))
		batch := chunks[i:end]

		if len(batch) < 2 {
			continue
		}

		var prompt strings.Builder
		prompt.WriteString(graphExtractionPrompt)
		for _, c := range batch {
			fmt.Fprintf(&prompt, "\n[%s]: %s\n", c.ID, c.Content)
		}

		resp, err := provider.Chat(ctx, oasis.ChatRequest{
			Messages: []oasis.ChatMessage{
				{Role: "user", Content: prompt.String()},
			},
		})
		if err != nil {
			continue // degrade gracefully
		}

		edges, err := parseEdgeResponse(resp.Content, batch)
		if err != nil {
			continue
		}
		allEdges = append(allEdges, edges...)
	}

	return allEdges, nil
}

// parseEdgeResponse parses LLM JSON output into ChunkEdge values.
// Only edges referencing valid chunk IDs from the batch are kept.
func parseEdgeResponse(content string, chunks []oasis.Chunk) ([]oasis.ChunkEdge, error) {
	var parsed struct {
		Edges []struct {
			Source   string  `json:"source"`
			Target   string  `json:"target"`
			Relation string  `json:"relation"`
			Weight   float32 `json:"weight"`
		} `json:"edges"`
	}

	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, err
	}

	validIDs := make(map[string]bool, len(chunks))
	for _, c := range chunks {
		validIDs[c.ID] = true
	}

	validRelations := map[string]oasis.RelationType{
		"references":  oasis.RelReferences,
		"elaborates":  oasis.RelElaborates,
		"depends_on":  oasis.RelDependsOn,
		"contradicts": oasis.RelContradicts,
		"part_of":     oasis.RelPartOf,
		"similar_to":  oasis.RelSimilarTo,
		"sequence":    oasis.RelSequence,
		"caused_by":   oasis.RelCausedBy,
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
			ID:       oasis.NewID(),
			SourceID: e.Source,
			TargetID: e.Target,
			Relation: rel,
			Weight:   e.Weight,
		})
	}

	return edges, nil
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
