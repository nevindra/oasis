package sqlite

import (
	"context"
	"encoding/json"
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
