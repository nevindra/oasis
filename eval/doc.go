// Package eval provides built-in scorers and a batch runner for evaluating
// agent output quality. Deterministic scorers (ExactMatch, KeywordCoverage,
// Trajectory, …) run with no I/O; LLM-judge scorers (Faithfulness, Toxicity,
// Rubric, …) wrap a core.Provider and satisfy core.AsyncScorer so the agent
// runtime runs them off the hot path. RunEvals drives a core.Agent over a
// dataset and aggregates per-scorer statistics for CI gating.
//
// All scorers implement core.Scorer and are attached to agents via
// agent.WithScorers (see the agent package). The types they consume and produce
// (core.Scorer, core.Score, core.ScorerRun) live in the core package.
package eval
