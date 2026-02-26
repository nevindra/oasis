package sqlite

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
		{ID: "c1", DocumentID: "doc1", Content: "golang concurrency patterns", ChunkIndex: 0, Embedding: []float32{0.1, 0.2}},
		{ID: "c2", DocumentID: "doc1", Content: "python machine learning basics", ChunkIndex: 1, Embedding: []float32{0.3, 0.4}},
		{ID: "c3", DocumentID: "doc1", Content: "golang error handling best practices", ChunkIndex: 2, Embedding: []float32{0.5, 0.6}},
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
		if r.Score <= 0 {
			t.Errorf("expected positive score, got %v", r.Score)
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

func TestSanitizeFTS5Query(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello world", "hello world"},
		{`C++`, "C"},
		{`"exact phrase"`, "exact phrase"},
		{"go-lang", "go lang"},
		{"foo*bar", "foo bar"},
		{"(nested) query", "nested  query"},
		{"  ", ""},
		{"normal query here", "normal query here"},
	}
	for _, tt := range tests {
		got := sanitizeFTS5Query(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeFTS5Query(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSearchChunksKeyword_SpecialChars(t *testing.T) {
	path := t.TempDir() + "/test.db"
	s := New(path)
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer s.Close()

	doc := oasis.Document{ID: "doc1", Title: "Test", Source: "test", CreatedAt: 1}
	chunks := []oasis.Chunk{
		{ID: "c1", DocumentID: "doc1", Content: "C++ programming guide", ChunkIndex: 0, Embedding: []float32{0.1, 0.2}},
	}
	if err := s.StoreDocument(ctx, doc, chunks); err != nil {
		t.Fatalf("StoreDocument() error = %v", err)
	}

	// Should not crash on FTS5 special chars.
	_, err := s.SearchChunksKeyword(ctx, "C++", 10)
	if err != nil {
		t.Fatalf("SearchChunksKeyword(C++) should not error, got: %v", err)
	}

	// Query that sanitizes to empty should return nil, not error.
	results, err := s.SearchChunksKeyword(ctx, "+++", 10)
	if err != nil {
		t.Fatalf("SearchChunksKeyword(+++) should not error, got: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("empty sanitized query should return 0 results, got %d", len(results))
	}
}
