package eval

import (
	"context"
	"sync"
	"testing"

	"github.com/nevindra/oasis/core"
)

// stubAgent implements core.Agent: it echoes the task input as the output.
type stubAgent struct {
	err error
}

func (stubAgent) Name() string        { return "stub" }
func (stubAgent) Description() string { return "stub agent" }
func (a stubAgent) Execute(_ context.Context, task core.AgentTask, _ ...core.RunOption) (core.AgentResult, error) {
	if a.err != nil {
		return core.AgentResult{}, a.err
	}
	return core.AgentResult{Output: "echo: " + task.Input}, nil
}

func TestRunEvalsAggregates(t *testing.T) {
	cfg := RunEvalsConfig{
		Agent: stubAgent{},
		Data: []EvalItem{
			{Input: "alpha beta", GroundTruth: "echo: alpha beta"}, // exact match → 1
			{Input: "gamma", GroundTruth: "wrong"},                 // exact match → 0
		},
		Scorers: []core.Scorer{ExactMatch(), KeywordCoverage("echo")},
	}
	rep, err := RunEvals(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunEvals: %v", err)
	}
	if rep.N != 2 || rep.Failed != 0 {
		t.Fatalf("N=%d Failed=%d", rep.N, rep.Failed)
	}
	if rep.Mean["exact_match"] != 0.5 {
		t.Fatalf("exact_match mean = %v, want 0.5", rep.Mean["exact_match"])
	}
	if rep.Mean["keyword_coverage"] != 1.0 {
		t.Fatalf("keyword_coverage mean = %v, want 1.0", rep.Mean["keyword_coverage"])
	}
	if rep.Min["exact_match"] != 0 || rep.Max["exact_match"] != 1 {
		t.Fatalf("exact_match min/max = %v/%v", rep.Min["exact_match"], rep.Max["exact_match"])
	}
}

func TestRunEvalsOnItemAndErrors(t *testing.T) {
	var mu sync.Mutex
	seen := 0
	cfg := RunEvalsConfig{
		Agent:       stubAgent{err: context.DeadlineExceeded},
		Data:        []EvalItem{{Input: "x"}, {Input: "y"}},
		Scorers:     []core.Scorer{ExactMatch()},
		Concurrency: 1,
		OnItem: func(EvalResult) {
			mu.Lock()
			seen++
			mu.Unlock()
		},
	}
	rep, err := RunEvals(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunEvals should not return agent errors: %v", err)
	}
	if rep.Failed != 2 {
		t.Fatalf("Failed = %d, want 2", rep.Failed)
	}
	if seen != 2 {
		t.Fatalf("OnItem called %d times, want 2", seen)
	}
}
