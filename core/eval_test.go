package core

import (
	"context"
	"testing"
)

// stubScorer verifies the Scorer interface is implementable and the value
// types compose as expected.
type stubScorer struct{ id string }

func (s stubScorer) ID() string { return s.id }
func (s stubScorer) Score(_ context.Context, run ScorerRun) (Score, error) {
	return Score{ScorerID: s.id, Value: 1, Reason: "ok"}, nil
}

func TestScorerInterfaceAndValueTypes(t *testing.T) {
	var sc Scorer = stubScorer{id: "stub"}
	run := ScorerRun{
		Input:  "q",
		Output: "a",
		Source: ScorerSourceLive,
		Steps:  []StepTrace{{Name: "tool", Type: StepTypeTool}},
		Expected: &ExpectedTrajectory{
			Steps:    []ExpectedStep{{Name: "tool"}},
			Strategy: OrderedSubset,
		},
	}
	got, err := sc.Score(context.Background(), run)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.ScorerID != "stub" || got.Value != 1 {
		t.Fatalf("unexpected score: %+v", got)
	}
}

func TestScoreModeAndSamplingZeroValues(t *testing.T) {
	if ScoreModeAuto != 0 {
		t.Fatalf("ScoreModeAuto must be the zero value (default)")
	}
	cfg := ScorerConfig{Scorer: stubScorer{id: "x"}}
	if cfg.Mode != ScoreModeAuto {
		t.Fatalf("zero-value ScorerConfig.Mode should be Auto")
	}
	if cfg.Sampling.Rate != 0 {
		t.Fatalf("zero-value Sampling.Rate should be 0 (interpreted as always-on by runtime)")
	}
}
