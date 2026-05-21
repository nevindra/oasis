package rag

import (
	"context"

	"github.com/nevindra/oasis/core"
)

// nopStore satisfies core.Store with no-ops. Tests embed it and override only
// the methods they care about.
type nopStore struct{}

func (nopStore) CreateThread(_ context.Context, _ core.Thread) error              { return nil }
func (nopStore) GetThread(_ context.Context, _ string) (core.Thread, error)       { return core.Thread{}, nil }
func (nopStore) ListThreads(_ context.Context, _ string, _ int) ([]core.Thread, error) {
	return nil, nil
}
func (nopStore) UpdateThread(_ context.Context, _ core.Thread) error { return nil }
func (nopStore) DeleteThread(_ context.Context, _ string) error      { return nil }
func (nopStore) StoreMessage(_ context.Context, _ core.Message) error {
	return nil
}
func (nopStore) GetMessages(_ context.Context, _ string, _ int) ([]core.Message, error) {
	return nil, nil
}
func (nopStore) SearchMessages(_ context.Context, _ []float32, _ int, _ string) ([]core.ScoredMessage, error) {
	return nil, nil
}
func (nopStore) StoreDocument(_ context.Context, _ core.Document, _ []core.Chunk) error {
	return nil
}
func (nopStore) ListDocuments(_ context.Context, _ int) ([]core.Document, error) {
	return nil, nil
}
func (nopStore) DeleteDocument(_ context.Context, _ string) error { return nil }
func (nopStore) SearchChunks(_ context.Context, _ []float32, _ int, _ ...core.ChunkFilter) ([]core.ScoredChunk, error) {
	return nil, nil
}
func (nopStore) GetChunksByIDs(_ context.Context, _ []string) ([]core.Chunk, error) {
	return nil, nil
}
func (nopStore) GetConfig(_ context.Context, _ string) (string, error) { return "", nil }
func (nopStore) SetConfig(_ context.Context, _, _ string) error        { return nil }
func (nopStore) CreateScheduledAction(_ context.Context, _ core.ScheduledAction) error {
	return nil
}
func (nopStore) ListScheduledActions(_ context.Context) ([]core.ScheduledAction, error) {
	return nil, nil
}
func (nopStore) GetDueScheduledActions(_ context.Context, _ int64) ([]core.ScheduledAction, error) {
	return nil, nil
}
func (nopStore) UpdateScheduledAction(_ context.Context, _ core.ScheduledAction) error {
	return nil
}
func (nopStore) UpdateScheduledActionEnabled(_ context.Context, _ string, _ bool) error {
	return nil
}
func (nopStore) DeleteScheduledAction(_ context.Context, _ string) error {
	return nil
}
func (nopStore) DeleteAllScheduledActions(_ context.Context) (int, error) { return 0, nil }
func (nopStore) FindScheduledActionsByDescription(_ context.Context, _ string) ([]core.ScheduledAction, error) {
	return nil, nil
}
func (nopStore) Init(_ context.Context) error { return nil }
func (nopStore) Close() error                 { return nil }

// mockProvider is a deterministic stub Provider for retriever tests.
type mockProvider struct {
	name      string
	responses []core.ChatResponse
	idx       int
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) ChatStream(_ context.Context, _ core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	if m.idx >= len(m.responses) {
		return core.ChatResponse{Content: "exhausted"}, nil
	}
	resp := m.responses[m.idx]
	m.idx++
	ch <- core.StreamEvent{Type: core.EventTextDelta, Content: resp.Content}
	return resp, nil
}
