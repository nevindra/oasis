package core

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"sync"
	"time"
)

// ErrToolResultNotFound is returned by ToolResultStore.Get when the id is
// unknown or has expired.
var ErrToolResultNotFound = errors.New("tool result not found or expired")

// ToolResultStore holds full tool results when their content exceeds the
// inline budget set by WithMaxToolResultLen. The framework writes to the store
// for post-hoc inspection; oversize results are split into multiple sequential
// tool-result messages to the LLM (transparent chunking — no LLM-visible hints).
//
// Implementations must be safe for concurrent use. The default in-memory
// implementation (NewInMemoryToolResultStore) is bounded by total bytes
// and per-entry TTL with LRU eviction.
type ToolResultStore interface {
	// Put stores the full content and returns an opaque id for post-hoc
	// inspection. The id is not surfaced to the LLM.
	Put(ctx context.Context, content string) (id string, err error)

	// Get returns a substring of the stored content starting at offset bytes,
	// up to length bytes. total is the full byte length of the stored content.
	// offset and length are in bytes.
	// Returns ErrToolResultNotFound if the id is unknown or expired.
	// If offset >= total, returns empty content with no error.
	Get(ctx context.Context, id string, offset, length int) (content string, total int, err error)
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

// WithToolResultMaxEntries sets the maximum number of stored entries.
// When exceeded, oldest entries (by insertion order) are evicted. Default is 10_000.
//
// Why: the byte cap alone does not prevent unbounded map growth when many
// small tool results land in the store — a long-lived agent that records
// thousands of zero/tiny payloads will never trip the byte cap but the
// internal map and FIFO order slice grow without bound, and expireExpiredLocked
// walks them on every Put/Get.
func WithToolResultMaxEntries(n int) InMemoryToolResultStoreOption {
	return func(s *inMemoryStore) { s.maxEntries = n }
}

// WithToolResultTTL sets the per-entry expiration window. Expired entries are
// removed lazily on the next Get or Put. Default is 5 minutes.
func WithToolResultTTL(d time.Duration) InMemoryToolResultStoreOption {
	return func(s *inMemoryStore) { s.ttl = d }
}

// NewInMemoryToolResultStore returns a bounded in-memory ToolResultStore.
// Default cap: 10 MiB total, 10_000 entries, 5 min TTL per entry, FIFO eviction on overflow.
func NewInMemoryToolResultStore(opts ...InMemoryToolResultStoreOption) ToolResultStore {
	s := &inMemoryStore{
		entries:    map[string]*storeEntry{},
		order:      []string{},
		maxBytes:   10 * 1024 * 1024,
		maxEntries: 10_000,
		ttl:        5 * time.Minute,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

type storeEntry struct {
	content    string
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
	maxEntries int
	ttl        time.Duration
	// Why: O(1) fast path for expireExpiredLocked. nextExpiry tracks the
	// minimum expiresAt across all live entries. If now < nextExpiry, nothing
	// can have expired yet and the O(N) scan is skipped entirely. The invariant
	// is that nextExpiry is NEVER later than the true minimum — a stale-but-
	// earlier value wastes at most one scan; a later-than-true value would allow
	// expired entries to survive a Get/Put, which is a correctness bug.
	// FIFO and byte-cap eviction remove entries but never need to update
	// nextExpiry: removing an entry can only increase (or keep equal) the true
	// minimum, so any existing nextExpiry remains <= the true minimum — the
	// invariant is preserved. Only Put (which adds a new entry potentially
	// earlier than all survivors) and the sweep itself (which must recompute
	// after removals) touch this field.
	nextExpiry time.Time
}

func (s *inMemoryStore) Put(ctx context.Context, content string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.expireExpiredLocked()

	id := newResultID()
	now := time.Now()
	entry := &storeEntry{
		content:    content,
		bytes:      int64(len(content)),
		expiresAt:  now.Add(s.ttl),
		lastAccess: now,
	}
	s.entries[id] = entry
	s.order = append(s.order, id)
	s.totalBytes += entry.bytes

	// Why: maintain the nextExpiry invariant — take the earlier of the current
	// tracked minimum and the new entry's expiry. Zero nextExpiry means unset
	// (no entries were live before this Put), so always take the new expiry.
	if s.nextExpiry.IsZero() || entry.expiresAt.Before(s.nextExpiry) {
		s.nextExpiry = entry.expiresAt
	}

	s.evictLocked()
	return id, nil
}

func (s *inMemoryStore) Get(ctx context.Context, id string, offset, length int) (string, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.expireExpiredLocked()

	entry, ok := s.entries[id]
	if !ok {
		return "", 0, ErrToolResultNotFound
	}
	entry.lastAccess = time.Now()

	total := len(entry.content)
	if offset >= total {
		return "", total, nil
	}
	end := offset + length
	if end > total {
		end = total
	}
	return entry.content[offset:end], total, nil
}

func (s *inMemoryStore) expireExpiredLocked() {
	// Why: O(1) fast path — if the soonest-expiring entry has not yet expired,
	// nothing in the store can have expired, so skip the O(N) scan entirely.
	// This is the common case for short-lived agent runs (typical TTL 5 minutes,
	// run duration well under 5 minutes). Zero nextExpiry means the store is
	// empty; nothing to do.
	now := time.Now()
	if s.nextExpiry.IsZero() || now.Before(s.nextExpiry) {
		return
	}

	// Slow path: at least one entry may have expired. Walk and recompute.
	kept := s.order[:0]
	var minExpiry time.Time
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
		// Why: recompute the minimum expiry of surviving entries so nextExpiry
		// reflects the true minimum after removals. Zero minExpiry means no
		// survivors have been seen yet.
		if minExpiry.IsZero() || e.expiresAt.Before(minExpiry) {
			minExpiry = e.expiresAt
		}
	}
	s.order = kept
	// Why: update nextExpiry to the recomputed minimum. If no entries survived,
	// minExpiry is zero, which correctly marks the store as empty. This ensures
	// the invariant (nextExpiry <= true minimum) is maintained post-sweep.
	s.nextExpiry = minExpiry
}

// evictLocked enforces both the byte cap and entry cap in a single FIFO pass.
// Caller must hold s.mu.
func (s *inMemoryStore) evictLocked() {
	for len(s.order) > 0 {
		overBytes := s.totalBytes > s.maxBytes
		overEntries := s.maxEntries > 0 && len(s.order) > s.maxEntries
		if !overBytes && !overEntries {
			return
		}
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
