// memory/store_conformance_inmem_test.go
package memory

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/nevindra/oasis/core"
)

// inMemTestStore mirrors memtest.ItemStore but lives in the memory package
// itself so the in-package conformance suite avoids a cyclic import.
type inMemTestStore struct {
	mu    sync.Mutex
	items map[string]core.MemoryItem
}

func newInMemTestStore() *inMemTestStore { return &inMemTestStore{items: map[string]core.MemoryItem{}} }

func (s *inMemTestStore) Init(context.Context) error { return nil }

func (s *inMemTestStore) Upsert(_ context.Context, it core.MemoryItem) error {
	if it.ID == "" {
		return errors.New("ID required")
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

func (s *inMemTestStore) UpsertBatch(ctx context.Context, items []core.MemoryItem) error {
	for _, it := range items {
		if err := s.Upsert(ctx, it); err != nil {
			return err
		}
	}
	return nil
}

func (s *inMemTestStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
	return nil
}

func (s *inMemTestStore) DeleteWhere(_ context.Context, f core.MemoryFilter) (int, error) {
	if f.IsEmpty() {
		return 0, errors.New("refuse empty filter")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	for id, it := range s.items {
		if filterMatches(it, f) {
			delete(s.items, id)
			n++
		}
	}
	return n, nil
}

func (s *inMemTestStore) Get(_ context.Context, id string) (core.MemoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok {
		return core.MemoryItem{}, core.ErrNotFound
	}
	return it, nil
}

func (s *inMemTestStore) List(_ context.Context, f core.MemoryFilter) ([]core.MemoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.MemoryItem, 0, len(s.items))
	for _, it := range s.items {
		if filterMatches(it, f) {
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

func (s *inMemTestStore) SearchSemantic(_ context.Context, emb []float32, f core.MemoryFilter, topK int) ([]core.ScoredMemoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scored := make([]core.ScoredMemoryItem, 0, len(s.items))
	for _, it := range s.items {
		if !filterMatches(it, f) || len(it.Embedding) == 0 {
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

func filterMatches(it core.MemoryItem, f core.MemoryFilter) bool {
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
	if f.Scope != nil && (it.Scope.Kind != f.Scope.Kind || it.Scope.Ref != f.Scope.Ref) {
		return false
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
