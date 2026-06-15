package core

import (
	"context"
	"testing"
	"time"
)

// memScoreStore is an in-memory ScoreStore used to prove the interface is
// implementable and the filter semantics are usable.
type memScoreStore struct{ rows []ScoreRow }

func (m *memScoreStore) SaveScores(_ context.Context, rows []ScoreRow) error {
	m.rows = append(m.rows, rows...)
	return nil
}
func (m *memScoreStore) ListScores(_ context.Context, f ScoreFilter) ([]ScoreRow, error) {
	var out []ScoreRow
	for _, r := range m.rows {
		if f.ScorerID != "" && r.ScorerID != f.ScorerID {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}
func (m *memScoreStore) GetScore(_ context.Context, id string) (ScoreRow, error) {
	for _, r := range m.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return ScoreRow{}, context.Canceled // any sentinel for the test
}
func (m *memScoreStore) DeleteScores(_ context.Context, _ ScoreFilter) (int, error) {
	n := len(m.rows)
	m.rows = nil
	return n, nil
}

func TestScoreStoreInterface(t *testing.T) {
	var s ScoreStore = &memScoreStore{}
	row := ScoreRow{ID: "1", ScorerID: "faithfulness", Value: 0.9, Source: ScorerSourceLive, CreatedAt: time.Now()}
	if err := s.SaveScores(context.Background(), []ScoreRow{row}); err != nil {
		t.Fatalf("SaveScores: %v", err)
	}
	got, err := s.ListScores(context.Background(), ScoreFilter{ScorerID: "faithfulness"})
	if err != nil || len(got) != 1 || got[0].Value != 0.9 {
		t.Fatalf("ListScores returned %+v err=%v", got, err)
	}
}

func TestScoreSinkInterface(t *testing.T) {
	var sink ScoreSink = scoreSinkFunc(func(_ context.Context, _ ScoreRow) error { return nil })
	if err := sink.Emit(context.Background(), ScoreRow{}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
}

type scoreSinkFunc func(context.Context, ScoreRow) error

func (f scoreSinkFunc) Emit(ctx context.Context, r ScoreRow) error { return f(ctx, r) }
