// memory/store_conformance_test.go
package memory

import (
	"context"
	"sort"
	"testing"

	"github.com/nevindra/oasis/core"
)

// ConformanceTest runs every ItemStore contract test against the given store.
// Satellite stores (sqlite, postgres) re-run this against their own
// implementations to verify identical behavior.
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
	ctx := context.Background()
	it := core.MemoryItem{ID: "x1", Kind: KindFact, Content: "hello", Scope: Scoped(ScopeResource, "u1")}
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
	ctx := context.Background()
	first := core.MemoryItem{ID: "x1", Kind: KindFact, Content: "v1", Scope: Scoped(ScopeResource, "u1")}
	if err := s.Upsert(ctx, first); err != nil {
		t.Fatal(err)
	}
	got1, _ := s.Get(ctx, "x1")
	created := got1.CreatedAt

	second := core.MemoryItem{ID: "x1", Kind: KindFact, Content: "v2", Scope: Scoped(ScopeResource, "u1")}
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
	ctx := context.Background()
	must(t, s.Upsert(ctx, core.MemoryItem{ID: "a", Kind: KindFact, Scope: Scoped(ScopeResource, "u1")}))
	must(t, s.Upsert(ctx, core.MemoryItem{ID: "b", Kind: KindEvent, Scope: Scoped(ScopeResource, "u1")}))
	got, err := s.List(ctx, core.MemoryFilter{Kinds: []core.MemoryKind{KindFact}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %+v", got)
	}
}

func testListByScope(t *testing.T, s core.MemoryItemStore) {
	ctx := context.Background()
	must(t, s.Upsert(ctx, core.MemoryItem{ID: "a", Kind: KindFact, Scope: Scoped(ScopeResource, "u1")}))
	must(t, s.Upsert(ctx, core.MemoryItem{ID: "b", Kind: KindFact, Scope: Scoped(ScopeResource, "u2")}))
	sc := Scoped(ScopeResource, "u1")
	got, err := s.List(ctx, core.MemoryFilter{Scope: &sc})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %+v", got)
	}
}

func testListByPinned(t *testing.T, s core.MemoryItemStore) {
	ctx := context.Background()
	must(t, s.Upsert(ctx, core.MemoryItem{ID: "a", Kind: KindFact, Pinned: true}))
	must(t, s.Upsert(ctx, core.MemoryItem{ID: "b", Kind: KindFact, Pinned: false}))
	yes := true
	got, _ := s.List(ctx, core.MemoryFilter{Pinned: &yes})
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %+v", got)
	}
}

func testDeleteWhereEmpty(t *testing.T, s core.MemoryItemStore) {
	_, err := s.DeleteWhere(context.Background(), core.MemoryFilter{})
	if err == nil {
		t.Fatal("expected error on empty filter")
	}
}

func testSearchSemantic(t *testing.T, s core.MemoryItemStore) {
	ctx := context.Background()
	must(t, s.Upsert(ctx, core.MemoryItem{ID: "near", Kind: KindFact, Content: "near", Embedding: []float32{1, 0, 0}}))
	must(t, s.Upsert(ctx, core.MemoryItem{ID: "far", Kind: KindFact, Content: "far", Embedding: []float32{0, 1, 0}}))
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
	_, err := s.Get(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error")
	}
	if !core.IsNotFound(err) {
		t.Fatalf("expected not-found, got %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestConformance_Memtest(t *testing.T) {
	ConformanceTest(t, func(t *testing.T) core.MemoryItemStore {
		// memtest.New returns *memtest.ItemStore which satisfies core.MemoryItemStore.
		// Import path requires moving the import into a build that supports it;
		// for the in-package conformance run we use a local memory-backed
		// implementation. The satellite stores re-call ConformanceTest from
		// their own test files.
		return newInMemory()
	})
}

// newInMemory returns a minimal in-package in-memory core.MemoryItemStore used solely
// for running the conformance suite without importing memtest (which would
// be a cyclic import).
func newInMemory() core.MemoryItemStore {
	// Use a thin shim that forwards to memtest is impossible due to import cycle;
	// instead, duplicate the small in-memory implementation here as test-only code.
	// Implementation: see memory/store_conformance_inmem_test.go
	return newInMemTestStore()
}
