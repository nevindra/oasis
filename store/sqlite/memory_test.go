// store/sqlite/memory_test.go
package sqlite_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
	"github.com/nevindra/oasis/memory/memtest"
	"github.com/nevindra/oasis/store/sqlite"
)

func TestSQLite_ItemStoreConformance(t *testing.T) {
	memtest.ConformanceTest(t, func(t *testing.T) core.MemoryItemStore {
		ctx := context.Background()
		s := sqlite.New(":memory:")
		if err := s.Init(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s.Memory() // returns the ItemStore handle on the satellite store
	})
}

// TestUpsertBatch_Atomicity verifies that a mid-batch failure causes a full
// rollback: no items from the batch are visible after the error.
func TestUpsertBatch_Atomicity(t *testing.T) {
	ctx := context.Background()
	s := sqlite.New(":memory:")
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	store := s.Memory()

	goodItem := core.MemoryItem{
		ID:      "batch-item-1",
		Kind:    memory.KindFact,
		Content: "should be rolled back",
		Scope:   memory.Scoped(memory.ScopeResource, "test"),
	}
	// An item with an empty ID will be rejected by upsertTx, simulating a
	// mid-batch failure after goodItem has been written within the tx.
	badItem := core.MemoryItem{
		ID:      "", // triggers "sqlite: item ID required"
		Kind:    memory.KindFact,
		Content: "bad item",
		Scope:   memory.Scoped(memory.ScopeResource, "test"),
	}

	err := store.UpsertBatch(ctx, []core.MemoryItem{goodItem, badItem})
	if err == nil {
		t.Fatal("UpsertBatch: expected error due to bad item, got nil")
	}

	// True atomicity: goodItem must NOT be present because the tx was rolled back.
	_, getErr := store.Get(ctx, goodItem.ID)
	if !errors.Is(getErr, core.ErrNotFound) {
		t.Fatalf("UpsertBatch atomicity broken: goodItem was persisted despite batch error (Get returned: %v)", getErr)
	}
}
