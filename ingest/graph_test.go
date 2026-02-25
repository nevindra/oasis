package ingest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	oasis "github.com/nevindra/oasis"
)

func TestExtractGraphEdges(t *testing.T) {
	chunks := []oasis.Chunk{
		{ID: "c1", Content: "Go is a programming language."},
		{ID: "c2", Content: "Go was created by Google, as mentioned in the introduction."},
		{ID: "c3", Content: "Go supports concurrency via goroutines, building on the concepts above."},
	}

	provider := &mockGraphProvider{
		response: `{"edges":[{"source":"c2","target":"c1","relation":"references","weight":0.9,"description":"mentions Go's creation"},{"source":"c3","target":"c2","relation":"elaborates","weight":0.8,"description":"expands on concurrency details"}]}`,
	}

	edges, err := extractGraphEdges(context.Background(), provider, chunks, 5, 0, 1, nil)
	if err != nil {
		t.Fatalf("extractGraphEdges: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2", len(edges))
	}
	if edges[0].Relation != oasis.RelReferences {
		t.Errorf("edges[0].Relation = %q, want references", edges[0].Relation)
	}
	if edges[0].Description != "mentions Go's creation" {
		t.Errorf("edges[0].Description = %q, want %q", edges[0].Description, "mentions Go's creation")
	}
	if edges[1].Description != "expands on concurrency details" {
		t.Errorf("edges[1].Description = %q, want %q", edges[1].Description, "expands on concurrency details")
	}
}

func TestParseEdgeResponse_NoDescription(t *testing.T) {
	chunks := []oasis.Chunk{
		{ID: "c1", Content: "A"},
		{ID: "c2", Content: "B"},
	}
	edges, err := parseEdgeResponse(`{"edges":[{"source":"c1","target":"c2","relation":"references","weight":0.8}]}`, chunks)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("len = %d, want 1", len(edges))
	}
	if edges[0].Description != "" {
		t.Errorf("Description = %q, want empty", edges[0].Description)
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

func TestExtractGraphEdges_SlidingWindow(t *testing.T) {
	// 7 chunks, batchSize=5, overlap=2 → stride=3
	// Batches: [0,1,2,3,4] [3,4,5,6] (second batch has 4 items, >=2 so valid)
	chunks := make([]oasis.Chunk, 7)
	for i := range chunks {
		chunks[i] = oasis.Chunk{ID: fmt.Sprintf("c%d", i), Content: fmt.Sprintf("Chunk %d content.", i)}
	}

	callCount := 0
	provider := &mockGraphProvider{
		response: `{"edges":[]}`,
		onChat:   func() { callCount++ },
	}

	_, err := extractGraphEdges(context.Background(), provider, chunks, 5, 2, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2 (two overlapping batches)", callCount)
	}
}

func TestDeduplicateEdges(t *testing.T) {
	edges := []oasis.ChunkEdge{
		{ID: "e1", SourceID: "c1", TargetID: "c2", Relation: oasis.RelReferences, Weight: 0.7, Description: "first"},
		{ID: "e2", SourceID: "c1", TargetID: "c2", Relation: oasis.RelReferences, Weight: 0.9, Description: "second"},
		{ID: "e3", SourceID: "c1", TargetID: "c3", Relation: oasis.RelElaborates, Weight: 0.8, Description: "unique"},
	}
	deduped := deduplicateEdges(edges)
	if len(deduped) != 2 {
		t.Fatalf("len = %d, want 2", len(deduped))
	}
	// The c1→c2 references edge should keep weight 0.9 (highest).
	for _, e := range deduped {
		if e.SourceID == "c1" && e.TargetID == "c2" && e.Relation == oasis.RelReferences {
			if e.Weight != 0.9 {
				t.Errorf("Weight = %f, want 0.9", e.Weight)
			}
			if e.Description != "second" {
				t.Errorf("Description = %q, want %q (from highest-weight edge)", e.Description, "second")
			}
		}
	}
}

func TestExtractGraphEdges_Parallel(t *testing.T) {
	// 15 chunks, batchSize=5, overlap=0, workers=3
	// Should produce 3 batches, all processed in parallel.
	chunks := make([]oasis.Chunk, 15)
	for i := range chunks {
		chunks[i] = oasis.Chunk{ID: fmt.Sprintf("c%d", i), Content: fmt.Sprintf("Chunk %d.", i)}
	}

	var mu sync.Mutex
	maxConcurrent := 0
	current := 0

	provider := &mockGraphProvider{
		response: `{"edges":[]}`,
		onChat: func() {
			mu.Lock()
			current++
			if current > maxConcurrent {
				maxConcurrent = current
			}
			mu.Unlock()
			time.Sleep(50 * time.Millisecond) // simulate LLM latency
			mu.Lock()
			current--
			mu.Unlock()
		},
	}

	_, err := extractGraphEdges(context.Background(), provider, chunks, 5, 0, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if maxConcurrent < 2 {
		t.Errorf("maxConcurrent = %d, want >= 2 (should run in parallel)", maxConcurrent)
	}
}

func TestExtractGraphEdges_CancelContext(t *testing.T) {
	chunks := make([]oasis.Chunk, 20)
	for i := range chunks {
		chunks[i] = oasis.Chunk{ID: fmt.Sprintf("c%d", i), Content: fmt.Sprintf("Chunk %d.", i)}
	}

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	callCount := 0
	provider := &mockGraphProvider{
		response: `{"edges":[]}`,
		onChat: func() {
			mu.Lock()
			callCount++
			c := callCount
			mu.Unlock()
			if c >= 2 {
				cancel()
			}
		},
	}

	_, err := extractGraphEdges(ctx, provider, chunks, 5, 0, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	// With cancellation after 2 calls, we should have fewer than 4 calls.
	if callCount >= 4 {
		t.Errorf("callCount = %d, should be < 4 (context was cancelled)", callCount)
	}
}

type mockGraphProvider struct {
	response string
	onChat   func()
}

func (m *mockGraphProvider) Chat(_ context.Context, req oasis.ChatRequest) (oasis.ChatResponse, error) {
	if m.onChat != nil {
		m.onChat()
	}
	return oasis.ChatResponse{Content: m.response}, nil
}

func (m *mockGraphProvider) ChatWithTools(_ context.Context, _ oasis.ChatRequest, _ []oasis.ToolDefinition) (oasis.ChatResponse, error) {
	return oasis.ChatResponse{}, fmt.Errorf("not implemented")
}

func (m *mockGraphProvider) ChatStream(_ context.Context, _ oasis.ChatRequest, _ chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	return oasis.ChatResponse{}, fmt.Errorf("not implemented")
}

func (m *mockGraphProvider) Name() string { return "mock" }
