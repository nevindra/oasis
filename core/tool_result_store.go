package core

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// ErrToolResultNotFound is returned by ToolResultStore.Get when the id is
// unknown or has expired.
var ErrToolResultNotFound = errors.New("tool result not found or expired")

// ToolResultStore holds full tool results when their content exceeds the
// inline budget set by WithMaxToolResultLen. The LLM retrieves slices via
// the auto-registered read_full_result built-in tool.
//
// Implementations must be safe for concurrent use. The default in-memory
// implementation (NewInMemoryToolResultStore) is bounded by total bytes
// and per-entry TTL with LRU eviction.
type ToolResultStore interface {
	// Put stores the full content and returns an opaque id. The id is
	// embedded in the truncation marker handed to the LLM.
	Put(ctx context.Context, content json.RawMessage) (id string, err error)

	// Get returns a byte slice of the stored content starting at offset bytes,
	// up to length bytes. total is the full byte length of the stored content.
	// offset and length are in bytes; rune-safe alignment is the caller's
	// responsibility (read_full_result handles it for LLM-facing output).
	// Returns ErrToolResultNotFound if the id is unknown or expired.
	// If offset >= total, returns empty content with no error.
	Get(ctx context.Context, id string, offset, length int) (content json.RawMessage, total int, err error)
}

// Compile-time interface satisfaction check.
var _ ToolResultStore = (*inMemoryStore)(nil)

// InMemoryToolResultStoreOption configures the default in-memory ToolResultStore.
type InMemoryToolResultStoreOption func(*inMemoryStore)

// WithToolResultMaxBytes sets the total byte cap across all stored entries.
// When exceeded, oldest entries (by insertion order) are evicted. Default is 10 MiB.
func WithToolResultMaxBytes(n int64) InMemoryToolResultStoreOption {
	return func(s *inMemoryStore) { s.maxBytes = n }
}

// WithToolResultTTL sets the per-entry expiration window. Expired entries are
// removed lazily on the next Get or Put. Default is 5 minutes.
func WithToolResultTTL(d time.Duration) InMemoryToolResultStoreOption {
	return func(s *inMemoryStore) { s.ttl = d }
}

// NewInMemoryToolResultStore returns a bounded in-memory ToolResultStore.
// Default cap: 10 MiB total, 5 min TTL per entry, FIFO eviction on overflow.
func NewInMemoryToolResultStore(opts ...InMemoryToolResultStoreOption) ToolResultStore {
	s := &inMemoryStore{
		entries:  map[string]*storeEntry{},
		order:    []string{},
		maxBytes: 10 * 1024 * 1024,
		ttl:      5 * time.Minute,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

type storeEntry struct {
	content    []byte
	bytes      int64
	expiresAt  time.Time
	lastAccess time.Time
}

type inMemoryStore struct {
	mu         sync.Mutex
	entries    map[string]*storeEntry
	order      []string // FIFO of ids
	totalBytes int64
	maxBytes   int64
	ttl        time.Duration
}

func (s *inMemoryStore) Put(ctx context.Context, content json.RawMessage) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.expireExpiredLocked()

	id := newResultID()
	entry := &storeEntry{
		content:    content,
		bytes:      int64(len(content)),
		expiresAt:  time.Now().Add(s.ttl),
		lastAccess: time.Now(),
	}
	s.entries[id] = entry
	s.order = append(s.order, id)
	s.totalBytes += entry.bytes

	s.evictUntilUnderCapLocked()
	return id, nil
}

func (s *inMemoryStore) Get(ctx context.Context, id string, offset, length int) (json.RawMessage, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.expireExpiredLocked()

	entry, ok := s.entries[id]
	if !ok {
		return nil, 0, ErrToolResultNotFound
	}
	entry.lastAccess = time.Now()

	total := len(entry.content)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + length
	if end > total {
		end = total
	}
	return json.RawMessage(entry.content[offset:end]), total, nil
}

func (s *inMemoryStore) expireExpiredLocked() {
	now := time.Now()
	kept := s.order[:0]
	for _, id := range s.order {
		e, ok := s.entries[id]
		if !ok {
			continue
		}
		if now.After(e.expiresAt) {
			s.totalBytes -= e.bytes
			delete(s.entries, id)
			continue
		}
		kept = append(kept, id)
	}
	s.order = kept
}

func (s *inMemoryStore) evictUntilUnderCapLocked() {
	for s.totalBytes > s.maxBytes && len(s.order) > 0 {
		id := s.order[0]
		s.order = s.order[1:]
		if e, ok := s.entries[id]; ok {
			s.totalBytes -= e.bytes
			delete(s.entries, id)
		}
	}
}

func newResultID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}
