package core_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

type stubResultStore struct {
	data map[string]string
}

func (s *stubResultStore) Put(ctx context.Context, content string) (string, error) {
	id := "id1"
	s.data[id] = content
	return id, nil
}

func (s *stubResultStore) Get(ctx context.Context, id string, offset, length int) (string, int, error) {
	c, ok := s.data[id]
	if !ok {
		return "", 0, core.ErrToolResultNotFound
	}
	total := len(c)
	if offset >= total {
		return "", total, nil
	}
	end := offset + length
	if end > total {
		end = total
	}
	return c[offset:end], total, nil
}

var _ core.ToolResultStore = (*stubResultStore)(nil)

func TestToolResultStoreInterface(t *testing.T) {
	s := &stubResultStore{data: map[string]string{}}
	id, err := s.Put(context.Background(), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	raw, total, err := s.Get(context.Background(), id, 0, len("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	if total != len("hello world") {
		t.Errorf("got total=%d, want %d", total, len("hello world"))
	}
	_ = raw
}

func TestInMemoryStorePutGetRoundTrip(t *testing.T) {
	s := core.NewInMemoryToolResultStore()
	content := "the quick brown fox"
	id, err := s.Put(context.Background(), content)
	if err != nil {
		t.Fatal(err)
	}

	// Fetch full content and verify round-trip.
	raw, total, err := s.Get(context.Background(), id, 0, len(content))
	if err != nil {
		t.Fatal(err)
	}
	if total != len(content) {
		t.Errorf("got total=%d, want %d", total, len(content))
	}
	if raw != content {
		t.Errorf("round-trip mismatch: got %q, want %q", raw, content)
	}
}

func TestInMemoryStoreByteSlicing(t *testing.T) {
	s := core.NewInMemoryToolResultStore()
	// Store ASCII bytes: "hello" → 5 bytes
	payload := `"hello"`
	id, _ := s.Put(context.Background(), payload)
	// Fetch bytes 1–3 (the "ell" in "hello" within the JSON-quoted form)
	raw, total, err := s.Get(context.Background(), id, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(payload) {
		t.Errorf("got total=%d, want %d", total, len(payload))
	}
	if raw != payload[1:4] {
		t.Errorf("got slice=%q, want %q", raw, payload[1:4])
	}
}

func TestInMemoryStoreOffsetPastEnd(t *testing.T) {
	s := core.NewInMemoryToolResultStore()
	id, _ := s.Put(context.Background(), "abc")
	raw, total, err := s.Get(context.Background(), id, 1000, 5)
	if err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		t.Errorf("expected empty string, got %q", raw)
	}
	if total != len("abc") {
		t.Errorf("got total=%d, want %d", total, len("abc"))
	}
}

func TestInMemoryStoreUnknownID(t *testing.T) {
	s := core.NewInMemoryToolResultStore()
	_, _, err := s.Get(context.Background(), "no-such-id", 0, 10)
	if !errors.Is(err, core.ErrToolResultNotFound) {
		t.Errorf("expected ErrToolResultNotFound, got %v", err)
	}
}

func TestInMemoryStoreTTLEviction(t *testing.T) {
	s := core.NewInMemoryToolResultStore(core.WithToolResultTTL(50 * time.Millisecond))
	id, _ := s.Put(context.Background(), "hello")
	time.Sleep(80 * time.Millisecond)
	_, _, err := s.Get(context.Background(), id, 0, 5)
	if !errors.Is(err, core.ErrToolResultNotFound) {
		t.Errorf("expected expired entry to return ErrToolResultNotFound, got %v", err)
	}
}

func TestInMemoryStoreLRUEviction(t *testing.T) {
	s := core.NewInMemoryToolResultStore(core.WithToolResultMaxBytes(10))

	id1, _ := s.Put(context.Background(), "0123456789") // 10 bytes — fills cap
	id2, _ := s.Put(context.Background(), "abcdefghij") // 10 bytes — evicts id1

	_, _, err := s.Get(context.Background(), id1, 0, 10)
	if !errors.Is(err, core.ErrToolResultNotFound) {
		t.Errorf("expected id1 evicted, got %v", err)
	}
	raw, _, err := s.Get(context.Background(), id2, 0, 10)
	if err != nil || raw != "abcdefghij" {
		t.Errorf("expected id2 retained, got raw=%q err=%v", raw, err)
	}
}

func TestInMemoryStoreConcurrentPut(t *testing.T) {
	s := core.NewInMemoryToolResultStore()
	var wg sync.WaitGroup
	ids := make([]string, 100)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := s.Put(context.Background(), fmt.Sprintf("payload-%d", i))
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = id
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate id: %s", id)
		}
		seen[id] = true
	}
}

// TestInMemoryStoreNextExpiryTracking verifies that nextExpiry correctly tracks
// the soonest surviving entry after a partial TTL sweep removes early-expiring
// entries.
func TestInMemoryStoreNextExpiryTracking(t *testing.T) {
	ctx := context.Background()
	// Use two different TTLs: a short one (50ms) and a long one (10s).
	// After the short entries expire, nextExpiry must advance to the long entry's
	// expiry — not stay at the already-gone short expiry.
	shortTTL := 60 * time.Millisecond
	longTTL := 10 * time.Second

	// Store two entries with long TTL first.
	sLong := core.NewInMemoryToolResultStore(core.WithToolResultTTL(longTTL))
	idLong1, err := sLong.Put(ctx, "long-entry-1")
	if err != nil {
		t.Fatal(err)
	}
	idLong2, err := sLong.Put(ctx, "long-entry-2")
	if err != nil {
		t.Fatal(err)
	}

	// Both long entries must be retrievable immediately.
	_, _, err = sLong.Get(ctx, idLong1, 0, 12)
	if err != nil {
		t.Fatalf("long entry 1 missing immediately after put: %v", err)
	}
	_, _, err = sLong.Get(ctx, idLong2, 0, 12)
	if err != nil {
		t.Fatalf("long entry 2 missing immediately after put: %v", err)
	}

	// Now create a separate store with short TTL to exercise the nextExpiry
	// fast-path: after the short entries expire, a subsequent Put/Get must
	// trigger a sweep and remove them.
	sShort := core.NewInMemoryToolResultStore(core.WithToolResultTTL(shortTTL))
	idShort, err := sShort.Put(ctx, "short-entry")
	if err != nil {
		t.Fatal(err)
	}

	// Entry must exist right after put.
	_, _, err = sShort.Get(ctx, idShort, 0, 11)
	if err != nil {
		t.Fatalf("short entry missing immediately after put: %v", err)
	}

	// Wait for short TTL to expire.
	time.Sleep(100 * time.Millisecond)

	// A new Put triggers expireExpiredLocked which must remove the expired entry.
	_, err = sShort.Put(ctx, "trigger-sweep")
	if err != nil {
		t.Fatal(err)
	}

	// The expired entry must now be gone.
	_, _, err = sShort.Get(ctx, idShort, 0, 11)
	if !errors.Is(err, core.ErrToolResultNotFound) {
		t.Errorf("expected expired short entry to be gone after sweep, got: %v", err)
	}
}

// BenchmarkInMemoryStorePut measures Put throughput on a store pre-filled with
// ~10,000 unexpired entries. The nextExpiry fast-path should make the
// expireExpiredLocked call a near-zero-cost early return rather than an O(N) scan.
func BenchmarkInMemoryStorePut(b *testing.B) {
	ctx := context.Background()
	const prefill = 10_000
	// Large enough TTL that nothing expires during the benchmark.
	s := core.NewInMemoryToolResultStore(
		core.WithToolResultTTL(10*time.Minute),
		core.WithToolResultMaxEntries(prefill+b.N+1),
		core.WithToolResultMaxBytes(1<<31), // 2 GiB — won't be hit
	)
	for i := 0; i < prefill; i++ {
		if _, err := s.Put(ctx, fmt.Sprintf("prefill-entry-%d", i)); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Put(ctx, "bench-entry"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkInMemoryStoreGet measures Get throughput on a store pre-filled with
// ~10,000 unexpired entries. The nextExpiry fast-path should skip the O(N)
// expiry scan on every Get.
func BenchmarkInMemoryStoreGet(b *testing.B) {
	ctx := context.Background()
	const prefill = 10_000
	s := core.NewInMemoryToolResultStore(
		core.WithToolResultTTL(10*time.Minute),
		core.WithToolResultMaxEntries(prefill+1),
		core.WithToolResultMaxBytes(1<<31),
	)
	ids := make([]string, prefill)
	for i := 0; i < prefill; i++ {
		id, err := s.Put(ctx, fmt.Sprintf("prefill-entry-%d", i))
		if err != nil {
			b.Fatal(err)
		}
		ids[i] = id
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := ids[i%prefill]
		if _, _, err := s.Get(ctx, id, 0, 5); err != nil {
			b.Fatal(err)
		}
	}
}
