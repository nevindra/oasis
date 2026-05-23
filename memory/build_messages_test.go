package memory

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/nevindra/oasis/core"
)

// --- store stub for memory package tests ---

// noopStore is a minimal core.Store stub that returns empty/nil for all calls.
type noopStore struct{}

func (s *noopStore) StoreMessage(context.Context, core.Message) error { return nil }
func (s *noopStore) GetMessages(context.Context, string, int) ([]core.Message, error) {
	return nil, nil
}
func (s *noopStore) SearchMessages(context.Context, []float32, int, string) ([]core.ScoredMessage, error) {
	return nil, nil
}
func (s *noopStore) SearchChunks(context.Context, []float32, int, ...core.ChunkFilter) ([]core.ScoredChunk, error) {
	return nil, nil
}
func (s *noopStore) GetChunksByIDs(context.Context, []string) ([]core.Chunk, error) {
	return nil, nil
}
func (s *noopStore) StoreDocument(context.Context, core.Document, []core.Chunk) error { return nil }
func (s *noopStore) ListDocuments(context.Context, int) ([]core.Document, error)      { return nil, nil }
func (s *noopStore) DeleteDocument(context.Context, string) error                     { return nil }
func (s *noopStore) CreateThread(context.Context, core.Thread) error                  { return nil }
func (s *noopStore) GetThread(context.Context, string) (core.Thread, error) {
	return core.Thread{}, errors.New("not found")
}
func (s *noopStore) ListThreads(context.Context, string, int) ([]core.Thread, error) {
	return nil, nil
}
func (s *noopStore) UpdateThread(context.Context, core.Thread) error { return nil }
func (s *noopStore) DeleteThread(context.Context, string) error      { return nil }
func (s *noopStore) GetConfig(context.Context, string) (string, error) {
	return "", nil
}
func (s *noopStore) SetConfig(context.Context, string, string) error { return nil }
func (s *noopStore) Init(context.Context) error                      { return nil }
func (s *noopStore) Close() error                                    { return nil }
func (s *noopStore) CreateScheduledAction(context.Context, core.ScheduledAction) error {
	return nil
}
func (s *noopStore) ListScheduledActions(context.Context) ([]core.ScheduledAction, error) {
	return nil, nil
}
func (s *noopStore) GetDueScheduledActions(context.Context, int64) ([]core.ScheduledAction, error) {
	return nil, nil
}
func (s *noopStore) UpdateScheduledAction(context.Context, core.ScheduledAction) error { return nil }
func (s *noopStore) UpdateScheduledActionEnabled(context.Context, string, bool) error  { return nil }
func (s *noopStore) DeleteScheduledAction(context.Context, string) error               { return nil }
func (s *noopStore) DeleteAllScheduledActions(context.Context) (int, error)            { return 0, nil }
func (s *noopStore) FindScheduledActionsByDescription(context.Context, string) ([]core.ScheduledAction, error) {
	return nil, nil
}

// errorHistoryStore simulates a store whose GetMessages fails, used to
// verify that downstream memory features (cross-thread recall) degrade
// gracefully rather than running on empty history.
type errorHistoryStore struct {
	noopStore
	related []core.ScoredMessage
}

func (s *errorHistoryStore) GetMessages(_ context.Context, _ string, _ int) ([]core.Message, error) {
	return nil, errors.New("database unavailable")
}

func (s *errorHistoryStore) SearchMessages(_ context.Context, _ []float32, _ int, _ string) ([]core.ScoredMessage, error) {
	return s.related, nil
}

// stubEmbeddingProvider returns a fixed non-zero embedding for any input.
type stubEmbeddingProvider struct{}

func (e *stubEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}
func (e *stubEmbeddingProvider) Dimensions() int { return 3 }
func (e *stubEmbeddingProvider) Name() string    { return "stub" }

// TestMemoryLoadHistoryError_SkipsRecall verifies that when the conversation
// store fails to return history, the agent does NOT fire cross-thread recall.
// Running recall on top of missing history injects context the LLM has no
// way to relate to. The request still goes through with system prompt + user
// input only.
func TestMemoryLoadHistoryError_SkipsRecall(t *testing.T) {
	store := &errorHistoryStore{
		related: []core.ScoredMessage{
			{Message: core.Message{ThreadID: "other", Role: "user", Content: "earlier note"}, Score: 0.9},
		},
	}
	emb := &stubEmbeddingProvider{}

	m := &AgentMemory{
		store:             store,
		embedding:         emb,
		crossThreadSearch: true,
		maxHistory:        10,
		logger:            slog.Default(),
	}

	task := core.AgentTask{Input: "hi", ThreadID: "t1"}
	msgs := m.BuildMessages(context.Background(), "test_agent", "base system", task)

	// Verify no recall system message was injected.
	recallFound := false
	for _, msg := range msgs {
		if msg.Role == "system" && msg.Content != "base system" {
			recallFound = true
		}
	}
	if recallFound {
		t.Error("cross-thread recall should be skipped when history load fails, but a recall system message was found")
	}

	// Sanity: system prompt and user message must still be present.
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		roles[i] = string(m.Role)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages (system + user), got %d: %v", len(msgs), roles)
	}
}

// --- mergeAdjacentSystemMessages unit tests ---

// TestMergeAdjacentSystemMessages covers the merge helper directly.
func TestMergeAdjacentSystemMessages(t *testing.T) {
	sys := func(s string) core.ChatMessage { return core.ChatMessage{Role: "system", Content: s} }
	usr := func(s string) core.ChatMessage { return core.ChatMessage{Role: "user", Content: s} }

	tests := []struct {
		name  string
		input []core.ChatMessage
		// wantLen is the expected number of messages after merging.
		wantLen int
		// wantSystemContent is the expected Content of the first system message (if any).
		wantSystemContent string
	}{
		{
			name:    "zero messages",
			input:   nil,
			wantLen: 0,
		},
		{
			name:    "one system message",
			input:   []core.ChatMessage{sys("only system")},
			wantLen: 1,
			wantSystemContent: "only system",
		},
		{
			name:    "two adjacent system messages",
			input:   []core.ChatMessage{sys("part A"), sys("part B")},
			wantLen: 1,
			wantSystemContent: "part A\n\npart B",
		},
		{
			name:    "two systems separated by user",
			input:   []core.ChatMessage{sys("S1"), usr("U"), sys("S2")},
			wantLen: 3,
			wantSystemContent: "S1",
		},
		{
			name:    "three adjacent system messages",
			input:   []core.ChatMessage{sys("A"), sys("B"), sys("C")},
			wantLen: 1,
			wantSystemContent: "A\n\nB\n\nC",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeAdjacentSystemMessages(tc.input)
			if len(got) != tc.wantLen {
				t.Fatalf("len(got) = %d, want %d; messages: %v", len(got), tc.wantLen, got)
			}
			if tc.wantSystemContent != "" {
				var firstSys string
				for _, m := range got {
					if m.Role == "system" {
						firstSys = m.Content
						break
					}
				}
				if firstSys != tc.wantSystemContent {
					t.Errorf("first system content = %q, want %q", firstSys, tc.wantSystemContent)
				}
			}
			// Verify no two adjacent messages are both "system".
			for i := 1; i < len(got); i++ {
				if got[i-1].Role == "system" && got[i].Role == "system" {
					t.Errorf("adjacent system messages remain at index %d..%d", i-1, i)
				}
			}
		})
	}
}
