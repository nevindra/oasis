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

func (s *retrieverStore) SearchChunks(_ context.Context, _ []float32, _ int) ([]ScoredChunk, error) {
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

func (s *retrieverStore) SearchChunksKeyword(_ context.Context, _ string, _ int) ([]ScoredChunk, error) {
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
