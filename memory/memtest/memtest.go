// memory/memtest/memtest.go
// Package memtest provides an in-memory ItemStore implementation for use in
// framework tests and as a conformance harness for satellite stores.
package memtest

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
)

// ItemStore is a goroutine-safe in-memory ItemStore for tests.
type ItemStore struct {
	mu    sync.Mutex
	items map[string]core.MemoryItem
}

// New returns a fresh in-memory ItemStore.
func New() *ItemStore {
	return &ItemStore{items: map[string]core.MemoryItem{}}
}

func (s *ItemStore) Init(context.Context) error { return nil }

func (s *ItemStore) Upsert(_ context.Context, it core.MemoryItem) error {
	if it.ID == "" {
		return errors.New("memtest: item ID required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	if existing, ok := s.items[it.ID]; ok {
		it.CreatedAt = existing.CreatedAt
	} else if it.CreatedAt == 0 {
		it.CreatedAt = now
	}
	it.UpdatedAt = now
	s.items[it.ID] = it
	return nil
}

func (s *ItemStore) UpsertBatch(ctx context.Context, items []core.MemoryItem) error {
	for _, it := range items {
		if err := s.Upsert(ctx, it); err != nil {
			return err
		}
	}
	return nil
}

func (s *ItemStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
	return nil
}

func (s *ItemStore) DeleteWhere(_ context.Context, f core.MemoryFilter) (int, error) {
	if f.IsEmpty() {
		return 0, errors.New("memtest: refuse to delete with empty filter")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	for id, it := range s.items {
		if matches(it, f) {
			delete(s.items, id)
			n++
		}
	}
	return n, nil
}

func (s *ItemStore) Get(_ context.Context, id string) (core.MemoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok {
		return core.MemoryItem{}, core.ErrNotFound
	}
	return it, nil
}

func (s *ItemStore) List(_ context.Context, f core.MemoryFilter) ([]core.MemoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.MemoryItem, 0, len(s.items))
	for _, it := range s.items {
		if matches(it, f) {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *ItemStore) SearchSemantic(_ context.Context, emb []float32, f core.MemoryFilter, topK int) ([]core.ScoredMemoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scored := make([]core.ScoredMemoryItem, 0, len(s.items))
	for _, it := range s.items {
		if !matches(it, f) || len(it.Embedding) == 0 {
			continue
		}
		scored = append(scored, core.ScoredMemoryItem{Item: it, Score: core.CosineSimilarity(emb, it.Embedding)})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if topK > 0 && len(scored) > topK {
		scored = scored[:topK]
	}
	return scored, nil
}

// ConformanceTest runs every ItemStore contract test against the given store.
// Satellite stores (sqlite, postgres) import this package and call
// ConformanceTest from their own test files to verify identical behavior.
func ConformanceTest(t *testing.T, newStore func(t *testing.T) core.MemoryItemStore) {
	t.Helper()
	t.Run("UpsertGet", func(t *testing.T) { testUpsertGet(t, newStore(t)) })
	t.Run("UpsertOverwrites", func(t *testing.T) { testUpsertOverwrites(t, newStore(t)) })
	t.Run("ListByKind", func(t *testing.T) { testListByKind(t, newStore(t)) })
	t.Run("ListByScope", func(t *testing.T) { testListByScope(t, newStore(t)) })
	t.Run("ListByPinned", func(t *testing.T) { testListByPinned(t, newStore(t)) })
	t.Run("DeleteWhereRejectsEmpty", func(t *testing.T) { testDeleteWhereEmpty(t, newStore(t)) })
	t.Run("SearchSemantic", func(t *testing.T) { testSearchSemantic(t, newStore(t)) })
	t.Run("GetMissingReturnsNotFound", func(t *testing.T) { testGetMissing(t, newStore(t)) })
}

func testUpsertGet(t *testing.T, s core.MemoryItemStore) {
	t.Helper()
	ctx := context.Background()
	it := core.MemoryItem{ID: "x1", Kind: memory.KindFact, Content: "hello", Scope: memory.Scoped(memory.ScopeResource, "u1")}
	if err := s.Upsert(ctx, it); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "x1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "hello" {
		t.Fatalf("content = %q", got.Content)
	}
	if got.UpdatedAt == 0 {
		t.Fatal("UpdatedAt not set")
	}
}

func testUpsertOverwrites(t *testing.T, s core.MemoryItemStore) {
	t.Helper()
	ctx := context.Background()
	first := core.MemoryItem{ID: "x1", Kind: memory.KindFact, Content: "v1", Scope: memory.Scoped(memory.ScopeResource, "u1")}
	if err := s.Upsert(ctx, first); err != nil {
		t.Fatal(err)
	}
	got1, _ := s.Get(ctx, "x1")
	created := got1.CreatedAt

	second := core.MemoryItem{ID: "x1", Kind: memory.KindFact, Content: "v2", Scope: memory.Scoped(memory.ScopeResource, "u1")}
	if err := s.Upsert(ctx, second); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.Get(ctx, "x1")
	if got2.Content != "v2" {
		t.Fatalf("content = %q", got2.Content)
	}
	if got2.CreatedAt != created {
		t.Fatalf("CreatedAt changed: %d -> %d", created, got2.CreatedAt)
	}
	if got2.UpdatedAt < got2.CreatedAt {
		t.Fatalf("UpdatedAt < CreatedAt")
	}
}

func testListByKind(t *testing.T, s core.MemoryItemStore) {
	t.Helper()
	ctx := context.Background()
	mustT(t, s.Upsert(ctx, core.MemoryItem{ID: "a", Kind: memory.KindFact, Scope: memory.Scoped(memory.ScopeResource, "u1")}))
	mustT(t, s.Upsert(ctx, core.MemoryItem{ID: "b", Kind: memory.KindEvent, Scope: memory.Scoped(memory.ScopeResource, "u1")}))
	got, err := s.List(ctx, core.MemoryFilter{Kinds: []core.MemoryKind{memory.KindFact}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %+v", got)
	}
}

func testListByScope(t *testing.T, s core.MemoryItemStore) {
	t.Helper()
	ctx := context.Background()
	mustT(t, s.Upsert(ctx, core.MemoryItem{ID: "a", Kind: memory.KindFact, Scope: memory.Scoped(memory.ScopeResource, "u1")}))
	mustT(t, s.Upsert(ctx, core.MemoryItem{ID: "b", Kind: memory.KindFact, Scope: memory.Scoped(memory.ScopeResource, "u2")}))
	sc := memory.Scoped(memory.ScopeResource, "u1")
	got, err := s.List(ctx, core.MemoryFilter{Scope: &sc})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %+v", got)
	}
}

func testListByPinned(t *testing.T, s core.MemoryItemStore) {
	t.Helper()
	ctx := context.Background()
	mustT(t, s.Upsert(ctx, core.MemoryItem{ID: "a", Kind: memory.KindFact, Pinned: true}))
	mustT(t, s.Upsert(ctx, core.MemoryItem{ID: "b", Kind: memory.KindFact, Pinned: false}))
	yes := true
	got, _ := s.List(ctx, core.MemoryFilter{Pinned: &yes})
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %+v", got)
	}
}

func testDeleteWhereEmpty(t *testing.T, s core.MemoryItemStore) {
	t.Helper()
	_, err := s.DeleteWhere(context.Background(), core.MemoryFilter{})
	if err == nil {
		t.Fatal("expected error on empty filter")
	}
}

func testSearchSemantic(t *testing.T, s core.MemoryItemStore) {
	t.Helper()
	ctx := context.Background()
	mustT(t, s.Upsert(ctx, core.MemoryItem{ID: "near", Kind: memory.KindFact, Content: "near", Embedding: []float32{1, 0, 0}}))
	mustT(t, s.Upsert(ctx, core.MemoryItem{ID: "far", Kind: memory.KindFact, Content: "far", Embedding: []float32{0, 1, 0}}))
	got, err := s.SearchSemantic(ctx, []float32{1, 0, 0}, core.MemoryFilter{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 1 || got[0].Item.ID != "near" {
		t.Fatalf("expected 'near' first: %+v", got)
	}
	// sanity: results are sorted descending
	sortedCopy := make([]core.ScoredMemoryItem, len(got))
	copy(sortedCopy, got)
	sort.Slice(sortedCopy, func(i, j int) bool { return sortedCopy[i].Score > sortedCopy[j].Score })
	for i := range got {
		if got[i].Score != sortedCopy[i].Score {
			t.Fatal("not sorted descending")
		}
	}
}

func testGetMissing(t *testing.T, s core.MemoryItemStore) {
	t.Helper()
	_, err := s.Get(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error")
	}
	if !core.IsNotFound(err) {
		t.Fatalf("expected not-found, got %v", err)
	}
}

func mustT(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func matches(it core.MemoryItem, f core.MemoryFilter) bool {
	if len(f.Kinds) > 0 {
		var ok bool
		for _, k := range f.Kinds {
			if it.Kind == k {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if f.Scope != nil {
		if it.Scope.Kind != f.Scope.Kind || it.Scope.Ref != f.Scope.Ref {
			return false
		}
	}
	for _, t := range f.Tags {
		var has bool
		for _, tag := range it.Tags {
			if tag == t {
				has = true
				break
			}
		}
		if !has {
			return false
		}
	}
	if f.Pinned != nil && it.Pinned != *f.Pinned {
		return false
	}
	if f.Since > 0 && it.CreatedAt < f.Since {
		return false
	}
	if f.Until > 0 && it.CreatedAt > f.Until {
		return false
	}
	if !f.IncludeExp && it.ExpiresAt > 0 && it.ExpiresAt <= time.Now().Unix() {
		return false
	}
	return true
}
