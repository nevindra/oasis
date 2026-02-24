package sqlite

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

func testMemoryStore(t *testing.T) *MemoryStore {
	t.Helper()
	s := testStore(t)
	ms := NewMemoryStore(s.db)
	if err := ms.Init(context.Background()); err != nil {
		t.Fatalf("MemoryStore Init: %v", err)
	}
	return ms
}

// insertMemFact inserts a fact directly into the DB for test setup.
func insertMemFact(t *testing.T, ms *MemoryStore, id, fact, category string, confidence float64, embedding []float32, createdAt, updatedAt int64) {
	t.Helper()
	embJSON := serializeEmbedding(embedding)
	_, err := ms.db.ExecContext(context.Background(),
		`INSERT INTO user_facts (id, fact, category, confidence, embedding, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, fact, category, confidence, embJSON, createdAt, updatedAt)
	if err != nil {
		t.Fatalf("insertMemFact: %v", err)
	}
}

func countMemFacts(t *testing.T, ms *MemoryStore) int {
	t.Helper()
	var count int
	if err := ms.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM user_facts`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	return count
}

func getMemConfidence(t *testing.T, ms *MemoryStore, id string) float64 {
	t.Helper()
	var conf float64
	if err := ms.db.QueryRowContext(context.Background(), `SELECT confidence FROM user_facts WHERE id = ?`, id).Scan(&conf); err != nil {
		t.Fatalf("getMemConfidence for %q: %v", id, err)
	}
	return conf
}

func getMemFactText(t *testing.T, ms *MemoryStore, id string) string {
	t.Helper()
	var fact string
	if err := ms.db.QueryRowContext(context.Background(), `SELECT fact FROM user_facts WHERE id = ?`, id).Scan(&fact); err != nil {
		t.Fatalf("getMemFactText for %q: %v", id, err)
	}
	return fact
}

func TestMemoryStoreInitCreatesTable(t *testing.T) {
	s := testStore(t)
	ms := NewMemoryStore(s.db)
	ctx := context.Background()

	if err := ms.Init(ctx); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	var count int
	err := ms.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_facts`).Scan(&count)
	if err != nil {
		t.Fatalf("query user_facts after Init: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows, got %d", count)
	}
}

func TestMemoryStoreInitIdempotent(t *testing.T) {
	s := testStore(t)
	ms := NewMemoryStore(s.db)
	ctx := context.Background()

	if err := ms.Init(ctx); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := ms.Init(ctx); err != nil {
		t.Fatalf("second Init: %v", err)
	}
}

func TestMemoryStoreUpsertFactInsert(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	emb := []float32{1, 0, 0}
	if err := ms.UpsertFact(ctx, "likes Go", "preference", emb); err != nil {
		t.Fatalf("UpsertFact: %v", err)
	}

	if n := countMemFacts(t, ms); n != 1 {
		t.Errorf("expected 1 fact, got %d", n)
	}

	facts, err := ms.getTopFacts(ctx, 10)
	if err != nil {
		t.Fatalf("getTopFacts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].Fact != "likes Go" {
		t.Errorf("fact text = %q, want %q", facts[0].Fact, "likes Go")
	}
	if facts[0].Category != "preference" {
		t.Errorf("category = %q, want %q", facts[0].Category, "preference")
	}
	if facts[0].Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0", facts[0].Confidence)
	}
}

func TestMemoryStoreUpsertFactMergeSimilar(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	emb := []float32{1, 0, 0}
	if err := ms.UpsertFact(ctx, "likes Go", "preference", emb); err != nil {
		t.Fatalf("first UpsertFact: %v", err)
	}

	facts, _ := ms.getTopFacts(ctx, 10)
	firstID := facts[0].ID

	// Identical embedding → cosine sim = 1.0 > 0.85 → should merge.
	if err := ms.UpsertFact(ctx, "really likes Go", "preference", emb); err != nil {
		t.Fatalf("second UpsertFact: %v", err)
	}

	if n := countMemFacts(t, ms); n != 1 {
		t.Errorf("expected 1 fact after merge, got %d", n)
	}

	factText := getMemFactText(t, ms, firstID)
	if factText != "really likes Go" {
		t.Errorf("fact text after merge = %q, want %q", factText, "really likes Go")
	}

	// 1.0 + 0.1 = 1.1, capped at 1.0
	conf := getMemConfidence(t, ms, firstID)
	if conf != 1.0 {
		t.Errorf("confidence after merge = %v, want 1.0 (capped)", conf)
	}
}

func TestMemoryStoreUpsertFactMergeConfidenceIncrease(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	emb := []float32{1, 0, 0}
	now := time.Now().Unix()
	insertMemFact(t, ms, "fact-1", "likes Go", "preference", 0.7, emb, now, now)

	if err := ms.UpsertFact(ctx, "really likes Go", "preference", emb); err != nil {
		t.Fatalf("UpsertFact: %v", err)
	}

	if n := countMemFacts(t, ms); n != 1 {
		t.Errorf("expected 1 fact after merge, got %d", n)
	}

	conf := getMemConfidence(t, ms, "fact-1")
	expected := 0.8 // 0.7 + 0.1
	if math.Abs(conf-expected) > 1e-6 {
		t.Errorf("confidence = %v, want %v", conf, expected)
	}
}

func TestMemoryStoreUpsertFactNoMergeDissimilar(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	if err := ms.UpsertFact(ctx, "likes Go", "preference", []float32{1, 0, 0}); err != nil {
		t.Fatalf("first UpsertFact: %v", err)
	}
	if err := ms.UpsertFact(ctx, "lives in Jakarta", "location", []float32{0, 1, 0}); err != nil {
		t.Fatalf("second UpsertFact: %v", err)
	}

	if n := countMemFacts(t, ms); n != 2 {
		t.Errorf("expected 2 facts (no merge), got %d", n)
	}
}

func TestMemoryStoreSearchFacts(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	insertMemFact(t, ms, "f1", "likes Go", "preference", 1.0, []float32{1, 0, 0}, now, now)
	insertMemFact(t, ms, "f2", "lives in Jakarta", "location", 1.0, []float32{0, 1, 0}, now, now)
	insertMemFact(t, ms, "f3", "mostly likes Go", "preference", 1.0, []float32{0.9, 0.1, 0}, now, now)

	results, err := ms.SearchFacts(ctx, []float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0].ID != "f1" {
		t.Errorf("first result = %q, want f1", results[0].ID)
	}
	if results[1].ID != "f3" {
		t.Errorf("second result = %q, want f3", results[1].ID)
	}
	if results[2].ID != "f2" {
		t.Errorf("third result = %q, want f2", results[2].ID)
	}
}

func TestMemoryStoreSearchFactsTopK(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	insertMemFact(t, ms, "f1", "fact one", "cat", 1.0, []float32{1, 0, 0, 0, 0}, now, now)
	insertMemFact(t, ms, "f2", "fact two", "cat", 1.0, []float32{0.9, 0.1, 0, 0, 0}, now, now)
	insertMemFact(t, ms, "f3", "fact three", "cat", 1.0, []float32{0, 1, 0, 0, 0}, now, now)
	insertMemFact(t, ms, "f4", "fact four", "cat", 1.0, []float32{0, 0, 1, 0, 0}, now, now)
	insertMemFact(t, ms, "f5", "fact five", "cat", 1.0, []float32{0, 0, 0, 1, 0}, now, now)

	results, err := ms.SearchFacts(ctx, []float32{1, 0, 0, 0, 0}, 2)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (topK=2), got %d", len(results))
	}
	if results[0].ID != "f1" {
		t.Errorf("first result = %q, want f1", results[0].ID)
	}
	if results[1].ID != "f2" {
		t.Errorf("second result = %q, want f2", results[1].ID)
	}
}

func TestMemoryStoreSearchFactsFiltersLowConfidence(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	insertMemFact(t, ms, "f1", "visible", "cat", 1.0, []float32{1, 0, 0}, now, now)
	insertMemFact(t, ms, "f2", "invisible", "cat", 0.2, []float32{1, 0, 0}, now, now)

	results, err := ms.SearchFacts(ctx, []float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (low confidence filtered), got %d", len(results))
	}
	if results[0].ID != "f1" {
		t.Errorf("result = %q, want f1", results[0].ID)
	}
}

func TestMemoryStoreBuildContextWithEmbedding(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	insertMemFact(t, ms, "f1", "likes Go", "preference", 1.0, []float32{1, 0, 0}, now, now)
	insertMemFact(t, ms, "f2", "lives in Jakarta", "location", 1.0, []float32{0, 1, 0}, now, now)

	result, err := ms.BuildContext(ctx, []float32{1, 0, 0})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if !strings.HasPrefix(result, "## What you know about the user\n") {
		t.Errorf("missing header, got:\n%s", result)
	}
	if !strings.Contains(result, "likes Go") {
		t.Errorf("missing fact 'likes Go', got:\n%s", result)
	}
	if !strings.Contains(result, "[preference]") {
		t.Errorf("missing category [preference], got:\n%s", result)
	}

	lines := strings.Split(strings.TrimSpace(result), "\n")
	// Line 0: header, line 1: trust framing, line 2: blank, line 3+: facts
	if len(lines) < 4 {
		t.Fatalf("expected at least 4 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[3], "likes Go") {
		t.Errorf("first fact should be 'likes Go' (best match), got: %s", lines[3])
	}
}

func TestMemoryStoreBuildContextWithoutEmbedding(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	insertMemFact(t, ms, "f1", "low confidence fact", "cat", 0.5, []float32{1, 0, 0}, now, now)
	insertMemFact(t, ms, "f2", "high confidence fact", "cat", 1.0, []float32{0, 1, 0}, now, now)

	result, err := ms.BuildContext(ctx, nil)
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if !strings.HasPrefix(result, "## What you know about the user\n") {
		t.Errorf("missing header, got:\n%s", result)
	}

	lines := strings.Split(strings.TrimSpace(result), "\n")
	// Line 0: header, line 1: trust framing, line 2: blank, line 3+: facts
	if len(lines) < 5 {
		t.Fatalf("expected at least 5 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[3], "high confidence fact") {
		t.Errorf("first fact should be 'high confidence fact', got: %s", lines[3])
	}
}

func TestMemoryStoreBuildContextEmpty(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	result, err := ms.BuildContext(ctx, nil)
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string for no facts, got %q", result)
	}
}

func TestMemoryStoreDeleteMatchingFacts(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	insertMemFact(t, ms, "f1", "likes Go programming", "preference", 1.0, []float32{1, 0, 0}, now, now)
	insertMemFact(t, ms, "f2", "lives in Jakarta", "location", 1.0, []float32{0, 1, 0}, now, now)
	insertMemFact(t, ms, "f3", "likes Python too", "preference", 1.0, []float32{0, 0, 1}, now, now)

	if err := ms.DeleteMatchingFacts(ctx, "likes"); err != nil {
		t.Fatalf("DeleteMatchingFacts: %v", err)
	}

	if n := countMemFacts(t, ms); n != 1 {
		t.Errorf("expected 1 fact remaining, got %d", n)
	}

	facts, _ := ms.getTopFacts(ctx, 10)
	if len(facts) != 1 || facts[0].ID != "f2" {
		t.Errorf("expected only f2 to remain, got %v", facts)
	}
}

func TestMemoryStoreDecayOldFacts(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	eightDaysAgo := now - (8 * 86400)

	insertMemFact(t, ms, "old-fact", "old memory", "cat", 0.8, []float32{1, 0, 0}, eightDaysAgo, eightDaysAgo)
	insertMemFact(t, ms, "new-fact", "fresh memory", "cat", 0.8, []float32{0, 1, 0}, now, now)

	if err := ms.DecayOldFacts(ctx); err != nil {
		t.Fatalf("DecayOldFacts: %v", err)
	}

	oldConf := getMemConfidence(t, ms, "old-fact")
	expected := 0.8 * 0.95
	if math.Abs(oldConf-expected) > 1e-6 {
		t.Errorf("old fact confidence = %v, want %v", oldConf, expected)
	}

	newConf := getMemConfidence(t, ms, "new-fact")
	if math.Abs(newConf-0.8) > 1e-6 {
		t.Errorf("new fact confidence = %v, want 0.8 (unchanged)", newConf)
	}
}

func TestMemoryStoreDecayOldFactsDeletesVeryOld(t *testing.T) {
	ms := testMemoryStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	thirtyOneDaysAgo := now - (31 * 86400)

	insertMemFact(t, ms, "ancient-fact", "ancient memory", "cat", 0.2, []float32{1, 0, 0}, thirtyOneDaysAgo, thirtyOneDaysAgo)
	insertMemFact(t, ms, "old-ok-fact", "old but ok", "cat", 0.5, []float32{0, 1, 0}, thirtyOneDaysAgo, thirtyOneDaysAgo)

	if err := ms.DecayOldFacts(ctx); err != nil {
		t.Fatalf("DecayOldFacts: %v", err)
	}

	var count int
	ms.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_facts WHERE id = ?`, "ancient-fact").Scan(&count)
	if count != 0 {
		t.Errorf("expected ancient-fact to be deleted, but it still exists")
	}

	ms.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_facts WHERE id = ?`, "old-ok-fact").Scan(&count)
	if count != 1 {
		t.Errorf("expected old-ok-fact to survive, but it was deleted")
	}
}
