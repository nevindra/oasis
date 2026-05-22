// Package history contains the option API for an agent's conversation history,
// per-thread compaction, semantic trimming, and per-turn compression. Pass these
// options to oasis.WithHistory(...).
package history

import (
	"github.com/nevindra/oasis/core"
)

// Option configures conversation history on an agent. Pass values to
// oasis.WithHistory. Options are flat — combine as needed.
type Option func(*Config)

// Config is the resolved history configuration. Internal; agent.WithHistory
// constructs and consumes it. Exported only so the agent package can read it.
type Config struct {
	Store             core.Store
	MaxHistory        int
	MaxTokens         int // history token budget
	AutoTitle         bool
	CrossThreadSearch bool
	MinScore          float32
	Compactor         core.Compactor
	CompactThreshold  float64
	SemanticTrimming  bool
	TrimmingEmbedding core.EmbeddingProvider
	KeepRecent        int

	// Per-turn compression (no Store required).
	CompressModel     core.ModelFunc // function (ctx, task) → Provider; see core.ModelFunc
	CompressThreshold int            // rune count threshold; 0/negative = disabled
}

// Store sets the conversation memory store. Required to enable per-thread
// loading/persisting of conversation history. Per-turn compression works
// without a store.
func Store(s core.Store) Option {
	return func(c *Config) { c.Store = s }
}

// MaxHistory caps the number of recent messages loaded before each LLM call.
// Default: 10.
func MaxHistory(n int) Option {
	return func(c *Config) { c.MaxHistory = n }
}

// MaxTokens caps the loaded history by token budget (estimated). Composes
// with MaxHistory — whichever limit triggers first. Zero disables.
// This is the HISTORY token budget; output token limits live in oasis.Generation.
func MaxTokens(n int) Option {
	return func(c *Config) { c.MaxTokens = n }
}

// AutoTitle enables background generation of a short title from the first
// user message. Requires Store.
func AutoTitle() Option {
	return func(c *Config) { c.AutoTitle = true }
}

// CrossThreadSearch enables semantic recall across all conversation threads.
// Requires Store and the agent-level WithEmbedding option. Use MinScore to
// tune the similarity threshold (default 0.60).
func CrossThreadSearch() Option {
	return func(c *Config) { c.CrossThreadSearch = true }
}

// MinScore sets the minimum cosine similarity score for CrossThreadSearch.
// Default 0.60.
func MinScore(score float32) Option {
	return func(c *Config) { c.MinScore = score }
}

// Compaction wires a Compactor that runs when stored history exceeds
// threshold × effectiveWindow. Requires Store. threshold is 0.0–1.0;
// recommended 0.80. Passing nil compactor or threshold <= 0 is a no-op.
func Compaction(compactor core.Compactor, threshold float64) Option {
	return func(c *Config) {
		if compactor == nil || threshold <= 0 {
			return
		}
		c.Compactor = compactor
		c.CompactThreshold = threshold
	}
}

// SemanticTrim enables relevance-based history trimming. When loaded history
// exceeds MaxHistory/MaxTokens, older messages are scored by cosine similarity
// to the current query — lowest-scoring messages drop first. Requires Store.
// KeepRecent preserves the N most recent messages regardless of score
// (default 3). SemanticTrim takes its own embedding so a smaller/faster model
// can be used here while CrossThreadSearch and WithUserMemory share the
// agent-level embedding set by WithEmbedding.
func SemanticTrim(e core.EmbeddingProvider) Option {
	return func(c *Config) {
		c.SemanticTrimming = true
		c.TrimmingEmbedding = e
	}
}

// KeepRecent tunes how many recent messages SemanticTrim preserves regardless
// of relevance. Default 3.
func KeepRecent(n int) Option {
	return func(c *Config) { c.KeepRecent = n }
}

// Compress enables per-turn LLM-driven summarization when the in-memory
// message slice exceeds threshold runes. fn returns the model used for
// summarization (falls back to the agent's main provider if fn returns nil).
// threshold <= 0 disables. Does NOT require Store — works on the in-memory
// slice during a single Execute.
func Compress(fn core.ModelFunc, threshold int) Option {
	return func(c *Config) {
		c.CompressModel = fn
		c.CompressThreshold = threshold
	}
}

// Build resolves an Option slice into a Config.
func Build(opts []Option) Config {
	var c Config
	for _, o := range opts {
		o(&c)
	}
	return c
}
