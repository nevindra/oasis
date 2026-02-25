package ingest

import (
	"context"
	"fmt"
	"testing"

	oasis "github.com/nevindra/oasis"
)

func TestExtractCrossDocumentEdges(t *testing.T) {
	store := &mockCrossDocStore{
		documents: []oasis.Document{
			{ID: "d1", Title: "OAuth Setup"},
			{ID: "d2", Title: "OAuth Troubleshooting"},
		},
		chunksByDoc: map[string][]oasis.Chunk{
			"d1": {{ID: "c1", DocumentID: "d1", Content: "OAuth setup flow", Embedding: []float32{0.9, 0.1}}},
			"d2": {{ID: "c2", DocumentID: "d2", Content: "OAuth error debugging", Embedding: []float32{0.8, 0.2}}},
		},
	}

	provider := &mockGraphProvider{
		response: `{"edges":[{"source":"c1","target":"c2","relation":"similar_to","weight":0.8,"description":"both cover OAuth"}]}`,
	}

	emb := &mockEmbeddingProvider{embedding: []float32{0.5, 0.5}}
	ing := NewIngestor(store, emb, WithGraphExtraction(provider))

	count, err := ing.ExtractCrossDocumentEdges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Error("expected edges to be created")
	}
	if len(store.storedEdges) == 0 {
		t.Error("no edges stored")
	}
}

func TestExtractCrossDocumentEdges_NoProvider(t *testing.T) {
	store := &mockCrossDocStore{}
	emb := &mockEmbeddingProvider{embedding: []float32{0.1}}
	ing := NewIngestor(store, emb) // no WithGraphExtraction

	_, err := ing.ExtractCrossDocumentEdges(context.Background())
	if err == nil {
		t.Error("expected error when no graph provider configured")
	}
}

// --- Mock helpers ---

type mockCrossDocStore struct {
	oasis.Store
	documents   []oasis.Document
	chunksByDoc map[string][]oasis.Chunk
	storedEdges []oasis.ChunkEdge
}

func (s *mockCrossDocStore) ListDocuments(_ context.Context, _ int) ([]oasis.Document, error) {
	return s.documents, nil
}

func (s *mockCrossDocStore) GetChunksByDocument(_ context.Context, docID string) ([]oasis.Chunk, error) {
	return s.chunksByDoc[docID], nil
}

func (s *mockCrossDocStore) SearchChunks(_ context.Context, _ []float32, topK int, filters ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	// Return chunks from other documents based on exclude filter.
	for _, f := range filters {
		if f.Op == oasis.OpNeq && f.Field == "document_id" {
			excludeDoc, _ := f.Value.(string)
			for docID, chunks := range s.chunksByDoc {
				if docID == excludeDoc {
					continue
				}
				var results []oasis.ScoredChunk
				for _, c := range chunks {
					results = append(results, oasis.ScoredChunk{Chunk: c, Score: 0.7})
				}
				return results, nil
			}
		}
	}
	return nil, nil
}

func (s *mockCrossDocStore) StoreEdges(_ context.Context, edges []oasis.ChunkEdge) error {
	s.storedEdges = append(s.storedEdges, edges...)
	return nil
}

func (s *mockCrossDocStore) GetEdges(_ context.Context, _ []string) ([]oasis.ChunkEdge, error) {
	return nil, nil
}

func (s *mockCrossDocStore) GetIncomingEdges(_ context.Context, _ []string) ([]oasis.ChunkEdge, error) {
	return nil, nil
}

func (s *mockCrossDocStore) PruneOrphanEdges(_ context.Context) (int, error) { return 0, nil }

// Stub remaining Store interface methods needed for compilation.
func (s *mockCrossDocStore) StoreDocument(_ context.Context, _ oasis.Document, _ []oasis.Chunk) error {
	return nil
}
func (s *mockCrossDocStore) DeleteDocument(_ context.Context, _ string) error { return nil }
func (s *mockCrossDocStore) GetChunksByIDs(_ context.Context, _ []string) ([]oasis.Chunk, error) {
	return nil, nil
}

type mockEmbeddingProvider struct {
	embedding []float32
}

func (m *mockEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = m.embedding
	}
	return out, nil
}

func (m *mockEmbeddingProvider) Dimensions() int { return len(m.embedding) }
func (m *mockEmbeddingProvider) Name() string    { return "mock" }

// Ensure mockCrossDocStore implements DocumentChunkLister.
var _ DocumentChunkLister = (*mockCrossDocStore)(nil)

// Ensure it also satisfies GraphStore via type assertion in the implementation.
var _ oasis.GraphStore = (*mockCrossDocStore)(nil)

// noopLogger suppresses log output in tests.
var _ = fmt.Sprintf // suppress unused import
