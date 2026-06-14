// memory/test_helpers_test.go
package memory

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/nevindra/oasis/core"
)

// testStore is the composite test double used by ingest-processor tests.
// It embeds *inMemTestStore (for ItemStore methods) and provides in-memory
// implementations of the core.Store methods that EnsureThread and
// PersistMessages actually invoke, plus zero-value stubs for every other
// method required by the core.Store interface.
type testStore struct {
	*inMemTestStore
	mu       sync.Mutex
	threads  map[string]core.Thread
	messages map[string][]core.Message
}

func newConformanceStore(_ interface{ Helper() }) *testStore {
	return &testStore{
		inMemTestStore: newInMemTestStore(),
		threads:        map[string]core.Thread{},
		messages:       map[string][]core.Message{},
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// --- core.Store: Threads ---

func (s *testStore) CreateThread(_ context.Context, t core.Thread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads[t.ID] = t
	return nil
}

func (s *testStore) GetThread(_ context.Context, id string) (core.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.threads[id]
	if !ok {
		return core.Thread{}, errors.New("not found")
	}
	return t, nil
}

func (s *testStore) ListThreads(_ context.Context, _ string, _ int) ([]core.Thread, error) {
	return nil, nil
}

func (s *testStore) UpdateThread(_ context.Context, t core.Thread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads[t.ID] = t
	return nil
}

func (s *testStore) DeleteThread(_ context.Context, _ string) error { return nil }

// --- core.Store: Messages ---

func (s *testStore) StoreMessage(_ context.Context, m core.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages[m.ThreadID] = append(s.messages[m.ThreadID], m)
	return nil
}

func (s *testStore) GetMessages(_ context.Context, _ string, _ int) ([]core.Message, error) {
	return nil, nil
}

func (s *testStore) SearchMessages(_ context.Context, _ []float32, _ int, _ string) ([]core.ScoredMessage, error) {
	return nil, nil
}

// --- core.Store: Documents + Chunks ---

func (s *testStore) StoreDocument(_ context.Context, _ core.Document, _ []core.Chunk) error {
	return nil
}

func (s *testStore) ListDocuments(_ context.Context, _ int) ([]core.Document, error) {
	return nil, nil
}

func (s *testStore) DeleteDocument(_ context.Context, _ string) error { return nil }

func (s *testStore) SearchChunks(_ context.Context, _ []float32, _ int, _ ...core.ChunkFilter) ([]core.ScoredChunk, error) {
	return nil, nil
}

func (s *testStore) GetChunksByIDs(_ context.Context, _ []string) ([]core.Chunk, error) {
	return nil, nil
}

// --- core.Store: Key-value config ---

func (s *testStore) GetConfig(_ context.Context, _ string) (string, error) { return "", nil }
func (s *testStore) SetConfig(_ context.Context, _, _ string) error        { return nil }

// --- core.Store: Lifecycle ---

// Init is already provided by inMemTestStore (Init(context.Context) error).
// Close is explicitly implemented here to satisfy core.Store.
func (s *testStore) Close() error { return nil }

// compile-time checks that *testStore satisfies both required interfaces
var _ core.Store = (*testStore)(nil)
var _ core.MemoryItemStore = (*testStore)(nil)
