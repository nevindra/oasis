package eval

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

// blockingAgent blocks on a channel until it is closed, simulating a slow agent.
// It reports started via the started channel when Execute is called.
type blockingAgent struct {
	started chan<- struct{}
	gate    <-chan struct{}
}

func (blockingAgent) Name() string        { return "blocking" }
func (blockingAgent) Description() string { return "blocks until gate closes" }
func (a blockingAgent) Execute(ctx context.Context, _ core.AgentTask, _ ...core.RunOption) (core.AgentResult, error) {
	// signal that we have started
	select {
	case a.started <- struct{}{}:
	default:
	}
	// block until context cancelled or gate opened
	select {
	case <-ctx.Done():
		return core.AgentResult{}, ctx.Err()
	case <-a.gate:
		return core.AgentResult{Output: "done"}, nil
	}
}

// TestRunEvalsCancellation verifies finding #1: cancelling the context during a
// batch causes RunEvals to return ctx.Err() promptly without deadlocking on the
// semaphore acquire loop.
func TestRunEvalsCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	gate := make(chan struct{})
	agent := blockingAgent{started: started, gate: gate}

	// Two items, concurrency=1: first item blocks, second would deadlock on the
	// semaphore if we don't honour ctx.Done().
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := RunEvals(ctx, RunEvalsConfig{
			Agent:       agent,
			Data:        []EvalItem{{Input: "a"}, {Input: "b"}},
			Concurrency: 1,
		})
		done <- err
	}()

	// Wait until the first item is executing, then cancel.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never started")
	}
	cancel()
	// Close gate so the running goroutine unblocks and drains cleanly.
	close(gate)

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunEvals returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunEvals did not return after context cancellation (possible deadlock)")
	}
}

// errScorer is a scorer that always returns an error.
type errScorer struct{ id string }

func (e errScorer) ID() string { return e.id }
func (e errScorer) Score(_ context.Context, _ core.ScorerRun) (core.Score, error) {
	return core.Score{}, errors.New("scorer intentionally failed")
}

// TestEvalOneScorerError verifies finding #2: a scorer error is recorded in
// EvalResult.ScorerErrors and that scorer is absent from EvalResult.Scores.
func TestEvalOneScorerError(t *testing.T) {
	const scorerID = "always_fails"
	cfg := RunEvalsConfig{
		Agent:   stubAgent{},
		Data:    []EvalItem{{Input: "hello", GroundTruth: "echo: hello"}},
		Scorers: []core.Scorer{errScorer{id: scorerID}},
	}
	rep, err := RunEvals(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunEvals: %v", err)
	}
	if rep.N != 1 {
		t.Fatalf("N = %d, want 1", rep.N)
	}

	// We need the raw EvalResult; run evalOne directly so we can inspect ScorerErrors.
	res := evalOne(context.Background(), cfg, cfg.Data[0])
	if res.ScorerErrors == nil {
		t.Fatal("ScorerErrors is nil; expected map with entry for always_fails")
	}
	if res.ScorerErrors[scorerID] == nil {
		t.Fatalf("ScorerErrors[%q] is nil; expected non-nil error", scorerID)
	}
	for _, s := range res.Scores {
		if s.ScorerID == scorerID {
			t.Fatalf("Scores contains entry for %q, but that scorer errored", scorerID)
		}
	}
}
