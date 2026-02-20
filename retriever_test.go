package oasis

import (
	"context"
	"testing"
)

func TestScoreReranker(t *testing.T) {
	tests := []struct {
		name     string
		minScore float32
		input    []RetrievalResult
		topK     int
		wantLen  int
		wantIDs  []string
	}{
		{
			name:     "filters below min score",
			minScore: 0.5,
			input: []RetrievalResult{
				{ChunkID: "a", Score: 0.9},
				{ChunkID: "b", Score: 0.3},
				{ChunkID: "c", Score: 0.7},
			},
			topK:    10,
			wantLen: 2,
			wantIDs: []string{"a", "c"},
		},
		{
			name:     "respects topK",
			minScore: 0,
			input: []RetrievalResult{
				{ChunkID: "a", Score: 0.9},
				{ChunkID: "b", Score: 0.8},
				{ChunkID: "c", Score: 0.7},
			},
			topK:    2,
			wantLen: 2,
			wantIDs: []string{"a", "b"},
		},
		{
			name:     "sorts by score descending",
			minScore: 0,
			input: []RetrievalResult{
				{ChunkID: "c", Score: 0.3},
				{ChunkID: "a", Score: 0.9},
				{ChunkID: "b", Score: 0.6},
			},
			topK:    10,
			wantLen: 3,
			wantIDs: []string{"a", "b", "c"},
		},
		{
			name:     "empty input",
			minScore: 0,
			input:    nil,
			topK:     5,
			wantLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewScoreReranker(tt.minScore)
			got, err := r.Rerank(context.Background(), "query", tt.input, tt.topK)
			if err != nil {
				t.Fatalf("Rerank() error = %v", err)
			}
			if len(got) != tt.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tt.wantLen)
			}
			for i, id := range tt.wantIDs {
				if got[i].ChunkID != id {
					t.Errorf("got[%d].ChunkID = %q, want %q", i, got[i].ChunkID, id)
				}
			}
		})
	}
}

func TestReciprocalRankFusion(t *testing.T) {
	tests := []struct {
		name          string
		vector        []ScoredChunk
		keyword       []ScoredChunk
		keywordWeight float32
		wantFirst     string
		wantLen       int
	}{
		{
			name: "vector only when no keyword results",
			vector: []ScoredChunk{
				{Chunk: Chunk{ID: "a", Content: "alpha"}, Score: 0.9},
				{Chunk: Chunk{ID: "b", Content: "beta"}, Score: 0.8},
			},
			keyword:       nil,
			keywordWeight: 0.3,
			wantFirst:     "a",
			wantLen:       2,
		},
		{
			name: "boosts chunk appearing in both lists",
			vector: []ScoredChunk{
				{Chunk: Chunk{ID: "a"}, Score: 0.9},
				{Chunk: Chunk{ID: "b"}, Score: 0.8},
			},
			keyword: []ScoredChunk{
				{Chunk: Chunk{ID: "b"}, Score: 0.9},
				{Chunk: Chunk{ID: "c"}, Score: 0.8},
			},
			keywordWeight: 0.5,
			wantFirst:     "b",
			wantLen:       3,
		},
		{
			name:          "empty inputs",
			vector:        nil,
			keyword:       nil,
			keywordWeight: 0.3,
			wantLen:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reciprocalRankFusion(tt.vector, tt.keyword, tt.keywordWeight)
			if len(got) != tt.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tt.wantLen)
			}
			if tt.wantLen > 0 && got[0].ChunkID != tt.wantFirst {
				t.Errorf("got[0].ChunkID = %q, want %q", got[0].ChunkID, tt.wantFirst)
			}
		})
	}
}

// --- Mock helpers for HybridRetriever tests ---

type mockEmbeddingProvider struct {
	embedding []float32
	err       error
}

func (m *mockEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = m.embedding
	}
	return out, nil
}

func (m *mockEmbeddingProvider) Dimensions() int { return len(m.embedding) }
func (m *mockEmbeddingProvider) Name() string    { return "mock" }

type retrieverStore struct {
	nopStore
	chunks   []ScoredChunk
	parents  []Chunk
	keywords []ScoredChunk
}

func (s *retrieverStore) SearchChunks(_ context.Context, _ []float32, _ int, _ ...ChunkFilter) ([]ScoredChunk, error) {
	return s.chunks, nil
}

func (s *retrieverStore) GetChunksByIDs(_ context.Context, ids []string) ([]Chunk, error) {
	idSet := make(map[string]bool)
	for _, id := range ids {
		idSet[id] = true
	}
	var out []Chunk
	for _, c := range s.parents {
		if idSet[c.ID] {
			out = append(out, c)
		}
	}
	// Also return non-parent chunks that match
	for _, sc := range s.chunks {
		if idSet[sc.ID] {
			out = append(out, sc.Chunk)
		}
	}
	return out, nil
}

func (s *retrieverStore) SearchChunksKeyword(_ context.Context, _ string, _ int, _ ...ChunkFilter) ([]ScoredChunk, error) {
	return s.keywords, nil
}

type mockReranker struct {
	called bool
}

func (m *mockReranker) Rerank(_ context.Context, _ string, results []RetrievalResult, topK int) ([]RetrievalResult, error) {
	m.called = true
	reversed := make([]RetrievalResult, len(results))
	for i, r := range results {
		reversed[len(results)-1-i] = r
	}
	if len(reversed) > topK {
		reversed = reversed[:topK]
	}
	return reversed, nil
}

func TestHybridRetriever_VectorOnly(t *testing.T) {
	store := &retrieverStore{
		chunks: []ScoredChunk{
			{Chunk: Chunk{ID: "c1", Content: "hello world"}, Score: 0.9},
			{Chunk: Chunk{ID: "c2", Content: "goodbye world"}, Score: 0.8},
		},
	}
	emb := &mockEmbeddingProvider{embedding: []float32{0.1, 0.2}}

	r := NewHybridRetriever(store, emb)
	results, err := r.Retrieve(context.Background(), "hello", 5)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
	if results[0].ChunkID != "c1" {
		t.Errorf("results[0].ChunkID = %q, want %q", results[0].ChunkID, "c1")
	}
}

func TestHybridRetriever_ParentChildResolution(t *testing.T) {
	store := &retrieverStore{
		chunks: []ScoredChunk{
			{Chunk: Chunk{ID: "child1", DocumentID: "doc1", ParentID: "parent1", Content: "small chunk"}, Score: 0.9},
			{Chunk: Chunk{ID: "child2", DocumentID: "doc1", ParentID: "parent1", Content: "another small chunk"}, Score: 0.7},
			{Chunk: Chunk{ID: "c3", DocumentID: "doc1", Content: "no parent"}, Score: 0.8},
		},
		parents: []Chunk{
			{ID: "parent1", DocumentID: "doc1", Content: "big parent context with much more detail"},
		},
	}
	emb := &mockEmbeddingProvider{embedding: []float32{0.1, 0.2}}

	r := NewHybridRetriever(store, emb)
	results, err := r.Retrieve(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("len = %d, want 2 (parent dedup + standalone)", len(results))
	}

	foundParent := false
	for _, r := range results {
		if r.Content == "big parent context with much more detail" {
			foundParent = true
		}
	}
	if !foundParent {
		t.Error("parent content not found in results")
	}
}

func TestHybridRetriever_WithReranker(t *testing.T) {
	store := &retrieverStore{
		chunks: []ScoredChunk{
			{Chunk: Chunk{ID: "c1", Content: "first"}, Score: 0.9},
			{Chunk: Chunk{ID: "c2", Content: "second"}, Score: 0.8},
		},
	}
	emb := &mockEmbeddingProvider{embedding: []float32{0.1}}
	rr := &mockReranker{}

	r := NewHybridRetriever(store, emb, WithReranker(rr))
	results, err := r.Retrieve(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if !rr.called {
		t.Error("reranker was not called")
	}
	if len(results) >= 2 && results[0].ChunkID != "c2" {
		t.Errorf("results[0].ChunkID = %q, want %q (reranker should reverse)", results[0].ChunkID, "c2")
	}
}

func TestHybridRetriever_HybridSearch(t *testing.T) {
	store := &retrieverStore{
		chunks: []ScoredChunk{
			{Chunk: Chunk{ID: "c1", Content: "vector match"}, Score: 0.9},
		},
		keywords: []ScoredChunk{
			{Chunk: Chunk{ID: "c1", Content: "vector match"}, Score: 0.8},
			{Chunk: Chunk{ID: "c2", Content: "keyword only"}, Score: 0.7},
		},
	}
	emb := &mockEmbeddingProvider{embedding: []float32{0.1}}

	r := NewHybridRetriever(store, emb)
	results, err := r.Retrieve(context.Background(), "test", 5)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
	if results[0].ChunkID != "c1" {
		t.Errorf("results[0].ChunkID = %q, want %q (hybrid boost)", results[0].ChunkID, "c1")
	}
}

// --- LLMReranker tests ---

func TestLLMReranker(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			{Content: `{"scores":[{"index":0,"score":3},{"index":1,"score":9},{"index":2,"score":6}]}`},
		},
	}

	r := NewLLMReranker(provider)
	input := []RetrievalResult{
		{ChunkID: "a", Content: "first", Score: 0.5},
		{ChunkID: "b", Content: "second", Score: 0.5},
		{ChunkID: "c", Content: "third", Score: 0.5},
	}

	got, err := r.Rerank(context.Background(), "test query", input, 2)
	if err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ChunkID != "b" {
		t.Errorf("got[0].ChunkID = %q, want %q", got[0].ChunkID, "b")
	}
	if got[1].ChunkID != "c" {
		t.Errorf("got[1].ChunkID = %q, want %q", got[1].ChunkID, "c")
	}
}

// --- GraphRetriever tests ---

func TestGraphRetriever_VectorOnlyFallback(t *testing.T) {
	// Store that does NOT implement GraphStore — should fall back to vector-only.
	store := &graphTestStore{
		chunks: []ScoredChunk{
			{Chunk: Chunk{ID: "c1", DocumentID: "d1", Content: "chunk one"}, Score: 0.9},
			{Chunk: Chunk{ID: "c2", DocumentID: "d1", Content: "chunk two"}, Score: 0.7},
		},
	}
	emb := &graphTestEmbedding{}

	gr := NewGraphRetriever(store, emb)
	results, err := gr.Retrieve(context.Background(), "test query", 5)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
	if results[0].ChunkID != "c1" {
		t.Errorf("results[0].ChunkID = %q, want c1", results[0].ChunkID)
	}
}

func TestGraphRetriever_WithGraphTraversal(t *testing.T) {
	store := &graphTestStoreWithEdges{
		graphTestStore: graphTestStore{
			chunks: []ScoredChunk{
				{Chunk: Chunk{ID: "c1", DocumentID: "d1", Content: "seed chunk"}, Score: 0.9},
			},
			allChunks: map[string]Chunk{
				"c1": {ID: "c1", DocumentID: "d1", Content: "seed chunk"},
				"c2": {ID: "c2", DocumentID: "d1", Content: "related chunk"},
				"c3": {ID: "c3", DocumentID: "d1", Content: "distant chunk"},
			},
		},
		edges: map[string][]ChunkEdge{
			"c1": {{ID: "e1", SourceID: "c1", TargetID: "c2", Relation: RelReferences, Weight: 0.8}},
			"c2": {{ID: "e2", SourceID: "c2", TargetID: "c3", Relation: RelElaborates, Weight: 0.6}},
		},
	}
	emb := &graphTestEmbedding{}

	gr := NewGraphRetriever(store, emb, WithMaxHops(2), WithGraphWeight(0.3))
	results, err := gr.Retrieve(context.Background(), "test", 10)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}

	// Should have 3 results: c1 (seed), c2 (hop 1), c3 (hop 2)
	if len(results) != 3 {
		t.Fatalf("len = %d, want 3", len(results))
	}

	// c1 should score highest (seed with high vector score)
	if results[0].ChunkID != "c1" {
		t.Errorf("results[0].ChunkID = %q, want c1", results[0].ChunkID)
	}

	// c2 should score higher than c3 (closer hop)
	var c2Score, c3Score float32
	for _, r := range results {
		if r.ChunkID == "c2" {
			c2Score = r.Score
		}
		if r.ChunkID == "c3" {
			c3Score = r.Score
		}
	}
	if c2Score <= c3Score {
		t.Errorf("c2 score (%f) should be > c3 score (%f)", c2Score, c3Score)
	}
}

func TestGraphRetriever_Bidirectional(t *testing.T) {
	store := &graphTestStoreWithEdges{
		graphTestStore: graphTestStore{
			chunks: []ScoredChunk{
				{Chunk: Chunk{ID: "c2", DocumentID: "d1", Content: "seed"}, Score: 0.9},
			},
			allChunks: map[string]Chunk{
				"c1": {ID: "c1", DocumentID: "d1", Content: "references c2"},
				"c2": {ID: "c2", DocumentID: "d1", Content: "seed"},
			},
		},
		edges:         map[string][]ChunkEdge{},
		incomingEdges: map[string][]ChunkEdge{
			"c2": {{ID: "e1", SourceID: "c1", TargetID: "c2", Relation: RelReferences, Weight: 0.8}},
		},
	}
	emb := &graphTestEmbedding{}

	// Without bidirectional: only seed (no outgoing edges from c2)
	gr := NewGraphRetriever(store, emb, WithMaxHops(1))
	results, _ := gr.Retrieve(context.Background(), "test", 10)
	if len(results) != 1 {
		t.Fatalf("non-bidirectional: len = %d, want 1", len(results))
	}

	// With bidirectional: seed + c1 (via incoming edge)
	gr = NewGraphRetriever(store, emb, WithMaxHops(1), WithBidirectional(true))
	results, _ = gr.Retrieve(context.Background(), "test", 10)
	if len(results) != 2 {
		t.Fatalf("bidirectional: len = %d, want 2", len(results))
	}
}

func TestGraphRetriever_RelationFilter(t *testing.T) {
	store := &graphTestStoreWithEdges{
		graphTestStore: graphTestStore{
			chunks: []ScoredChunk{
				{Chunk: Chunk{ID: "c1", DocumentID: "d1", Content: "seed"}, Score: 0.9},
			},
			allChunks: map[string]Chunk{
				"c1": {ID: "c1", DocumentID: "d1", Content: "seed"},
				"c2": {ID: "c2", DocumentID: "d1", Content: "referenced"},
				"c3": {ID: "c3", DocumentID: "d1", Content: "contradicts"},
			},
		},
		edges: map[string][]ChunkEdge{
			"c1": {
				{ID: "e1", SourceID: "c1", TargetID: "c2", Relation: RelReferences, Weight: 0.8},
				{ID: "e2", SourceID: "c1", TargetID: "c3", Relation: RelContradicts, Weight: 0.9},
			},
		},
	}
	emb := &graphTestEmbedding{}

	// Filter to only follow "references" edges
	gr := NewGraphRetriever(store, emb, WithMaxHops(1), WithRelationFilter(RelReferences))
	results, _ := gr.Retrieve(context.Background(), "test", 10)

	// Should have 2: seed + c2 (references), but NOT c3 (contradicts)
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
	for _, r := range results {
		if r.ChunkID == "c3" {
			t.Error("c3 should be filtered out (contradicts relation)")
		}
	}
}

// --- GraphRetriever test helpers ---

type graphTestStore struct {
	nopStore
	chunks    []ScoredChunk
	allChunks map[string]Chunk
}

func (s *graphTestStore) SearchChunks(_ context.Context, _ []float32, topK int, _ ...ChunkFilter) ([]ScoredChunk, error) {
	if len(s.chunks) > topK {
		return s.chunks[:topK], nil
	}
	return s.chunks, nil
}

func (s *graphTestStore) GetChunksByIDs(_ context.Context, ids []string) ([]Chunk, error) {
	if s.allChunks == nil {
		return nil, nil
	}
	var result []Chunk
	for _, id := range ids {
		if c, ok := s.allChunks[id]; ok {
			result = append(result, c)
		}
	}
	return result, nil
}

type graphTestEmbedding struct{}

func (e *graphTestEmbedding) Embed(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i := range texts {
		result[i] = []float32{0.1, 0.2, 0.3}
	}
	return result, nil
}

func (e *graphTestEmbedding) Dimensions() int { return 3 }
func (e *graphTestEmbedding) Name() string    { return "graph-test" }

type graphTestStoreWithEdges struct {
	graphTestStore
	edges         map[string][]ChunkEdge
	incomingEdges map[string][]ChunkEdge
}

func (s *graphTestStoreWithEdges) StoreEdges(_ context.Context, edges []ChunkEdge) error {
	for _, e := range edges {
		s.edges[e.SourceID] = append(s.edges[e.SourceID], e)
	}
	return nil
}

func (s *graphTestStoreWithEdges) GetEdges(_ context.Context, chunkIDs []string) ([]ChunkEdge, error) {
	var result []ChunkEdge
	for _, id := range chunkIDs {
		result = append(result, s.edges[id]...)
	}
	return result, nil
}

func (s *graphTestStoreWithEdges) GetIncomingEdges(_ context.Context, chunkIDs []string) ([]ChunkEdge, error) {
	var result []ChunkEdge
	for _, id := range chunkIDs {
		result = append(result, s.incomingEdges[id]...)
	}
	return result, nil
}

func (s *graphTestStoreWithEdges) PruneOrphanEdges(_ context.Context) (int, error) { return 0, nil }

func TestLLMReranker_GracefulDegradation(t *testing.T) {
	provider := &mockProvider{
		responses: []ChatResponse{
			{Content: "not valid json"},
		},
	}
	r := NewLLMReranker(provider)
	input := []RetrievalResult{
		{ChunkID: "a", Score: 0.5},
		{ChunkID: "b", Score: 0.3},
	}

	got, err := r.Rerank(context.Background(), "test", input, 5)
	if err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("should return original results on parse failure, got %d", len(got))
	}
}
