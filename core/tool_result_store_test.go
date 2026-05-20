package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

type stubResultStore struct {
	data map[string]json.RawMessage
}

func (s *stubResultStore) Put(ctx context.Context, content json.RawMessage) (string, error) {
	id := "id1"
	s.data[id] = content
	return id, nil
}

func (s *stubResultStore) Get(ctx context.Context, id string, offset, length int) (json.RawMessage, int, error) {
	c, ok := s.data[id]
	if !ok {
		return nil, 0, core.ErrToolResultNotFound
	}
	total := len(c)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + length
	if end > total {
		end = total
	}
	return json.RawMessage(c[offset:end]), total, nil
}

var _ core.ToolResultStore = (*stubResultStore)(nil)

func TestToolResultStoreInterface(t *testing.T) {
	s := &stubResultStore{data: map[string]json.RawMessage{}}
	id, err := s.Put(context.Background(), core.TextContent("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	raw, total, err := s.Get(context.Background(), id, 0, len(core.TextContent("hello world")))
	if err != nil {
		t.Fatal(err)
	}
	if total != len(core.TextContent("hello world")) {
		t.Errorf("got total=%d, want %d", total, len(core.TextContent("hello world")))
	}
	_ = raw
}

func TestInMemoryStorePutGetRoundTrip(t *testing.T) {
	s := core.NewInMemoryToolResultStore()
	content := core.TextContent("the quick brown fox")
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
	if string(raw) != string(content) {
		t.Errorf("round-trip mismatch: got %q, want %q", raw, content)
	}
}

func TestInMemoryStoreByteSlicing(t *testing.T) {
	s := core.NewInMemoryToolResultStore()
	// Store ASCII bytes: "hello" → 5 bytes
	payload := json.RawMessage(`"hello"`)
	id, _ := s.Put(context.Background(), payload)
	// Fetch bytes 1–3 (the "ell" in "hello" within the JSON-quoted form)
	raw, total, err := s.Get(context.Background(), id, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(payload) {
		t.Errorf("got total=%d, want %d", total, len(payload))
	}
	if string(raw) != string(payload[1:4]) {
		t.Errorf("got slice=%q, want %q", raw, payload[1:4])
	}
}

func TestInMemoryStoreOffsetPastEnd(t *testing.T) {
	s := core.NewInMemoryToolResultStore()
	id, _ := s.Put(context.Background(), core.TextContent("abc"))
	raw, total, err := s.Get(context.Background(), id, 1000, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Errorf("expected empty slice, got %q", raw)
	}
	if total != len(core.TextContent("abc")) {
		t.Errorf("got total=%d, want %d", total, len(core.TextContent("abc")))
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
	id, _ := s.Put(context.Background(), core.TextContent("hello"))
	time.Sleep(80 * time.Millisecond)
	_, _, err := s.Get(context.Background(), id, 0, 5)
	if !errors.Is(err, core.ErrToolResultNotFound) {
		t.Errorf("expected expired entry to return ErrToolResultNotFound, got %v", err)
	}
}

func TestInMemoryStoreLRUEviction(t *testing.T) {
	s := core.NewInMemoryToolResultStore(core.WithToolResultMaxBytes(10))

	id1, _ := s.Put(context.Background(), json.RawMessage("0123456789")) // 10 bytes — fills cap
	id2, _ := s.Put(context.Background(), json.RawMessage("abcdefghij")) // 10 bytes — evicts id1

	_, _, err := s.Get(context.Background(), id1, 0, 10)
	if !errors.Is(err, core.ErrToolResultNotFound) {
		t.Errorf("expected id1 evicted, got %v", err)
	}
	raw, _, err := s.Get(context.Background(), id2, 0, 10)
	if err != nil || string(raw) != "abcdefghij" {
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
			id, err := s.Put(context.Background(), core.TextContent(fmt.Sprintf("payload-%d", i)))
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
