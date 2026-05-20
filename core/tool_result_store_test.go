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
	runes := []rune(c)
	if offset >= len(runes) {
		return "", len(runes), nil
	}
	end := offset + length
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[offset:end]), len(runes), nil
}

var _ core.ToolResultStore = (*stubResultStore)(nil)

func TestToolResultStoreInterface(t *testing.T) {
	s := &stubResultStore{data: map[string]string{}}
	id, err := s.Put(context.Background(), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	slice, total, err := s.Get(context.Background(), id, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if slice != "hello" || total != 11 {
		t.Errorf("got slice=%q total=%d, want %q 11", slice, total, "hello")
	}
}

func TestInMemoryStorePutGetRoundTrip(t *testing.T) {
	s := core.NewInMemoryToolResultStore()
	id, err := s.Put(context.Background(), "the quick brown fox")
	if err != nil {
		t.Fatal(err)
	}

	slice, total, err := s.Get(context.Background(), id, 4, 5)
	if err != nil {
		t.Fatal(err)
	}
	if slice != "quick" || total != 19 {
		t.Errorf("got slice=%q total=%d, want quick/19", slice, total)
	}
}

func TestInMemoryStoreOffsetPastEnd(t *testing.T) {
	s := core.NewInMemoryToolResultStore()
	id, _ := s.Put(context.Background(), "abc")
	slice, total, err := s.Get(context.Background(), id, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	if slice != "" || total != 3 {
		t.Errorf("got slice=%q total=%d, want empty/3", slice, total)
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
	slice, _, err := s.Get(context.Background(), id2, 0, 10)
	if err != nil || slice != "abcdefghij" {
		t.Errorf("expected id2 retained, got slice=%q err=%v", slice, err)
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
