package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	oasis "github.com/nevindra/oasis/core"
)

func TestPostgresScoreStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("OASIS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set OASIS_TEST_POSTGRES_DSN to run")
	}
	s, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	var store oasis.ScoreStore = s
	ctx := context.Background()
	_, _ = store.DeleteScores(ctx, oasis.ScoreFilter{EntityID: "agentP"})

	row := oasis.ScoreRow{ID: "p1", ScorerID: "faithfulness", RunID: "r", EntityID: "agentP", EntityType: "agent",
		Input: "q", Output: "a", Value: 0.8, Reason: "ok",
		Details: json.RawMessage(`{"k":2}`), Source: oasis.ScorerSourceLive, CreatedAt: time.Now()}
	if err := store.SaveScores(ctx, []oasis.ScoreRow{row}); err != nil {
		t.Fatalf("SaveScores: %v", err)
	}
	got, err := store.ListScores(ctx, oasis.ScoreFilter{EntityID: "agentP"})
	if err != nil || len(got) != 1 || got[0].Value != 0.8 || string(got[0].Details) != `{"k":2}` {
		t.Fatalf("ListScores wrong: %+v err=%v", got, err)
	}
	if _, err := store.DeleteScores(ctx, oasis.ScoreFilter{EntityID: "agentP"}); err != nil {
		t.Fatalf("DeleteScores: %v", err)
	}
}

// TestPostgresGetScoreNotFound proves GetScore reports core.ErrNotFound (not the
// leaked pgx.ErrNoRows) when no row has the requested id.
func TestPostgresGetScoreNotFound(t *testing.T) {
	dsn := os.Getenv("OASIS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set OASIS_TEST_POSTGRES_DSN to run")
	}
	s, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	_, err = s.GetScore(context.Background(), "definitely-missing")
	if !errors.Is(err, oasis.ErrNotFound) {
		t.Fatalf("GetScore missing: want ErrNotFound, got %v", err)
	}
}

// TestPostgresScoresRunIDFilter proves ScoreFilter.RunID scopes both ListScores
// and DeleteScores to rows with the matching run_id.
func TestPostgresScoresRunIDFilter(t *testing.T) {
	dsn := os.Getenv("OASIS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set OASIS_TEST_POSTGRES_DSN to run")
	}
	s, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	_, _ = s.DeleteScores(ctx, oasis.ScoreFilter{EntityID: "agentRun"})

	rows := []oasis.ScoreRow{
		{ID: "run1", ScorerID: "f", RunID: "r1", EntityID: "agentRun", EntityType: "agent", CreatedAt: time.Now()},
		{ID: "run2", ScorerID: "f", RunID: "r2", EntityID: "agentRun", EntityType: "agent", CreatedAt: time.Now()},
		{ID: "run3", ScorerID: "f", RunID: "r1", EntityID: "agentRun", EntityType: "agent", CreatedAt: time.Now()},
	}
	if err := s.SaveScores(ctx, rows); err != nil {
		t.Fatalf("SaveScores: %v", err)
	}
	t.Cleanup(func() { _, _ = s.DeleteScores(ctx, oasis.ScoreFilter{EntityID: "agentRun"}) })

	got, err := s.ListScores(ctx, oasis.ScoreFilter{RunID: "r1"})
	if err != nil {
		t.Fatalf("ListScores: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListScores RunID=r1: want 2 rows, got %d", len(got))
	}
	for _, r := range got {
		if r.RunID != "r1" {
			t.Fatalf("ListScores returned run_id=%q, want r1", r.RunID)
		}
	}

	n, err := s.DeleteScores(ctx, oasis.ScoreFilter{RunID: "r1"})
	if err != nil {
		t.Fatalf("DeleteScores: %v", err)
	}
	if n != 2 {
		t.Fatalf("DeleteScores RunID=r1: want 2 deleted, got %d", n)
	}
}
