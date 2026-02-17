package sqlite

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/nevindra/oasis"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s := New(filepath.Join(t.TempDir(), "test.db"))
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return s
}

func TestInitIdempotent(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "init.db"))
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := s.Init(ctx); err != nil {
		t.Fatalf("second Init: %v", err)
	}
}

func TestStoreAndGetMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	conv, _ := s.GetOrCreateConversation(ctx, "chat-1")

	msgs := []oasis.Message{
		{ID: oasis.NewID(), ConversationID: conv.ID, Role: "user", Content: "Hello", CreatedAt: 1000},
		{ID: oasis.NewID(), ConversationID: conv.ID, Role: "assistant", Content: "Hi!", CreatedAt: 1001},
		{ID: oasis.NewID(), ConversationID: conv.ID, Role: "user", Content: "Bye", CreatedAt: 1002},
	}
	for _, m := range msgs {
		if err := s.StoreMessage(ctx, m); err != nil {
			t.Fatalf("StoreMessage: %v", err)
		}
	}

	got, err := s.GetMessages(ctx, conv.ID, 10)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	if got[0].Content != "Hello" || got[2].Content != "Bye" {
		t.Error("messages not in chronological order")
	}

	// Test limit returns most recent
	got2, _ := s.GetMessages(ctx, conv.ID, 2)
	if len(got2) != 2 || got2[0].Content != "Hi!" {
		t.Errorf("limit 2: expected [Hi!, Bye], got %v", got2)
	}
}

func TestGetOrCreateConversationIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	c1, _ := s.GetOrCreateConversation(ctx, "chat-abc")
	c2, _ := s.GetOrCreateConversation(ctx, "chat-abc")
	if c1.ID != c2.ID {
		t.Errorf("not idempotent: %q vs %q", c1.ID, c2.ID)
	}
}

func TestConfig(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	val, _ := s.GetConfig(ctx, "missing")
	if val != "" {
		t.Errorf("missing key should return empty, got %q", val)
	}

	s.SetConfig(ctx, "k", "v1")
	val, _ = s.GetConfig(ctx, "k")
	if val != "v1" {
		t.Errorf("expected v1, got %q", val)
	}

	s.SetConfig(ctx, "k", "v2")
	val, _ = s.GetConfig(ctx, "k")
	if val != "v2" {
		t.Errorf("expected v2, got %q", val)
	}
}

func TestStoreDocument(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	doc := oasis.Document{
		ID: oasis.NewID(), Title: "Test", Source: "test",
		Content: "full content", CreatedAt: oasis.NowUnix(),
	}
	chunks := []oasis.Chunk{
		{ID: oasis.NewID(), DocumentID: doc.ID, Content: "chunk 1", ChunkIndex: 0},
		{ID: oasis.NewID(), DocumentID: doc.ID, Content: "chunk 2", ChunkIndex: 1},
	}

	if err := s.StoreDocument(ctx, doc, chunks); err != nil {
		t.Fatalf("StoreDocument: %v", err)
	}

	// Verify via raw query
	db, _ := s.openDB()
	defer db.Close()
	var count int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chunks WHERE document_id = ?", doc.ID).Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 chunks, got %d", count)
	}
}

func TestSearchMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	conv, _ := s.GetOrCreateConversation(ctx, "chat-vec")

	// Store messages with embeddings
	msgs := []oasis.Message{
		{ID: oasis.NewID(), ConversationID: conv.ID, Role: "user", Content: "about cats", Embedding: []float32{1, 0, 0}, CreatedAt: 1},
		{ID: oasis.NewID(), ConversationID: conv.ID, Role: "user", Content: "about dogs", Embedding: []float32{0, 1, 0}, CreatedAt: 2},
		{ID: oasis.NewID(), ConversationID: conv.ID, Role: "user", Content: "about birds", Embedding: []float32{0, 0, 1}, CreatedAt: 3},
	}
	for _, m := range msgs {
		s.StoreMessage(ctx, m)
	}

	// Search for cats-like vector
	results, err := s.SearchMessages(ctx, []float32{0.9, 0.1, 0}, 2)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Content != "about cats" {
		t.Errorf("top result should be 'about cats', got %q", results[0].Content)
	}
}

func TestSearchChunks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	doc := oasis.Document{ID: oasis.NewID(), Title: "Test", Source: "t", Content: "c", CreatedAt: 1}
	chunks := []oasis.Chunk{
		{ID: oasis.NewID(), DocumentID: doc.ID, Content: "rust", ChunkIndex: 0, Embedding: []float32{1, 0}},
		{ID: oasis.NewID(), DocumentID: doc.ID, Content: "go", ChunkIndex: 1, Embedding: []float32{0, 1}},
	}
	s.StoreDocument(ctx, doc, chunks)

	results, err := s.SearchChunks(ctx, []float32{0.8, 0.2}, 1)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(results) != 1 || results[0].Content != "rust" {
		t.Errorf("expected top result 'rust', got %v", results)
	}
}

func TestCosineSimilarity(t *testing.T) {
	// Identical vectors = 1.0
	s := cosineSimilarity([]float32{1, 2, 3}, []float32{1, 2, 3})
	if math.Abs(float64(s)-1.0) > 1e-6 {
		t.Errorf("identical vectors: expected ~1.0, got %f", s)
	}

	// Orthogonal vectors = 0.0
	s = cosineSimilarity([]float32{1, 0}, []float32{0, 1})
	if math.Abs(float64(s)) > 1e-6 {
		t.Errorf("orthogonal vectors: expected ~0.0, got %f", s)
	}

	// Opposite vectors = -1.0
	s = cosineSimilarity([]float32{1, 0}, []float32{-1, 0})
	if math.Abs(float64(s)+1.0) > 1e-6 {
		t.Errorf("opposite vectors: expected ~-1.0, got %f", s)
	}
}
