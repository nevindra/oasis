package postgres

import (
	"context"
	"encoding/json"
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
