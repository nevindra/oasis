package core

import (
	"context"
	"encoding/json"
)

// ScorerSource distinguishes production runs from test-harness runs.
type ScorerSource string

const (
	// ScorerSourceLive marks scores produced from sampled production runs.
	ScorerSourceLive ScorerSource = "LIVE"
	// ScorerSourceTest marks scores produced by the batch/CI runner.
	ScorerSourceTest ScorerSource = "TEST"
)

// TrajectoryMatch selects how a trajectory scorer compares a run's actual tool
// sequence against the expected one.
type TrajectoryMatch uint8

const (
	// ExactMatch requires names (and args, when set) to match exactly, in order.
	ExactMatch TrajectoryMatch = iota
	// OrderedSubset requires expected steps to appear in order; extras allowed.
	OrderedSubset
	// UnorderedSubset requires expected steps to appear in any order; extras allowed.
	UnorderedSubset
	// LLMJudgeMatch defers comparison to a judge LLM (used by the llm trajectory scorer).
	LLMJudgeMatch
)

// ExpectedStep is one expected tool or agent invocation. Args nil means
// name-only match.
type ExpectedStep struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// ExpectedTrajectory describes the tool sequence a trajectory scorer compares against.
type ExpectedTrajectory struct {
	Steps    []ExpectedStep  `json:"steps"`
	Strategy TrajectoryMatch `json:"strategy"`
}

// ScorerRun is the read-only input to Scorer.Score. The runtime builds it by
// SHARING slices from AgentResult — Iterations and Steps are not copied — so
// constructing it is allocation-light. Scorers MUST treat every field as
// immutable.
type ScorerRun struct {
	RunID       string              // run identifier, when available
	Input       string              // the task / user message
	Output      string              // AgentResult.Output
	Thinking    string              // AgentResult.Thinking (optional)
	GroundTruth string              // expected answer (TEST mode / similarity scorers)
	Context     []string            // retrieved RAG context (faithfulness, context-* scorers)
	Iterations  []IterationTrace    // full trajectory — already on AgentResult
	Steps       []StepTrace         // flat tool-call list — already on AgentResult
	Expected    *ExpectedTrajectory // expected tool sequence for trajectory scorers (optional)
	Source      ScorerSource
}

// Score is the pure result of one Scorer.Score call. Scorer-specific structure
// rides in Details as typed JSON (same pattern as AgentResult.ProviderMeta);
// there is no map[string]any at the boundary. Value is normalized to 0.0–1.0.
type Score struct {
	ScorerID string          `json:"scorer_id"`
	Value    float64         `json:"value"`
	Reason   string          `json:"reason,omitempty"`
	Details  json.RawMessage `json:"details,omitempty"`
}

// Scorer is the single contract every scorer satisfies — deterministic,
// LLM-judge, and trajectory scorers alike. ID must be stable: it keys
// persistence, aggregation, and retrospective lookup.
//
// Score returns a Go error only for infrastructure failure (e.g. the judge
// provider is unreachable). A low-quality output is a low Value, never an error.
type Scorer interface {
	ID() string
	Score(ctx context.Context, run ScorerRun) (Score, error)
}

// AsyncScorer is an optional capability: scorers that prefer to run off the hot
// path (LLM judges) implement it returning true. Under ScoreModeAuto the runtime
// runs such scorers asynchronously and all others inline. Detected by type
// assertion — scorers that don't implement it default to inline.
type AsyncScorer interface {
	PrefersAsync() bool
}

// ScoreMode selects where a scorer runs relative to the agent's response.
type ScoreMode uint8

const (
	// ScoreModeAuto runs inline for plain scorers and async for AsyncScorer (default).
	ScoreModeAuto ScoreMode = iota
	// ScoreModeInline forces inline execution: it blocks the run until scored.
	ScoreModeInline
	// ScoreModeAsync forces background execution on the worker pool.
	ScoreModeAsync
)

// Sampling controls what fraction of LIVE runs a scorer sees. TEST runs always
// score (sampling ignored). Rate <= 0 is treated as always-on (1.0); to stop
// scoring, omit the scorer. Rate >= 1 always scores; 0 < Rate < 1 is probabilistic.
type Sampling struct {
	Rate float64
}

// ScorerConfig attaches one Scorer with execution + sampling policy.
type ScorerConfig struct {
	Scorer   Scorer
	Mode     ScoreMode
	Sampling Sampling
}
