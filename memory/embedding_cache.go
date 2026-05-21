package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// trimCacheCap bounds the per-AgentMemory cache of message embeddings used
// during semantic trimming. 1024 entries × ~6KB/embedding ≈ 6 MB worst-case —
// well below other memory bounds (maxSuspendBytes = 256 MB). Sized to cover
// ~100 concurrent threads with 10-message histories without churn.
const trimCacheCap = 1024

// trimCacheEvictBatch is the fraction of the cache evicted at once when full.
// Batch eviction avoids per-access bookkeeping (no doubly-linked list), at
// the cost of slightly less precise LRU. Acceptable because semantic trim
// only fires when history exceeds the token budget — not every turn.
const trimCacheEvictBatch = 4 // evict cap/4

// embeddingCache stores message embeddings keyed by content hash. Used by
// semantic trim (trimHistory) to avoid re-embedding the same history messages
// across consecutive turns in a session.
//
// Concurrency: safe for concurrent get/put across multiple BuildMessages calls.
// Eviction: FIFO with batch evict — when len == cap, the oldest cap/N entries
// are dropped in one pass so the next insert is O(1) amortized.
type embeddingCache struct {
	mu      sync.Mutex
	entries map[string][]float32
	order   []string // insertion order; tracks eviction candidates
	cap     int
}

// newEmbeddingCache creates a cache with the given maximum entry count.
func newEmbeddingCache(cap int) *embeddingCache {
	return &embeddingCache{
		entries: make(map[string][]float32, cap),
		order:   make([]string, 0, cap),
		cap:     cap,
	}
}

// get returns the cached embedding for content, or (nil, false) on miss.
func (c *embeddingCache) get(content string) ([]float32, bool) {
	key := hashContent(content)
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[key]
	return v, ok
}

// put stores emb under content's hash. No-op if the key already exists
// (embeddings are content-derived and immutable for a given provider).
// Batch-evicts the oldest cap/trimCacheEvictBatch entries when full.
func (c *embeddingCache) put(content string, emb []float32) {
	key := hashContent(content)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; exists {
		return
	}
	if len(c.entries) >= c.cap {
		evict := c.cap / trimCacheEvictBatch
		if evict < 1 {
			evict = 1
		}
		if evict > len(c.order) {
			evict = len(c.order)
		}
		for i := 0; i < evict; i++ {
			delete(c.entries, c.order[i])
		}
		c.order = c.order[evict:]
	}
	c.entries[key] = emb
	c.order = append(c.order, key)
}

// hashContent returns a stable hex-encoded SHA-256 digest of s. Hashing
// (vs storing the raw content as a key) bounds key size to 64 bytes
// regardless of message length.
func hashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
