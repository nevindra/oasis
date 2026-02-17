package remember

import (
	"context"
	"encoding/json"
	"testing"

	oasis "github.com/nevindra/oasis"
)

// mockEmbedding returns zero vectors of the right size.
type mockEmbedding struct{}

func (m *mockEmbedding) Embed(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i := range texts {
		result[i] = make([]float32, 8)
	}
	return result, nil
}
func (m *mockEmbedding) Dimensions() int { return 8 }
func (m *mockEmbedding) Name() string    { return "mock" }

// mockStore records calls for verification.
type mockStore struct {
	documents []oasis.Document
	chunks    []oasis.Chunk
}

func (s *mockStore) StoreDocument(_ context.Context, doc oasis.Document, chunks []oasis.Chunk) error {
	s.documents = append(s.documents, doc)
	s.chunks = append(s.chunks, chunks...)
	return nil
}

// Stubs for VectorStore interface — only StoreDocument is used by remember.
func (s *mockStore) StoreMessage(context.Context, oasis.Message) error   { return nil }
func (s *mockStore) GetMessages(context.Context, string, int) ([]oasis.Message, error) {
	return nil, nil
}
func (s *mockStore) SearchMessages(context.Context, []float32, int) ([]oasis.Message, error) {
	return nil, nil
}
func (s *mockStore) SearchChunks(context.Context, []float32, int) ([]oasis.Chunk, error) {
	return nil, nil
}
func (s *mockStore) GetOrCreateConversation(context.Context, string) (oasis.Conversation, error) {
	return oasis.Conversation{}, nil
}
func (s *mockStore) GetConfig(context.Context, string) (string, error) { return "", nil }
func (s *mockStore) SetConfig(context.Context, string, string) error   { return nil }
func (s *mockStore) Init(context.Context) error                        { return nil }
func (s *mockStore) Close() error                                      { return nil }
func (s *mockStore) CreateScheduledAction(context.Context, oasis.ScheduledAction) error {
	return nil
}
func (s *mockStore) ListScheduledActions(context.Context) ([]oasis.ScheduledAction, error) {
	return nil, nil
}
func (s *mockStore) GetDueScheduledActions(context.Context, int64) ([]oasis.ScheduledAction, error) {
	return nil, nil
}
func (s *mockStore) UpdateScheduledAction(context.Context, oasis.ScheduledAction) error { return nil }
func (s *mockStore) UpdateScheduledActionEnabled(context.Context, string, bool) error   { return nil }
func (s *mockStore) DeleteScheduledAction(context.Context, string) error                { return nil }
func (s *mockStore) DeleteAllScheduledActions(context.Context) (int, error)             { return 0, nil }
func (s *mockStore) FindScheduledActionsByDescription(context.Context, string) ([]oasis.ScheduledAction, error) {
	return nil, nil
}

func TestRememberExecute(t *testing.T) {
	store := &mockStore{}
	tool := New(store, &mockEmbedding{})

	args, _ := json.Marshal(map[string]string{"content": "The capital of Indonesia is Jakarta."})
	result, err := tool.Execute(context.Background(), "remember", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if len(store.documents) == 0 {
		t.Error("expected document to be stored")
	}
}

func TestIngestFile(t *testing.T) {
	store := &mockStore{}
	tool := New(store, &mockEmbedding{})

	msg, err := tool.IngestFile(context.Background(), "# Hello\n\nWorld", "readme.md")
	if err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Error("expected message")
	}
	if len(store.documents) == 0 {
		t.Error("expected document")
	}
}

func TestIngestURL(t *testing.T) {
	store := &mockStore{}
	tool := New(store, &mockEmbedding{})

	msg, err := tool.IngestURL(context.Background(), "<p>Hello world</p>", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Error("expected message")
	}
}
