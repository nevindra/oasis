// memory/memory.go
// Package memory provides the unified MemoryItem-based memory system for
// Oasis LLMAgents. See doc.go for the model overview.
package memory

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nevindra/oasis/core"
)

// maxIngestGoroutines caps concurrent background ingestion goroutines.
// Provides backpressure when the store or embedding provider is slow.
const maxIngestGoroutines = 16

// AgentMemory orchestrates memory I/O for an LLMAgent. All fields are
// optional — a zero AgentMemory with no Store does nothing.
type AgentMemory struct {
	store     core.Store           // conversation history (threads, messages)
	itemStore core.MemoryItemStore // memory items; non-nil only when store also implements it
	embedding core.EmbeddingProvider
	provider  core.Provider // for LLM-driven processors (extraction, titling)

	// Pipeline configuration
	ingestProcs   []IngestProcessor   // appended after defaults
	retrieveProcs []RetrieveProcessor // appended after defaults

	// History knobs
	maxHistory        int
	maxTokens         int
	semanticTrimming  bool
	trimmingEmbedding core.EmbeddingProvider
	keepRecent        int

	// Tool-exchange replay knobs (see HistoryConfig).
	replayToolCalls     bool
	replayVerbatimTurns int
	protectedTools      []string

	// Recall knobs
	semanticRecall   bool
	semanticMinScore float32
	recallKinds      []core.MemoryKind
	recallTopK       int

	// Working memory
	workingMemory      bool
	workingMemoryScope core.MemoryScopeKind

	// Lifecycle
	autoTitle bool

	// Compaction (history-shrink). Trigger lives in the agent loop; these
	// fields are mirrored here so processors / callers can introspect them.
	compactor        core.Compactor
	compactThreshold float64

	// Per-turn compression (in-memory summarization).
	compressModel     core.ModelFunc
	compressThreshold int

	// Agent-callable tools (registered by oasis.WithMemory when WithTools is used)
	tools []core.AnyTool

	// Observability
	logger *slog.Logger
	tracer core.Tracer

	// Cached processor chains (built once at Init, reused per call)
	cachedRetrieveChain []RetrieveProcessor
	cachedIngestChain   []IngestProcessor

	// Background goroutine discipline
	semOnce       sync.Once
	sem           chan struct{}
	wg            sync.WaitGroup
	trimCacheOnce sync.Once
	trimCache     *embeddingCache
}

// AgentMemoryConfig holds the fields used to populate an AgentMemory.
// All fields are optional; zero values mean "use default" or "disabled".
type AgentMemoryConfig struct {
	Store     core.Store
	Embedding core.EmbeddingProvider
	Provider  core.Provider

	IngestProcs   []IngestProcessor
	RetrieveProcs []RetrieveProcessor

	MaxHistory       int
	MaxTokens        int
	SemanticTrimming bool

	// TrimmingEmbedding optionally overrides Embedding for semantic history
	// trimming so a smaller/faster model can be used than for cross-thread recall.
	TrimmingEmbedding core.EmbeddingProvider
	// KeepRecent is how many recent messages SemanticTrim preserves regardless
	// of relevance. Default 3 when SemanticTrimming is enabled.
	KeepRecent int

	// ReplayToolCalls / ReplayVerbatimTurns / ProtectedTools control replay
	// of persisted tool exchanges into history — see HistoryConfig.
	ReplayToolCalls     bool
	ReplayVerbatimTurns int
	ProtectedTools      []string

	SemanticRecall   bool
	SemanticMinScore float32
	RecallKinds      []core.MemoryKind
	RecallTopK       int

	WorkingMemory      bool
	WorkingMemoryScope core.MemoryScopeKind

	AutoTitle bool

	// Compaction: when stored history exceeds CompactThreshold × window,
	// the trigger (in the agent loop) calls Compactor.Compact. The trigger
	// stays framework-level; policy lives in the Compactor implementation.
	Compactor        core.Compactor
	CompactThreshold float64

	// Per-turn compression: when in-memory message slice exceeds
	// CompressThreshold runes, summarize via CompressModel. Works without a
	// Store — operates on the in-memory slice during a single Execute.
	CompressModel     core.ModelFunc
	CompressThreshold int

	// Tools are agent-callable memory tools to register when WithMemory
	// is applied to an LLMAgent. Populated by memory.WithTools(...). Default
	// is nil (no tools registered).
	Tools []core.AnyTool

	Logger *slog.Logger
	Tracer core.Tracer
}

// Init populates the AgentMemory from the given config. Call once before use.
func (m *AgentMemory) Init(cfg AgentMemoryConfig) {
	m.store = cfg.Store
	if is, ok := cfg.Store.(core.MemoryItemStore); ok {
		m.itemStore = is
	}
	m.embedding = cfg.Embedding
	m.provider = cfg.Provider
	m.ingestProcs = cfg.IngestProcs
	m.retrieveProcs = cfg.RetrieveProcs
	m.maxHistory = cfg.MaxHistory
	m.maxTokens = cfg.MaxTokens
	m.semanticTrimming = cfg.SemanticTrimming
	m.trimmingEmbedding = cfg.TrimmingEmbedding
	m.keepRecent = cfg.KeepRecent
	m.replayToolCalls = cfg.ReplayToolCalls
	m.replayVerbatimTurns = cfg.ReplayVerbatimTurns
	if m.replayToolCalls && m.replayVerbatimTurns <= 0 {
		m.replayVerbatimTurns = 2
	}
	m.protectedTools = cfg.ProtectedTools
	m.semanticRecall = cfg.SemanticRecall
	m.semanticMinScore = cfg.SemanticMinScore
	m.recallKinds = cfg.RecallKinds
	m.recallTopK = cfg.RecallTopK
	m.workingMemory = cfg.WorkingMemory
	m.workingMemoryScope = cfg.WorkingMemoryScope
	m.autoTitle = cfg.AutoTitle
	m.compactor = cfg.Compactor
	m.compactThreshold = cfg.CompactThreshold
	m.compressModel = cfg.CompressModel
	m.compressThreshold = cfg.CompressThreshold
	m.tools = cfg.Tools
	if cfg.Logger != nil {
		m.logger = cfg.Logger
	} else {
		m.logger = slog.New(slog.DiscardHandler)
	}
	m.tracer = cfg.Tracer

	m.cachedRetrieveChain = m.defaultRetrieveChain()
	m.cachedIngestChain = m.defaultIngestChain()
}

// initSem lazily initializes the ingest semaphore.
func (m *AgentMemory) initSem() {
	m.semOnce.Do(func() {
		if m.sem == nil {
			m.sem = make(chan struct{}, maxIngestGoroutines)
		}
	})
}

// initTrimCache lazily allocates the semantic-trim embedding cache.
func (m *AgentMemory) initTrimCache() {
	m.trimCacheOnce.Do(func() {
		if m.trimCache == nil {
			m.trimCache = newEmbeddingCache(trimCacheCap)
		}
	})
}

// Close waits for all background ingestion goroutines to finish.
// Reserved error return for future flush errors (remote stores).
func (m *AgentMemory) Close() error {
	m.wg.Wait()
	return nil
}

// truncateStr truncates s to at most n runes.
func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// Ensure context is imported (used by future methods added in later tasks).
var _ = context.Background

const persistBackpressureTimeout = 30 * time.Second
const persistTimeout = 30 * time.Second

// defaultIngestChain returns the default ingest pipeline. Order matters:
// EnsureThread first so subsequent processors see the row; PersistMessages
// before any LLM call so messages are durable even if extraction fails;
// FactExtractor/Deduper before Embedder so candidates carrying supersedes
// can resolve first; Upserter terminal; TitleGenerator and DecayProbabilistic
// last (don't block anyone else).
func (m *AgentMemory) defaultIngestChain() []IngestProcessor {
	chain := []IngestProcessor{
		EnsureThread{},
		PersistMessages{},
	}
	if m.provider != nil {
		chain = append(chain, FactExtractor{})
	}
	if m.embedding != nil {
		chain = append(chain, Deduper{}, Embedder{})
	}
	chain = append(chain, Upserter{})
	if m.autoTitle && m.provider != nil {
		chain = append(chain, TitleGenerator{})
	}
	chain = append(chain, DecayProbabilistic{})
	chain = append(chain, m.ingestProcs...) // user-appended processors run last
	return chain
}

// PersistTurn runs the ingest pipeline for a completed agent turn in the
// background. Bounded by maxIngestGoroutines. Falls back to lightweight
// persist (messages only, no LLM) when all slots are busy.
func (m *AgentMemory) PersistTurn(ctx context.Context, agentName string, task core.AgentTask, userText, asstText string, steps []core.StepTrace) {
	if m.store == nil || task.ThreadID == "" {
		return
	}
	m.initSem()

	fullPersist := true
	select {
	case m.sem <- struct{}{}:
	default:
		m.logger.Warn("ingest backpressure: lightweight persist", "thread_id", task.ThreadID)
		fullPersist = false
		t := time.NewTimer(persistBackpressureTimeout)
		select {
		case m.sem <- struct{}{}:
			t.Stop()
		case <-t.C:
			m.logger.Error("ingest backpressure: dropping turn", "thread_id", task.ThreadID)
			return
		}
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() { <-m.sem }()

		bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
		defer cancel()

		if m.tracer != nil {
			var span core.Span
			bgCtx, span = m.tracer.Start(bgCtx, "agent.memory.ingest",
				core.StringAttr("thread_id", task.ThreadID))
			defer span.End()
		}

		in := &IngestContext{
			AgentName: agentName,
			Task:      task,
			UserText:  userText,
			AsstText:  asstText,
			Steps:     steps,
			Store:     m.store,
			ItemStore: m.itemStore,
			Embedding: m.embedding,
			Provider:  m.provider,
			Logger:    m.logger,
		}

		chain := m.cachedIngestChain
		if !fullPersist {
			chain = []IngestProcessor{EnsureThread{}, PersistMessages{}}
		}
		if err := runIngestPipeline(bgCtx, in, chain); err != nil {
			m.logger.Error("ingest pipeline error", "error", err)
		}
	}()
}

// Remember persists a single MemoryItem. Defaults applied:
//   - ID: core.NewID() if empty
//   - Scope: from scopeForKind(item.Kind) when zero
//   - Source: {Kind: "user", AgentID: <unset>} when zero
//   - Embedding: backfilled if EmbeddingProvider is set
func (m *AgentMemory) Remember(ctx context.Context, item core.MemoryItem) error {
	if m.store == nil {
		return errors.New("memory: no store configured")
	}
	if m.itemStore == nil {
		return errors.New("memory: this operation requires a store implementing core.MemoryItemStore")
	}
	if item.ID == "" {
		item.ID = core.NewID()
	}
	if item.Scope.Kind == "" {
		item.Scope = Scoped(ScopeResource, "") // caller-supplied or empty fallback
	}
	if item.Source.Kind == "" {
		item.Source.Kind = "user"
	}
	if item.CreatedAt == 0 {
		item.CreatedAt = core.NowUnix()
	}
	if len(item.Embedding) == 0 && m.embedding != nil && item.Content != "" {
		if embs, err := m.embedding.Embed(ctx, []string{item.Content}); err == nil && len(embs) > 0 {
			item.Embedding = embs[0]
		}
	}
	return m.itemStore.Upsert(ctx, item)
}

// RecallOption configures Recall.
type RecallOption func(*recallCfg)
type recallCfg struct {
	kinds []core.MemoryKind
	scope *core.MemoryScope
	limit int
}

func RecallKind(k core.MemoryKind) RecallOption {
	return func(c *recallCfg) { c.kinds = append(c.kinds, k) }
}
func RecallScope(s core.MemoryScope) RecallOption { return func(c *recallCfg) { c.scope = &s } }
func RecallLimit(n int) RecallOption              { return func(c *recallCfg) { c.limit = n } }

// Recall returns items semantically similar to query.
func (m *AgentMemory) Recall(ctx context.Context, query string, opts ...RecallOption) ([]core.ScoredMemoryItem, error) {
	if m.store == nil {
		return nil, errors.New("memory: no store configured")
	}
	if m.itemStore == nil {
		return nil, errors.New("memory: this operation requires a store implementing core.MemoryItemStore")
	}
	if m.embedding == nil {
		return nil, errors.New("memory: no embedding configured")
	}
	cfg := recallCfg{limit: 5}
	for _, o := range opts {
		o(&cfg)
	}
	embs, err := m.embedding.Embed(ctx, []string{query})
	if err != nil || len(embs) == 0 {
		return nil, err
	}
	return m.itemStore.SearchSemantic(ctx, embs[0], core.MemoryFilter{
		Kinds: cfg.kinds, Scope: cfg.scope,
	}, cfg.limit)
}

// ForgetSpec describes what to delete.
type ForgetSpec struct {
	ID     string
	Match  string
	Kind   core.MemoryKind
	Older  time.Duration
	Filter *core.MemoryFilter // power-user: pass a full Filter directly
}

func ForgetByID(id string) ForgetSpec            { return ForgetSpec{ID: id} }
func ForgetByMatch(s string) ForgetSpec          { return ForgetSpec{Match: s} }
func ForgetByKind(k core.MemoryKind) ForgetSpec  { return ForgetSpec{Kind: k} }
func ForgetOlderThan(d time.Duration) ForgetSpec { return ForgetSpec{Older: d} }

// Forget deletes items matching the spec. Returns count deleted.
func (m *AgentMemory) Forget(ctx context.Context, spec ForgetSpec) (int, error) {
	if m.store == nil {
		return 0, errors.New("memory: no store configured")
	}
	if m.itemStore == nil {
		return 0, errors.New("memory: this operation requires a store implementing core.MemoryItemStore")
	}
	if spec.ID != "" {
		err := m.itemStore.Delete(ctx, spec.ID)
		if err != nil {
			return 0, err
		}
		return 1, nil
	}
	f := core.MemoryFilter{}
	if spec.Filter != nil {
		f = *spec.Filter
	}
	if spec.Kind != "" {
		f.Kinds = []core.MemoryKind{spec.Kind}
	}
	if spec.Older > 0 {
		f.Until = core.NowUnix() - int64(spec.Older.Seconds())
	}
	if spec.Match != "" {
		items, err := m.itemStore.List(ctx, f)
		if err != nil {
			return 0, err
		}
		n := 0
		for _, it := range items {
			if strings.Contains(strings.ToLower(it.Content), strings.ToLower(spec.Match)) {
				if err := m.itemStore.Delete(ctx, it.ID); err == nil {
					n++
				}
			}
		}
		return n, nil
	}
	return m.itemStore.DeleteWhere(ctx, f)
}

// List returns items matching the filter.
func (m *AgentMemory) List(ctx context.Context, f core.MemoryFilter) ([]core.MemoryItem, error) {
	if m.store == nil {
		return nil, errors.New("memory: no store configured")
	}
	if m.itemStore == nil {
		return nil, errors.New("memory: this operation requires a store implementing core.MemoryItemStore")
	}
	return m.itemStore.List(ctx, f)
}

// Get fetches one item by ID.
func (m *AgentMemory) Get(ctx context.Context, id string) (core.MemoryItem, error) {
	if m.store == nil {
		return core.MemoryItem{}, errors.New("memory: no store configured")
	}
	if m.itemStore == nil {
		return core.MemoryItem{}, errors.New("memory: this operation requires a store implementing core.MemoryItemStore")
	}
	return m.itemStore.Get(ctx, id)
}

// Pin sets or clears the pinned flag.
func (m *AgentMemory) Pin(ctx context.Context, id string, pinned bool) error {
	if m.store == nil {
		return errors.New("memory: no store configured")
	}
	if m.itemStore == nil {
		return errors.New("memory: this operation requires a store implementing core.MemoryItemStore")
	}
	it, err := m.itemStore.Get(ctx, id)
	if err != nil {
		return err
	}
	it.Pinned = pinned
	it.UpdatedAt = core.NowUnix()
	return m.itemStore.Upsert(ctx, it)
}
