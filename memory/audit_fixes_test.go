// memory/audit_fixes_test.go
package memory

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/nevindra/oasis/core"
)

// captureLogger returns a slog.Logger writing JSON to buf at Debug level so
// tests can assert that a degraded-state warning was actually surfaced.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// deleteErrStore wraps the composite test store and fails every Delete so we
// can verify that delete errors are surfaced (logged) rather than swallowed.
// It embeds *testStore so it satisfies both core.Store and core.MemoryItemStore.
type deleteErrStore struct {
	*testStore
	mu       sync.Mutex
	attempts int
}

func newDeleteErrStore(t *testing.T) *deleteErrStore {
	return &deleteErrStore{testStore: newConformanceStore(t)}
}

func (s *deleteErrStore) Delete(_ context.Context, _ string) error {
	s.mu.Lock()
	s.attempts++
	s.mu.Unlock()
	return errors.New("delete boom")
}

var (
	_ core.Store           = (*deleteErrStore)(nil)
	_ core.MemoryItemStore = (*deleteErrStore)(nil)
)

// --- Finding 1: Remember swallows embedding errors ---

func TestRemember_EmbeddingFails_StoresWithoutVector(t *testing.T) {
	store := newConformanceStore(t)
	emb := &fakeEmbedder{err: errors.New("embed boom")}
	var buf bytes.Buffer
	var m AgentMemory
	m.Init(AgentMemoryConfig{Store: store, Embedding: emb, Logger: captureLogger(&buf)})

	err := m.Remember(context.Background(), core.MemoryItem{
		ID: "x1", Kind: KindFact, Content: "remember me",
	})
	// Remember must succeed (store-without-vector fallback is correct).
	if err != nil {
		t.Fatalf("Remember returned error: %v", err)
	}
	// Item must be stored, just without a vector.
	got, gerr := store.Get(context.Background(), "x1")
	if gerr != nil {
		t.Fatalf("item not stored: %v", gerr)
	}
	if len(got.Embedding) != 0 {
		t.Fatalf("expected no embedding, got %d dims", len(got.Embedding))
	}
	// The degraded state must be observable: a warning was logged.
	if !strings.Contains(buf.String(), "embed boom") {
		t.Fatalf("embedding failure not surfaced in logs; got: %s", buf.String())
	}
}

// --- Finding 2: Forget (ByMatch) swallows delete errors ---

func TestForget_ByMatch_DeleteError_SurfacedAndCountsSuccesses(t *testing.T) {
	store := newDeleteErrStore(t)
	must(t, store.Upsert(context.Background(), core.MemoryItem{
		ID: "a", Kind: KindFact, Content: "alpha match",
	}))
	var buf bytes.Buffer
	var m AgentMemory
	m.Init(AgentMemoryConfig{Store: store, Logger: captureLogger(&buf)})

	n, err := m.Forget(context.Background(), ForgetByMatch("match"))
	if err != nil {
		t.Fatalf("Forget returned error: %v", err)
	}
	// Delete failed, so the count must reflect zero successes.
	if n != 0 {
		t.Fatalf("count = %d, want 0 (delete failed)", n)
	}
	// The failure must be surfaced (logged), not silently dropped.
	if !strings.Contains(buf.String(), "delete boom") && !strings.Contains(strings.ToLower(buf.String()), "delete") {
		t.Fatalf("delete failure not surfaced in logs; got: %s", buf.String())
	}
}

// --- Finding 3: Deduper swallows supersede-delete errors ---

func TestDeduper_SupersedeDeleteError_Surfaced(t *testing.T) {
	store := newDeleteErrStore(t)
	// Seed an existing fact that the candidate will supersede.
	must(t, store.Upsert(context.Background(), core.MemoryItem{
		ID: "old", Kind: KindFact, Content: "Lives in Jakarta",
		Embedding: []float32{1, 0, 0},
	}))
	emb := &fakeEmbedder{out: [][]float32{{1, 0, 0}}}
	var buf bytes.Buffer
	in := &IngestContext{
		Candidates: []core.MemoryItem{
			{ID: "new", Kind: KindFact, Content: "Lives in Bali",
				Tags: []string{"category:personal", "supersedes:Lives in Jakarta"}},
		},
		ItemStore: store,
		Embedding: emb,
		Logger:    captureLogger(&buf),
	}
	if err := (Deduper{}).Process(context.Background(), in); err != nil {
		t.Fatalf("Deduper returned error: %v", err)
	}
	// The supersede branch must have attempted a delete...
	if store.attempts == 0 {
		t.Fatal("expected a supersede-delete attempt")
	}
	// ...and the failure must be surfaced (logged), not swallowed.
	if !strings.Contains(strings.ToLower(buf.String()), "delete") {
		t.Fatalf("supersede-delete failure not surfaced in logs; got: %s", buf.String())
	}
}

// --- Finding 5: WithWorkingMemory loads the canonical KindNote item ---

func TestBuildMessages_WithWorkingMemory_LoadsKindNote(t *testing.T) {
	store := newConformanceStore(t)
	agentName := "agent"
	task := core.AgentTask{ThreadID: "t1", ChatID: "c1", Input: "what's the plan"}

	// Store the canonical working-memory note at its deterministic ID.
	wmID := WorkingMemoryID(agentName, Scoped(ScopeResource, task.ChatID))
	must(t, store.Upsert(context.Background(), core.MemoryItem{
		ID: wmID, Kind: KindNote, Content: "WORKING_MEMORY_CONTENT",
		Scope: Scoped(ScopeResource, task.ChatID),
	}))

	contains := func(msgs []core.ChatMessage, sub string) bool {
		for _, mm := range msgs {
			if strings.Contains(mm.Content, sub) {
				return true
			}
		}
		return false
	}

	// Without the option: working memory must NOT be loaded.
	var off AgentMemory
	off.Init(AgentMemoryConfig{Store: store, Logger: discardLogger()})
	if contains(off.BuildMessages(context.Background(), agentName, "", task), "WORKING_MEMORY_CONTENT") {
		t.Fatal("working memory loaded without WithWorkingMemory")
	}

	// With the option: working memory MUST appear in the built messages.
	var on AgentMemory
	cfg := BuildConfig(WithStore(store), WithWorkingMemory(), WithLogger(discardLogger()))
	on.Init(cfg)
	msgs := on.BuildMessages(context.Background(), agentName, "", task)
	if !contains(msgs, "WORKING_MEMORY_CONTENT") {
		t.Fatalf("working memory not loaded with WithWorkingMemory; msgs: %v", msgs)
	}
}
