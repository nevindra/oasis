// memory/options.go
package memory

import (
	"log/slog"

	"github.com/nevindra/oasis/core"
)

// Option configures an AgentMemoryConfig.
type Option func(*AgentMemoryConfig)

// WithStore binds the conversation store. Implement core.MemoryItemStore as
// well to enable memory-item operations (recall, remember, forget, etc.).
func WithStore(s core.Store) Option { return func(c *AgentMemoryConfig) { c.Store = s } }

// WithEmbedding sets the embedding provider used for recall + dedupe + working memory.
func WithEmbedding(p core.EmbeddingProvider) Option {
	return func(c *AgentMemoryConfig) { c.Embedding = p }
}

// WithProvider sets the LLM provider used for extraction and title generation.
func WithProvider(p core.Provider) Option {
	return func(c *AgentMemoryConfig) { c.Provider = p }
}

// HistoryConfig groups settings for history loading and trimming.
type HistoryConfig struct {
	MaxMessages  int                    // max messages to load (default 10)
	MaxTokens    int                    // token budget; 0 = no cap
	Semantic     bool                   // use semantic-similarity trimming when over budget
	TrimEmbedder core.EmbeddingProvider // nil = use main embedder
	KeepRecent   int                    // messages to keep regardless of relevance (default 3 when Semantic=true)

	// ReplayToolCalls expands persisted step traces back into
	// tool_call/tool_result message pairs when history is replayed, so the
	// model keeps seeing WHAT ran in earlier turns (and the results) instead
	// of only the final answer text. Off by default: plain-text history is
	// the cheapest, most trim-tolerant shape.
	ReplayToolCalls bool
	// ReplayVerbatimTurns is how many of the most recent assistant turns
	// replay their full tool outputs (RawOutput). Older turns replay the
	// bounded display digest (≤500 chars per step) so long threads don't
	// drag every historical tool payload forever. Default 2 when
	// ReplayToolCalls is set.
	ReplayVerbatimTurns int
	// ProtectedTools always replay their full output regardless of turn age
	// — for tools whose output IS durable instruction state (e.g. a skill
	// activation body that must steer the whole thread).
	ProtectedTools []string
}

// WithHistory configures history loading and trimming from a single HistoryConfig.
func WithHistory(cfg HistoryConfig) Option {
	return func(c *AgentMemoryConfig) {
		if cfg.MaxMessages > 0 {
			c.MaxHistory = cfg.MaxMessages
		}
		if cfg.MaxTokens > 0 {
			c.MaxTokens = cfg.MaxTokens
		}
		c.SemanticTrimming = cfg.Semantic
		c.TrimmingEmbedding = cfg.TrimEmbedder
		if cfg.KeepRecent > 0 {
			c.KeepRecent = cfg.KeepRecent
		}
		c.ReplayToolCalls = cfg.ReplayToolCalls
		if cfg.ReplayVerbatimTurns > 0 {
			c.ReplayVerbatimTurns = cfg.ReplayVerbatimTurns
		}
		c.ProtectedTools = cfg.ProtectedTools
	}
}

// WithCompaction wires a Compactor that runs when stored history exceeds
// threshold × effectiveWindow. Threshold is 0.0–1.0; recommended 0.80. Passing
// nil compactor or threshold <= 0 is a no-op. Requires a Store.
func WithCompaction(c core.Compactor, threshold float64) Option {
	return func(cfg *AgentMemoryConfig) {
		if c == nil || threshold <= 0 {
			return
		}
		cfg.Compactor = c
		cfg.CompactThreshold = threshold
	}
}

// WithCompress enables per-turn LLM-driven summarization when the in-memory
// message slice exceeds threshold runes. fn returns the model used for
// summarization (falls back to the agent's main provider if fn returns nil).
// threshold <= 0 disables. Does NOT require a Store — works on the in-memory
// slice during a single Execute.
func WithCompress(fn core.ModelFunc, threshold int) Option {
	return func(c *AgentMemoryConfig) {
		c.CompressModel = fn
		c.CompressThreshold = threshold
	}
}

// WithSemanticRecall enables cross-thread message recall (today's CrossThreadSearch).
func WithSemanticRecall() Option { return func(c *AgentMemoryConfig) { c.SemanticRecall = true } }

// WithSemanticRecallMinScore sets the cosine threshold for cross-thread recall.
func WithSemanticRecallMinScore(s float32) Option {
	return func(c *AgentMemoryConfig) { c.SemanticMinScore = s }
}

// WithRecallKinds configures which MemoryItem kinds are included in BatchedRecall.
// Defaults to [KindFact] when not set.
func WithRecallKinds(kinds ...core.MemoryKind) Option {
	return func(c *AgentMemoryConfig) { c.RecallKinds = append([]core.MemoryKind{}, kinds...) }
}

// WithRecallTopK sets the total top-K for BatchedRecall (default 8).
func WithRecallTopK(k int) Option { return func(c *AgentMemoryConfig) { c.RecallTopK = k } }

// WithWorkingMemory enables a markdown working-memory slot at the configured scope.
func WithWorkingMemory() Option {
	return func(c *AgentMemoryConfig) {
		c.WorkingMemory = true
		if c.WorkingMemoryScope == "" {
			c.WorkingMemoryScope = ScopeResource
		}
	}
}

// WithWorkingMemoryScope overrides the default Resource scope for working memory.
func WithWorkingMemoryScope(s core.MemoryScopeKind) Option {
	return func(c *AgentMemoryConfig) { c.WorkingMemoryScope = s }
}

// WithAutoTitle enables LLM-driven thread title generation on the first turn.
func WithAutoTitle() Option { return func(c *AgentMemoryConfig) { c.AutoTitle = true } }

// WithTools registers agent-callable memory tools. Default OFF; pass
// the tools you want — typically constructed from an AgentMemory like:
//
//	var m memory.AgentMemory
//	oasis.WithMemory(memory.WithTools(m.AllTools()...), ...)
//
// In practice the agent layer wires this for you when the option chain
// includes WithTools — the tools are stored in AgentMemoryConfig.Tools
// and registered with the LLMAgent during oasis.WithMemory application.
func WithTools(tools ...core.AnyTool) Option {
	return func(c *AgentMemoryConfig) { c.Tools = append(c.Tools, tools...) }
}

// WithIngestProcessors appends user-supplied processors after the default chain.
func WithIngestProcessors(ps ...IngestProcessor) Option {
	return func(c *AgentMemoryConfig) { c.IngestProcs = append(c.IngestProcs, ps...) }
}

// WithRetrieveProcessors appends user-supplied processors after the default chain.
func WithRetrieveProcessors(ps ...RetrieveProcessor) Option {
	return func(c *AgentMemoryConfig) { c.RetrieveProcs = append(c.RetrieveProcs, ps...) }
}

// WithLogger sets the slog logger.
func WithLogger(l *slog.Logger) Option { return func(c *AgentMemoryConfig) { c.Logger = l } }

// WithTracer sets the OpenTelemetry tracer.
func WithTracer(t core.Tracer) Option { return func(c *AgentMemoryConfig) { c.Tracer = t } }

// BuildConfig applies the options and returns the resulting config.
func BuildConfig(opts ...Option) AgentMemoryConfig {
	var cfg AgentMemoryConfig
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}
