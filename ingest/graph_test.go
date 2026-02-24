package ingest

import (
	"context"
	"fmt"
	"testing"

	oasis "github.com/nevindra/oasis"
)

func TestExtractGraphEdges(t *testing.T) {
	chunks := []oasis.Chunk{
		{ID: "c1", Content: "Go is a programming language."},
		{ID: "c2", Content: "Go was created by Google, as mentioned in the introduction."},
		{ID: "c3", Content: "Go supports concurrency via goroutines, building on the concepts above."},
	}

	provider := &mockGraphProvider{
		response: `{"edges":[{"source":"c2","target":"c1","relation":"references","weight":0.9},{"source":"c3","target":"c2","relation":"elaborates","weight":0.8}]}`,
	}

	edges, err := extractGraphEdges(context.Background(), provider, chunks, 5, nil)
	if err != nil {
		t.Fatalf("extractGraphEdges: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2", len(edges))
	}
	if edges[0].Relation != oasis.RelReferences {
		t.Errorf("edges[0].Relation = %q, want references", edges[0].Relation)
	}
}

func TestPruneEdges(t *testing.T) {
	edges := []oasis.ChunkEdge{
		{ID: "e1", SourceID: "c1", TargetID: "c2", Relation: oasis.RelReferences, Weight: 0.9},
		{ID: "e2", SourceID: "c1", TargetID: "c3", Relation: oasis.RelElaborates, Weight: 0.2},
		{ID: "e3", SourceID: "c1", TargetID: "c4", Relation: oasis.RelSequence, Weight: 0.5},
		{ID: "e4", SourceID: "c2", TargetID: "c3", Relation: oasis.RelDependsOn, Weight: 0.8},
	}

	// Prune by min weight 0.3 and max 2 edges per chunk.
	pruned := pruneEdges(edges, 0.3, 2)

	// e2 should be dropped (weight 0.2 < 0.3).
	// From c1: e1 (0.9) and e3 (0.5) kept, not e2 (dropped by weight).
	// From c2: e4 (0.8) kept.
	if len(pruned) != 3 {
		t.Fatalf("got %d edges, want 3", len(pruned))
	}
}

func TestBuildSequenceEdges(t *testing.T) {
	chunks := []oasis.Chunk{
		{ID: "c1", DocumentID: "d1", ChunkIndex: 0, Content: "First chunk"},
		{ID: "c2", DocumentID: "d1", ChunkIndex: 1, Content: "Second chunk"},
		{ID: "c3", DocumentID: "d1", ChunkIndex: 2, Content: "Third chunk"},
	}

	edges := buildSequenceEdges(chunks)
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2", len(edges))
	}

	// c1 → c2
	if edges[0].SourceID != "c1" || edges[0].TargetID != "c2" {
		t.Errorf("edge[0]: got %s→%s, want c1→c2", edges[0].SourceID, edges[0].TargetID)
	}
	if edges[0].Relation != oasis.RelSequence {
		t.Errorf("edge[0].Relation = %q, want sequence", edges[0].Relation)
	}
	if edges[0].Weight != 1.0 {
		t.Errorf("edge[0].Weight = %f, want 1.0", edges[0].Weight)
	}

	// c2 → c3
	if edges[1].SourceID != "c2" || edges[1].TargetID != "c3" {
		t.Errorf("edge[1]: got %s→%s, want c2→c3", edges[1].SourceID, edges[1].TargetID)
	}
}

func TestBuildSequenceEdges_UnsortedInput(t *testing.T) {
	chunks := []oasis.Chunk{
		{ID: "c3", DocumentID: "d1", ChunkIndex: 2, Content: "Third"},
		{ID: "c1", DocumentID: "d1", ChunkIndex: 0, Content: "First"},
		{ID: "c2", DocumentID: "d1", ChunkIndex: 1, Content: "Second"},
	}

	edges := buildSequenceEdges(chunks)
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2", len(edges))
	}
	// Should still be c1→c2, c2→c3 after sorting by ChunkIndex.
	if edges[0].SourceID != "c1" || edges[0].TargetID != "c2" {
		t.Errorf("edge[0]: got %s→%s, want c1→c2", edges[0].SourceID, edges[0].TargetID)
	}
	if edges[1].SourceID != "c2" || edges[1].TargetID != "c3" {
		t.Errorf("edge[1]: got %s→%s, want c2→c3", edges[1].SourceID, edges[1].TargetID)
	}
}

func TestBuildSequenceEdges_SingleChunk(t *testing.T) {
	chunks := []oasis.Chunk{
		{ID: "c1", DocumentID: "d1", ChunkIndex: 0, Content: "Only chunk"},
	}
	edges := buildSequenceEdges(chunks)
	if len(edges) != 0 {
		t.Fatalf("got %d edges, want 0", len(edges))
	}
}

type mockGraphProvider struct {
	response string
}

func (m *mockGraphProvider) Chat(_ context.Context, req oasis.ChatRequest) (oasis.ChatResponse, error) {
	return oasis.ChatResponse{Content: m.response}, nil
}

func (m *mockGraphProvider) ChatWithTools(_ context.Context, _ oasis.ChatRequest, _ []oasis.ToolDefinition) (oasis.ChatResponse, error) {
	return oasis.ChatResponse{}, fmt.Errorf("not implemented")
}

func (m *mockGraphProvider) ChatStream(_ context.Context, _ oasis.ChatRequest, _ chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	return oasis.ChatResponse{}, fmt.Errorf("not implemented")
}

func (m *mockGraphProvider) Name() string { return "mock" }
