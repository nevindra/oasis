package memory

import (
	"testing"
)

func TestEmbeddingCacheGetMissesEmpty(t *testing.T) {
	c := newEmbeddingCache(8)
	if _, ok := c.get("anything"); ok {
		t.Fatalf("expected miss on empty cache")
	}
}

func TestEmbeddingCachePutThenGet(t *testing.T) {
	c := newEmbeddingCache(8)
	emb := []float32{0.1, 0.2, 0.3}
	c.put("hello", emb)

	got, ok := c.get("hello")
	if !ok {
		t.Fatalf("expected hit after put")
	}
	if len(got) != len(emb) || got[0] != emb[0] {
		t.Fatalf("got %v, want %v", got, emb)
	}

	// Different content → miss.
	if _, ok := c.get("goodbye"); ok {
		t.Fatalf("expected miss for different content")
	}
}

func TestEmbeddingCachePutIsIdempotent(t *testing.T) {
	c := newEmbeddingCache(4)
	c.put("a", []float32{1})
	c.put("a", []float32{2}) // ignored — same key
	got, _ := c.get("a")
	if got[0] != 1 {
		t.Fatalf("second put should be a no-op for existing key; got %v", got)
	}
	if len(c.order) != 1 {
		t.Fatalf("expected order length 1, got %d", len(c.order))
	}
}

func TestEmbeddingCacheEvictsOldestQuarterWhenFull(t *testing.T) {
	// cap=8 → evict cap/4 = 2 when full.
	c := newEmbeddingCache(8)
	for i := 0; i < 8; i++ {
		c.put(keyN(i), []float32{float32(i)})
	}
	// All 8 should be present.
	for i := 0; i < 8; i++ {
		if _, ok := c.get(keyN(i)); !ok {
			t.Fatalf("entry %d should be present pre-eviction", i)
		}
	}

	// Insert one more → triggers eviction of oldest 2.
	c.put(keyN(8), []float32{8})

	// Entries 0 and 1 should be gone.
	for i := 0; i < 2; i++ {
		if _, ok := c.get(keyN(i)); ok {
			t.Errorf("entry %d should have been evicted", i)
		}
	}
	// Entries 2..8 should remain.
	for i := 2; i <= 8; i++ {
		if _, ok := c.get(keyN(i)); !ok {
			t.Errorf("entry %d should still be present", i)
		}
	}
}

func TestEmbeddingCacheHandlesSmallCap(t *testing.T) {
	// cap=2, evict batch = 2/4 = 0 → bumped to 1 by the floor.
	c := newEmbeddingCache(2)
	c.put("a", []float32{1})
	c.put("b", []float32{2})
	c.put("c", []float32{3}) // evicts "a"

	if _, ok := c.get("a"); ok {
		t.Error("'a' should have been evicted")
	}
	if _, ok := c.get("b"); !ok {
		t.Error("'b' should still be present")
	}
	if _, ok := c.get("c"); !ok {
		t.Error("'c' should be present")
	}
}

func keyN(i int) string {
	return string(rune('a' + i))
}
