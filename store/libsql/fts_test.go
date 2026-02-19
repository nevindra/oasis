package libsql

import (
	"context"
	"testing"

	"github.com/nevindra/oasis"
)

func TestSearchChunksKeyword(t *testing.T) {
	path := t.TempDir() + "/test.db"
	s := New(path)
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer s.Close()

	doc := oasis.Document{ID: "doc1", Title: "Test", Source: "test", CreatedAt: 1}
	chunks := []oasis.Chunk{
		{ID: "c1", DocumentID: "doc1", Content: "golang concurrency patterns", ChunkIndex: 0},
		{ID: "c2", DocumentID: "doc1", Content: "python machine learning basics", ChunkIndex: 1},
		{ID: "c3", DocumentID: "doc1", Content: "golang error handling best practices", ChunkIndex: 2},
	}
	if err := s.StoreDocument(ctx, doc, chunks); err != nil {
		t.Fatalf("StoreDocument() error = %v", err)
	}

	results, err := s.SearchChunksKeyword(ctx, "golang", 10)
	if err != nil {
		t.Fatalf("SearchChunksKeyword() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
	for _, r := range results {
		if r.Chunk.ID != "c1" && r.Chunk.ID != "c3" {
			t.Errorf("unexpected chunk ID %q", r.Chunk.ID)
		}
	}
}

func TestSearchChunksKeyword_NoResults(t *testing.T) {
	path := t.TempDir() + "/test.db"
	s := New(path)
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer s.Close()

	results, err := s.SearchChunksKeyword(ctx, "nonexistent", 10)
	if err != nil {
		t.Fatalf("SearchChunksKeyword() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len = %d, want 0", len(results))
	}
}

func TestKeywordSearcherInterface(t *testing.T) {
	var s interface{} = &Store{}
	if _, ok := s.(oasis.KeywordSearcher); !ok {
		t.Fatal("Store does not implement KeywordSearcher")
	}
}
