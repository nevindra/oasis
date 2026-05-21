// Package memory provides conversational memory wiring for LLM agents.
//
// The package splits across four files by concern:
//
//   - memory_orchestration.go: AgentMemory struct, Init/Close lifecycle,
//     MemoryStore interface, shared helpers
//   - build_messages.go:       BuildMessages, trimHistory, buildSystemPrompt
//                              (context assembly for the LLM call)
//   - persist.go:              PersistMessages, ensureThread,
//                              generateTitleNewThread (background writes)
//   - facts.go:                ExtractedFact and the fact-extraction
//                              pipeline (LLM-driven memory updates)
package memory

import (
	"context"
	"log/slog"
	"sync"

	"github.com/nevindra/oasis/core"
)

// MemoryStore provides long-term user memory with semantic deduplication.
// Optional — pass to WithUserMemory() to enable.
type MemoryStore interface {
	UpsertFact(ctx context.Context, fact, category string, embedding []float32) error
	// SearchFacts returns facts semantically similar to the query embedding,
	// sorted by Score descending. Only facts with confidence >= 0.3 are returned.
	SearchFacts(ctx context.Context, embedding []float32, topK int) ([]core.ScoredFact, error)
	BuildContext(ctx context.Context, queryEmbedding []float32) (string, error)
	// DeleteFact removes a single fact by its ID.
	DeleteFact(ctx context.Context, factID string) error
	// DeleteMatchingFacts removes facts whose text contains the given substring.
	// Implementations must treat pattern as a plain substring match — never as
	// SQL LIKE, regex, or any other pattern language — to prevent injection.
	DeleteMatchingFacts(ctx context.Context, pattern string) error
	DecayOldFacts(ctx context.Context) error
	Init(ctx context.Context) error
}

// maxPersistGoroutines is the maximum number of concurrent background
// persist goroutines. Provides backpressure when the store or embedding
// provider is slow, preventing unbounded goroutine growth.
const maxPersistGoroutines = 16

// AgentMemory provides shared memory wiring for LLMAgent and Network.
// All fields are optional — nil means the feature is disabled.
type AgentMemory struct {
	store             core.Store             // conversation history
	embedding         core.EmbeddingProvider // shared embedding provider
	memory            MemoryStore            // user facts
	crossThreadSearch bool                   // enabled by CrossThreadSearch option
	semanticMinScore  float32                // 0 = use defaultSemanticRecallMinScore
	maxHistory        int                    // 0 = use defaultMaxHistory
	maxTokens         int                    // 0 = disabled (no token-based trimming)
	autoTitle         bool                   // generate thread title from first message
	provider          core.Provider          // for auto-extraction when memory != nil
	semanticTrimming  bool                   // enabled by WithSemanticTrimming option
	trimmingEmbedding core.EmbeddingProvider // for semantic trimming (may equal embedding)
	keepRecent        int                    // 0 = use defaultKeepRecent
	tracer            core.Tracer            // nil = no tracing
	logger            *slog.Logger           // never nil (nopLogger fallback)
	semOnce           sync.Once              // guards sem initialization
	sem               chan struct{}          // bounded concurrency for background goroutines
	wg                sync.WaitGroup         // tracks in-flight persist goroutines
	trimCacheOnce     sync.Once              // guards trimCache initialization
	trimCache         *embeddingCache        // memoizes semantic-trim embeddings
}

// AgentMemoryConfig holds the optional settings for AgentMemory.
// All fields are optional — zero means "use default" or "disabled".
type AgentMemoryConfig struct {
	Store             core.Store
	Embedding         core.EmbeddingProvider
	Memory            MemoryStore
	CrossThreadSearch bool
	SemanticMinScore  float32
	MaxHistory        int
	MaxTokens         int
	AutoTitle         bool
	Provider          core.Provider
	SemanticTrimming  bool
	TrimmingEmbedding core.EmbeddingProvider
	KeepRecent        int
	Tracer            core.Tracer
	Logger            *slog.Logger
}

// Init populates the AgentMemory from the given config.
// Call once before using the AgentMemory. Subsequent calls overwrite all fields.
func (m *AgentMemory) Init(cfg AgentMemoryConfig) {
	m.store = cfg.Store
	m.embedding = cfg.Embedding
	m.memory = cfg.Memory
	m.crossThreadSearch = cfg.CrossThreadSearch
	m.semanticMinScore = cfg.SemanticMinScore
	m.maxHistory = cfg.MaxHistory
	m.maxTokens = cfg.MaxTokens
	m.autoTitle = cfg.AutoTitle
	m.provider = cfg.Provider
	m.semanticTrimming = cfg.SemanticTrimming
	m.trimmingEmbedding = cfg.TrimmingEmbedding
	m.keepRecent = cfg.KeepRecent
	m.tracer = cfg.Tracer
	if cfg.Logger != nil {
		m.logger = cfg.Logger
	}
}

// initSem lazily initializes the semaphore. Safe for concurrent callers.
// If sem was pre-set (e.g. in tests), the existing channel is preserved.
func (m *AgentMemory) initSem() {
	m.semOnce.Do(func() {
		if m.sem == nil {
			m.sem = make(chan struct{}, maxPersistGoroutines)
		}
	})
}

// initTrimCache lazily allocates the semantic-trim embedding cache.
// Only callers that actually run semantic trimming pay the allocation.
func (m *AgentMemory) initTrimCache() {
	m.trimCacheOnce.Do(func() {
		if m.trimCache == nil {
			m.trimCache = newEmbeddingCache(trimCacheCap)
		}
	})
}

// Close waits for all in-flight persist goroutines to finish and releases
// any resources held by the orchestrator. Called during agent/network
// shutdown to prevent data loss.
//
// Returns nil today. The error return is reserved for future flush errors
// (remote stores, network drains). Locking in the io.Closer-shaped signature
// now avoids a second breaking change later.
func (m *AgentMemory) Close() error {
	m.wg.Wait()
	return nil
}

// truncateStr truncates a string to at most n runes, preserving the original
// if it's shorter. Used to prevent unbounded growth in memory stores.
func truncateStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		return string(runes[:n])
	}
	return s
}
