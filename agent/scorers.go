package agent

import "github.com/nevindra/oasis/core"

// Re-exported scorer attachment types. They are defined in core (so
// internal/runtime can reference them without an agent↔runtime import cycle) and
// aliased here for discoverability alongside WithScorers.
type (
	// ScorerConfig attaches one scorer with execution + sampling policy.
	ScorerConfig = core.ScorerConfig
	// ScoreMode selects inline vs async execution.
	ScoreMode = core.ScoreMode
	// Sampling controls the fraction of LIVE runs a scorer sees.
	Sampling = core.Sampling
)

// ScoreMode values, re-exported for ergonomic use as agent.ScoreModeAuto, etc.
const (
	ScoreModeAuto   = core.ScoreModeAuto
	ScoreModeInline = core.ScoreModeInline
	ScoreModeAsync  = core.ScoreModeAsync
)

// WithScorers attaches scorers to the agent. Deterministic scorers run inline
// and attach to AgentResult.Scores; scorers implementing core.AsyncScorer (LLM
// judges) run in a bounded background pool that drains on Close. Configure
// persistence with WithScoreStore and external forwarding with WithScoreSink.
func WithScorers(scorers ...core.ScorerConfig) AgentOption {
	return func(c *Config) { c.Scorers = append(c.Scorers, scorers...) }
}

// WithScoreStore sets the store async scorer results are persisted to. The store
// must implement core.ScoreStore (SQLite and Postgres backends do).
func WithScoreStore(store core.ScoreStore) AgentOption {
	return func(c *Config) { c.ScoreStore = store }
}

// WithScoreSink forwards each persisted score to an external eval platform.
func WithScoreSink(sink core.ScoreSink) AgentOption {
	return func(c *Config) { c.ScoreSink = sink }
}
