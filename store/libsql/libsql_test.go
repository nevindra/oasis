package libsql

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nevindra/oasis"
)

// testStore creates a Store backed by a temporary SQLite file and
// calls Init. The database file is cleaned up when the test finishes.
func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s := New(dbPath)
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return s
}

func TestInitCreatesTables(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "init.db")
	s := New(dbPath)

	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Verify the database file was created.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("database file was not created")
	}

	// Calling Init again should be idempotent (IF NOT EXISTS).
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("second Init failed: %v", err)
	}
}

func TestStoreMessageAndGetMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Create a conversation first.
	conv, err := s.GetOrCreateConversation(ctx, "chat-123")
	if err != nil {
		t.Fatalf("GetOrCreateConversation: %v", err)
	}

	// Store messages.
	msgs := []oasis.Message{
		{ID: oasis.NewID(), ConversationID: conv.ID, Role: "user", Content: "Hello", CreatedAt: 1000},
		{ID: oasis.NewID(), ConversationID: conv.ID, Role: "assistant", Content: "Hi there!", CreatedAt: 1001},
		{ID: oasis.NewID(), ConversationID: conv.ID, Role: "user", Content: "How are you?", CreatedAt: 1002},
	}

	for _, m := range msgs {
		if err := s.StoreMessage(ctx, m); err != nil {
			t.Fatalf("StoreMessage: %v", err)
		}
	}

	// Retrieve messages.
	got, err := s.GetMessages(ctx, conv.ID, 10)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}

	// Verify chronological order (oldest first).
	if got[0].Content != "Hello" {
		t.Errorf("first message content = %q, want %q", got[0].Content, "Hello")
	}
	if got[2].Content != "How are you?" {
		t.Errorf("last message content = %q, want %q", got[2].Content, "How are you?")
	}

	// Test limit.
	got2, err := s.GetMessages(ctx, conv.ID, 2)
	if err != nil {
		t.Fatalf("GetMessages with limit: %v", err)
	}
	if len(got2) != 2 {
		t.Fatalf("expected 2 messages with limit, got %d", len(got2))
	}
	// Should return the 2 most recent in chronological order.
	if got2[0].Content != "Hi there!" {
		t.Errorf("limited first = %q, want %q", got2[0].Content, "Hi there!")
	}
	if got2[1].Content != "How are you?" {
		t.Errorf("limited second = %q, want %q", got2[1].Content, "How are you?")
	}
}

func TestGetOrCreateConversationIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	conv1, err := s.GetOrCreateConversation(ctx, "chat-abc")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if conv1.ID == "" {
		t.Fatal("conversation ID is empty")
	}
	if conv1.ChatID != "chat-abc" {
		t.Errorf("ChatID = %q, want %q", conv1.ChatID, "chat-abc")
	}

	conv2, err := s.GetOrCreateConversation(ctx, "chat-abc")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if conv1.ID != conv2.ID {
		t.Errorf("IDs differ: %q vs %q -- not idempotent", conv1.ID, conv2.ID)
	}
	if conv1.CreatedAt != conv2.CreatedAt {
		t.Errorf("CreatedAt differ: %d vs %d", conv1.CreatedAt, conv2.CreatedAt)
	}
}

func TestGetConfigSetConfig(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Get nonexistent key returns empty string.
	val, err := s.GetConfig(ctx, "foo")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty string for missing key, got %q", val)
	}

	// Set and get.
	if err := s.SetConfig(ctx, "foo", "bar"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	val, err = s.GetConfig(ctx, "foo")
	if err != nil {
		t.Fatalf("GetConfig after set: %v", err)
	}
	if val != "bar" {
		t.Errorf("GetConfig = %q, want %q", val, "bar")
	}

	// Overwrite.
	if err := s.SetConfig(ctx, "foo", "baz"); err != nil {
		t.Fatalf("SetConfig overwrite: %v", err)
	}
	val, err = s.GetConfig(ctx, "foo")
	if err != nil {
		t.Fatalf("GetConfig after overwrite: %v", err)
	}
	if val != "baz" {
		t.Errorf("GetConfig = %q, want %q", val, "baz")
	}
}

func TestStoreDocument(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	doc := oasis.Document{
		ID:        oasis.NewID(),
		Title:     "Test Doc",
		Source:    "https://example.com",
		Content:   "Full document content here.",
		CreatedAt: oasis.NowUnix(),
	}

	chunks := []oasis.Chunk{
		{ID: oasis.NewID(), DocumentID: doc.ID, Content: "chunk 1", ChunkIndex: 0},
		{ID: oasis.NewID(), DocumentID: doc.ID, Content: "chunk 2", ChunkIndex: 1},
		{ID: oasis.NewID(), DocumentID: doc.ID, Content: "chunk 3", ChunkIndex: 2},
	}

	if err := s.StoreDocument(ctx, doc, chunks); err != nil {
		t.Fatalf("StoreDocument: %v", err)
	}

	// Verify document was stored by querying directly.
	db, err := s.openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM documents WHERE id = ?", doc.ID).Scan(&count); err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 document, got %d", count)
	}

	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chunks WHERE document_id = ?", doc.ID).Scan(&count); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 chunks, got %d", count)
	}
}

func TestClose(t *testing.T) {
	s := testStore(t)
	// Close should be a no-op and not error.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSerializeEmbedding(t *testing.T) {
	emb := []float32{0.1, 0.2, 0.3, -0.5}
	got := serializeEmbedding(emb)
	want := "[0.1,0.2,0.3,-0.5]"
	if got != want {
		t.Errorf("serializeEmbedding = %q, want %q", got, want)
	}
}
