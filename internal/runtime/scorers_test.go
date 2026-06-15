package runtime

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

type fakeScorer struct {
	id    string
	async bool
	value float64
}

func (f fakeScorer) ID() string { return f.id }
func (f fakeScorer) Score(_ context.Context, _ core.ScorerRun) (core.Score, error) {
	return core.Score{ScorerID: f.id, Value: f.value}, nil
}

type asyncScorer struct{ fakeScorer }

func (a asyncScorer) PrefersAsync() bool { return true }

type fakeStore struct {
	mu   sync.Mutex
	rows []core.ScoreRow
}

func (s *fakeStore) SaveScores(_ context.Context, rows []core.ScoreRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, rows...)
	return nil
}
func (s *fakeStore) ListScores(context.Context, core.ScoreFilter) ([]core.ScoreRow, error) {
	return nil, nil
}
func (s *fakeStore) GetScore(context.Context, string) (core.ScoreRow, error) {
	return core.ScoreRow{}, nil
}
func (s *fakeStore) DeleteScores(context.Context, core.ScoreFilter) (int, error) { return 0, nil }
func (s *fakeStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

func TestScorerIsAsyncResolution(t *testing.T) {
	cases := []struct {
		name string
		cfg  core.ScorerConfig
		want bool
	}{
		{"auto-plain", core.ScorerConfig{Scorer: fakeScorer{id: "a"}}, false},
		{"auto-async", core.ScorerConfig{Scorer: asyncScorer{fakeScorer{id: "b", async: true}}}, true},
		{"force-inline", core.ScorerConfig{Scorer: asyncScorer{fakeScorer{id: "c"}}, Mode: core.ScoreModeInline}, false},
		{"force-async", core.ScorerConfig{Scorer: fakeScorer{id: "d"}, Mode: core.ScoreModeAsync}, true},
	}
	for _, tc := range cases {
		if got := scorerIsAsync(tc.cfg); got != tc.want {
			t.Errorf("%s: scorerIsAsync = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSampleHit(t *testing.T) {
	if !sampleHit(0) {
		t.Fatal("Rate 0 must be treated as always-on")
	}
	if !sampleHit(1.5) {
		t.Fatal("Rate >= 1 must always hit")
	}
	// 0 < rate < 1 is probabilistic; just assert it does not panic.
	_ = sampleHit(0.5)
}

func TestRunScorersInlineAndAsync(t *testing.T) {
	store := &fakeStore{}
	rt := &Runtime{}
	rt.name = "agent1"
	rt.Scorers = []core.ScorerConfig{
		{Scorer: fakeScorer{id: "inline", value: 0.7}},
		{Scorer: asyncScorer{fakeScorer{id: "async", value: 0.3}}},
	}
	rt.ScoreStore = store
	rt.scorePool = newScorerPool(2, 16, store, nil, nil)

	res := rt.runScorers(context.Background(), "the question", core.AgentResult{Output: "the answer"})

	// Inline score attaches immediately.
	if len(res.Scores) != 1 || res.Scores[0].ScorerID != "inline" {
		t.Fatalf("expected one inline score, got %+v", res.Scores)
	}

	// Async score lands after the pool drains.
	rt.scorePool.close()
	if store.count() != 1 {
		t.Fatalf("expected 1 persisted async score, got %d", store.count())
	}
	if store.rows[0].EntityID != "agent1" || store.rows[0].EntityType != "agent" {
		t.Fatalf("async row identity wrong: %+v", store.rows[0])
	}
}

func TestScorerPoolDropsWhenFull(t *testing.T) {
	// Buffer of 0 + a blocking worker forces overflow on the second submit.
	block := make(chan struct{})
	p := &scorerPool{jobs: make(chan scoreJob), logger: discardLoggerScorers()}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for job := range p.jobs {
			<-block // hold the only worker
			_ = job
		}
	}()

	p.submit(scoreJob{scorer: fakeScorer{id: "x"}}) // taken by the worker, which then blocks
	// Give the worker a moment to pick up the first job.
	time.Sleep(10 * time.Millisecond)
	p.submit(scoreJob{scorer: fakeScorer{id: "y"}}) // no room → dropped

	if p.dropped.Load() == 0 {
		t.Fatal("expected at least one dropped job")
	}
	close(block)
	close(p.jobs)
	p.wg.Wait()
}

func discardLoggerScorers() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
