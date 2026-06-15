package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/nevindra/oasis/core"
)

type inlineTestScorer struct{ id string }

func (s inlineTestScorer) ID() string { return s.id }
func (s inlineTestScorer) Score(_ context.Context, run core.ScorerRun) (core.Score, error) {
	// Length-based deterministic value, just to prove it ran on the real output.
	v := 0.0
	if run.Output != "" {
		v = 1.0
	}
	return core.Score{ScorerID: s.id, Value: v}, nil
}

type asyncTestScorer struct{ inlineTestScorer }

func (a asyncTestScorer) PrefersAsync() bool { return true }

// scoreRecordingStore is a ScoreStore that records persisted rows.
// Named differently from recordingStore (defined in memory_integration_test.go)
// to avoid a redeclaration error in the same package.
type scoreRecordingStore struct {
	mu   sync.Mutex
	rows []core.ScoreRow
}

func (s *scoreRecordingStore) SaveScores(_ context.Context, rows []core.ScoreRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, rows...)
	return nil
}
func (s *scoreRecordingStore) ListScores(context.Context, core.ScoreFilter) ([]core.ScoreRow, error) {
	return nil, nil
}
func (s *scoreRecordingStore) GetScore(context.Context, string) (core.ScoreRow, error) {
	return core.ScoreRow{}, nil
}
func (s *scoreRecordingStore) DeleteScores(context.Context, core.ScoreFilter) (int, error) {
	return 0, nil
}

func TestAgentInlineAndAsyncScoring(t *testing.T) {
	store := &scoreRecordingStore{}
	a := New("scored", "agent with scorers",
		&mockProvider{name: "m", responses: []core.ChatResponse{{Content: "the answer is 42"}}},
		WithScorers(
			ScorerConfig{Scorer: inlineTestScorer{id: "inline"}},
			ScorerConfig{Scorer: asyncTestScorer{inlineTestScorer{id: "async"}}},
		),
		WithScoreStore(store),
	)

	res, err := a.Execute(context.Background(), AgentTask{Input: "q"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Scores) != 1 || res.Scores[0].ScorerID != "inline" || res.Scores[0].Value != 1.0 {
		t.Fatalf("inline score missing/wrong: %+v", res.Scores)
	}

	// Close drains the async pool; the async score must be persisted by then.
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.rows) != 1 || store.rows[0].ScorerID != "async" {
		t.Fatalf("expected 1 persisted async score, got %+v", store.rows)
	}
	if store.rows[0].EntityID != "scored" || store.rows[0].EntityType != "agent" {
		t.Fatalf("async row identity wrong: %+v", store.rows[0])
	}
}
