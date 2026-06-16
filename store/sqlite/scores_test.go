package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	oasis "github.com/nevindra/oasis/core"
)

func TestSQLiteScoreStoreRoundTrip(t *testing.T) {
	s := New(":memory:")
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	var store oasis.ScoreStore = s // compile-time capability check
	ctx := context.Background()

	rows := []oasis.ScoreRow{
		{ID: "1", ScorerID: "faithfulness", RunID: "r1", EntityID: "agent1", EntityType: "agent",
			Input: "q", Output: "a", Value: 0.9, Reason: "grounded",
			Details: json.RawMessage(`{"k":1}`), Source: oasis.ScorerSourceLive, CreatedAt: time.Now()},
		{ID: "2", ScorerID: "toxicity", RunID: "r1", EntityID: "agent1", EntityType: "agent",
			Value: 0.1, Source: oasis.ScorerSourceTest, CreatedAt: time.Now()},
	}
	if err := store.SaveScores(ctx, rows); err != nil {
		t.Fatalf("SaveScores: %v", err)
	}

	got, err := store.ListScores(ctx, oasis.ScoreFilter{ScorerID: "faithfulness"})
	if err != nil {
		t.Fatalf("ListScores: %v", err)
	}
	if len(got) != 1 || got[0].Value != 0.9 || string(got[0].Details) != `{"k":1}` {
		t.Fatalf("ListScores wrong: %+v", got)
	}

	one, err := store.GetScore(ctx, "2")
	if err != nil || one.ScorerID != "toxicity" {
		t.Fatalf("GetScore wrong: %+v err=%v", one, err)
	}

	n, err := store.DeleteScores(ctx, oasis.ScoreFilter{EntityID: "agent1"})
	if err != nil || n != 2 {
		t.Fatalf("DeleteScores n=%d err=%v", n, err)
	}
}

// TestSQLiteGetScoreNotFound proves GetScore reports core.ErrNotFound (not the
// leaked sql.ErrNoRows) when no row has the requested id.
func TestSQLiteGetScoreNotFound(t *testing.T) {
	s := New(":memory:")
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer s.Close()

	_, err := s.GetScore(context.Background(), "missing")
	if !errors.Is(err, oasis.ErrNotFound) {
		t.Fatalf("GetScore missing: want ErrNotFound, got %v", err)
	}
}

// TestSQLiteListScoresRunIDFilter proves ScoreFilter.RunID scopes ListScores to
// rows with the matching run_id (the field was previously ignored).
func TestSQLiteListScoresRunIDFilter(t *testing.T) {
	s := New(":memory:")
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	rows := []oasis.ScoreRow{
		{ID: "1", ScorerID: "f", RunID: "r1", EntityID: "a", EntityType: "agent", CreatedAt: time.Now()},
		{ID: "2", ScorerID: "f", RunID: "r2", EntityID: "a", EntityType: "agent", CreatedAt: time.Now()},
		{ID: "3", ScorerID: "f", RunID: "r1", EntityID: "a", EntityType: "agent", CreatedAt: time.Now()},
	}
	if err := s.SaveScores(ctx, rows); err != nil {
		t.Fatalf("SaveScores: %v", err)
	}

	got, err := s.ListScores(ctx, oasis.ScoreFilter{RunID: "r1"})
	if err != nil {
		t.Fatalf("ListScores: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListScores RunID=r1: want 2 rows, got %d (%+v)", len(got), got)
	}
	for _, r := range got {
		if r.RunID != "r1" {
			t.Fatalf("ListScores returned row with run_id=%q, want r1", r.RunID)
		}
	}
}

// TestSQLiteDeleteScoresRunIDFilter proves ScoreFilter.RunID scopes DeleteScores
// to rows with the matching run_id (the field was previously ignored).
func TestSQLiteDeleteScoresRunIDFilter(t *testing.T) {
	s := New(":memory:")
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	rows := []oasis.ScoreRow{
		{ID: "1", RunID: "r1", EntityID: "a", CreatedAt: time.Now()},
		{ID: "2", RunID: "r2", EntityID: "a", CreatedAt: time.Now()},
		{ID: "3", RunID: "r1", EntityID: "a", CreatedAt: time.Now()},
	}
	if err := s.SaveScores(ctx, rows); err != nil {
		t.Fatalf("SaveScores: %v", err)
	}

	n, err := s.DeleteScores(ctx, oasis.ScoreFilter{RunID: "r1"})
	if err != nil {
		t.Fatalf("DeleteScores: %v", err)
	}
	if n != 2 {
		t.Fatalf("DeleteScores RunID=r1: want 2 deleted, got %d", n)
	}
	remaining, err := s.ListScores(ctx, oasis.ScoreFilter{})
	if err != nil {
		t.Fatalf("ListScores: %v", err)
	}
	if len(remaining) != 1 || remaining[0].RunID != "r2" {
		t.Fatalf("after delete: want 1 row run_id=r2, got %+v", remaining)
	}
}
