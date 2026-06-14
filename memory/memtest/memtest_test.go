// memory/memtest/memtest_test.go
package memtest_test

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
	"github.com/nevindra/oasis/memory/memtest"
)

func TestMemStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := memtest.New()
	item := core.MemoryItem{
		ID:      "a1",
		Kind:    memory.KindFact,
		Content: "user's name is Nev",
		Scope:   memory.Scoped(memory.ScopeResource, "user_1"),
	}
	if err := s.Upsert(ctx, item); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.Get(ctx, "a1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Content != item.Content {
		t.Fatalf("content mismatch: %q vs %q", got.Content, item.Content)
	}
}
