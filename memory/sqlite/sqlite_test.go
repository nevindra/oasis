package sqlite

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// --- helper functions ---

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s := New(dbPath)
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return s
}

// insertFactDirect inserts a fact directly into the DB, bypassing UpsertFact logic.
// Useful for setting up test state with specific field values (e.g. old timestamps, custom confidence).
func insertFactDirect(t *testing.T, s *Store, id, fact, category string, confidence float64, embedding []float32, createdAt, updatedAt int64) {
	t.Helper()
	db, err := s.openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	embJSON := serializeEmbedding(embedding)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO user_facts (id, fact, category, confidence, embedding, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, fact, category, confidence, embJSON, createdAt, updatedAt)
	if err != nil {
		t.Fatalf("insertFactDirect: %v", err)
	}
}

func countFacts(t *testing.T, s *Store) int {
	t.Helper()
	db, err := s.openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM user_facts`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	return count
}

func getConfidence(t *testing.T, s *Store, id string) float64 {
	t.Helper()
	db, err := s.openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	var conf float64
	if err := db.QueryRowContext(context.Background(), `SELECT confidence FROM user_facts WHERE id = ?`, id).Scan(&conf); err != nil {
		t.Fatalf("getConfidence for %q: %v", id, err)
	}
	return conf
}

func getFactText(t *testing.T, s *Store, id string) string {
	t.Helper()
	db, err := s.openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	var fact string
	if err := db.QueryRowContext(context.Background(), `SELECT fact FROM user_facts WHERE id = ?`, id).Scan(&fact); err != nil {
		t.Fatalf("getFactText for %q: %v", id, err)
	}
	return fact
}

// --- unit tests for helpers ---

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float32
	}{
		{
			name: "identical vectors",
			a:    []float32{1, 0, 0},
			b:    []float32{1, 0, 0},
			want: 1.0,
		},
		{
			name: "orthogonal vectors",
			a:    []float32{1, 0, 0},
			b:    []float32{0, 1, 0},
			want: 0.0,
		},
		{
			name: "opposite vectors",
			a:    []float32{1, 0, 0},
			b:    []float32{-1, 0, 0},
			want: -1.0,
		},
		{
			name: "different lengths",
			a:    []float32{1, 0},
			b:    []float32{1, 0, 0},
			want: 0,
		},
		{
			name: "empty vectors",
			a:    []float32{},
			b:    []float32{},
			want: 0,
		},
		{
			name: "zero vectors",
			a:    []float32{0, 0, 0},
			b:    []float32{0, 0, 0},
			want: 0,
		},
		{
			name: "similar but not identical",
			a:    []float32{1, 0.1, 0},
			b:    []float32{1, 0, 0},
			want: float32(1.0 / math.Sqrt(1.01)), // dot=1, normA=sqrt(1.01), normB=1
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cosineSimilarity(tt.a, tt.b)
			if diff := math.Abs(float64(got - tt.want)); diff > 1e-6 {
				t.Errorf("cosineSimilarity(%v, %v) = %v, want %v (diff=%v)", tt.a, tt.b, got, tt.want, diff)
			}
		})
	}
}

func TestSerializeDeserializeRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		emb  []float32
	}{
		{"positive values", []float32{1.0, 2.5, 3.7}},
		{"negative values", []float32{-1.0, -0.5, 0.3}},
		{"zeros", []float32{0, 0, 0}},
		{"mixed", []float32{-1.5, 0, 1.5, 0.001}},
		{"single element", []float32{42.0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := serializeEmbedding(tt.emb)
			got := deserializeEmbedding(s)
			if len(got) != len(tt.emb) {
				t.Fatalf("length mismatch: got %d, want %d", len(got), len(tt.emb))
			}
			for i := range tt.emb {
				if diff := math.Abs(float64(got[i] - tt.emb[i])); diff > 1e-6 {
					t.Errorf("index %d: got %v, want %v", i, got[i], tt.emb[i])
				}
			}
		})
	}
}

func TestSerializeEmpty(t *testing.T) {
	got := serializeEmbedding(nil)
	if got != "" {
		t.Errorf("serializeEmbedding(nil) = %q, want empty string", got)
	}
	got = serializeEmbedding([]float32{})
	if got != "" {
		t.Errorf("serializeEmbedding([]float32{}) = %q, want empty string", got)
	}
}

func TestDeserializeEmpty(t *testing.T) {
	got := deserializeEmbedding("")
	if got != nil {
		t.Errorf("deserializeEmbedding(\"\") = %v, want nil", got)
	}
}

// --- integration tests ---

func TestInitCreatesTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s := New(dbPath)
	ctx := context.Background()

	if err := s.Init(ctx); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Verify table exists by querying it
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var count int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_facts`).Scan(&count)
	if err != nil {
		t.Fatalf("query user_facts after Init: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows, got %d", count)
	}
}

func TestInitIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s := New(dbPath)
	ctx := context.Background()

	// Init twice should not fail (CREATE TABLE IF NOT EXISTS)
	if err := s.Init(ctx); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := s.Init(ctx); err != nil {
		t.Fatalf("second Init: %v", err)
	}
}

func TestUpsertFactInsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	emb := []float32{1, 0, 0}
	err := s.UpsertFact(ctx, "likes Go", "preference", emb)
	if err != nil {
		t.Fatalf("UpsertFact: %v", err)
	}

	n := countFacts(t, s)
	if n != 1 {
		t.Errorf("expected 1 fact, got %d", n)
	}

	// Verify the fact content via getTopFacts
	facts, err := s.getTopFacts(ctx, 10)
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

func TestUpsertFactMergeSimilar(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert first fact with a known embedding
	emb := []float32{1, 0, 0}
	err := s.UpsertFact(ctx, "likes Go", "preference", emb)
	if err != nil {
		t.Fatalf("first UpsertFact: %v", err)
	}
	if n := countFacts(t, s); n != 1 {
		t.Fatalf("expected 1 fact after first insert, got %d", n)
	}

	// Get the ID of the first fact
	facts, err := s.getTopFacts(ctx, 10)
	if err != nil {
		t.Fatalf("getTopFacts: %v", err)
	}
	firstID := facts[0].ID

	// Insert second fact with identical embedding (cosine sim = 1.0 > 0.85 threshold)
	err = s.UpsertFact(ctx, "really likes Go", "preference", emb)
	if err != nil {
		t.Fatalf("second UpsertFact: %v", err)
	}

	// Should still be 1 fact (merged, not inserted)
	if n := countFacts(t, s); n != 1 {
		t.Errorf("expected 1 fact after merge, got %d", n)
	}

	// Verify the fact text was updated
	factText := getFactText(t, s, firstID)
	if factText != "really likes Go" {
		t.Errorf("fact text after merge = %q, want %q", factText, "really likes Go")
	}

	// Verify confidence increased (1.0 + 0.1 = 1.1, capped at 1.0)
	conf := getConfidence(t, s, firstID)
	if conf != 1.0 {
		t.Errorf("confidence after merge = %v, want 1.0 (capped)", conf)
	}
}

func TestUpsertFactMergeConfidenceIncrease(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert a fact directly with confidence < 1.0 so we can observe the increase
	emb := []float32{1, 0, 0}
	now := time.Now().Unix()
	insertFactDirect(t, s, "fact-1", "likes Go", "preference", 0.7, emb, now, now)

	// Upsert with identical embedding should merge and increase confidence by 0.1
	err := s.UpsertFact(ctx, "really likes Go", "preference", emb)
	if err != nil {
		t.Fatalf("UpsertFact: %v", err)
	}

	if n := countFacts(t, s); n != 1 {
		t.Errorf("expected 1 fact after merge, got %d", n)
	}

	conf := getConfidence(t, s, "fact-1")
	expected := 0.8 // 0.7 + 0.1
	if math.Abs(conf-expected) > 1e-6 {
		t.Errorf("confidence = %v, want %v", conf, expected)
	}
}

func TestUpsertFactNoMergeDissimilar(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Two orthogonal embeddings: cosine similarity = 0.0 (well below 0.85)
	err := s.UpsertFact(ctx, "likes Go", "preference", []float32{1, 0, 0})
	if err != nil {
		t.Fatalf("first UpsertFact: %v", err)
	}

	err = s.UpsertFact(ctx, "lives in Jakarta", "location", []float32{0, 1, 0})
	if err != nil {
		t.Fatalf("second UpsertFact: %v", err)
	}

	// Both facts should exist (no merge)
	if n := countFacts(t, s); n != 2 {
		t.Errorf("expected 2 facts (no merge), got %d", n)
	}
}

func TestSearchFacts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	// Three facts with different embeddings
	insertFactDirect(t, s, "f1", "likes Go", "preference", 1.0, []float32{1, 0, 0}, now, now)
	insertFactDirect(t, s, "f2", "lives in Jakarta", "location", 1.0, []float32{0, 1, 0}, now, now)
	insertFactDirect(t, s, "f3", "mostly likes Go", "preference", 1.0, []float32{0.9, 0.1, 0}, now, now)

	// Search with embedding close to f1 and f3
	query := []float32{1, 0, 0}
	results, err := s.SearchFacts(ctx, query, 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// f1 should be first (exact match, cosine=1.0)
	if results[0].ID != "f1" {
		t.Errorf("first result = %q, want f1", results[0].ID)
	}
	// f3 should be second (very similar to query)
	if results[1].ID != "f3" {
		t.Errorf("second result = %q, want f3", results[1].ID)
	}
	// f2 should be last (orthogonal to query)
	if results[2].ID != "f2" {
		t.Errorf("third result = %q, want f2", results[2].ID)
	}
}

func TestSearchFactsTopK(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	// Insert 5 facts
	insertFactDirect(t, s, "f1", "fact one", "cat", 1.0, []float32{1, 0, 0, 0, 0}, now, now)
	insertFactDirect(t, s, "f2", "fact two", "cat", 1.0, []float32{0.9, 0.1, 0, 0, 0}, now, now)
	insertFactDirect(t, s, "f3", "fact three", "cat", 1.0, []float32{0, 1, 0, 0, 0}, now, now)
	insertFactDirect(t, s, "f4", "fact four", "cat", 1.0, []float32{0, 0, 1, 0, 0}, now, now)
	insertFactDirect(t, s, "f5", "fact five", "cat", 1.0, []float32{0, 0, 0, 1, 0}, now, now)

	results, err := s.SearchFacts(ctx, []float32{1, 0, 0, 0, 0}, 2)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results (topK=2), got %d", len(results))
	}

	// Best matches should be f1 (exact) and f2 (close)
	if results[0].ID != "f1" {
		t.Errorf("first result = %q, want f1", results[0].ID)
	}
	if results[1].ID != "f2" {
		t.Errorf("second result = %q, want f2", results[1].ID)
	}
}

func TestSearchFactsFiltersLowConfidence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	// Insert one high-confidence fact and one below threshold (< 0.3)
	insertFactDirect(t, s, "f1", "visible", "cat", 1.0, []float32{1, 0, 0}, now, now)
	insertFactDirect(t, s, "f2", "invisible", "cat", 0.2, []float32{1, 0, 0}, now, now)

	results, err := s.SearchFacts(ctx, []float32{1, 0, 0}, 10)
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

func TestBuildContextWithEmbedding(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	insertFactDirect(t, s, "f1", "likes Go", "preference", 1.0, []float32{1, 0, 0}, now, now)
	insertFactDirect(t, s, "f2", "lives in Jakarta", "location", 1.0, []float32{0, 1, 0}, now, now)

	result, err := s.BuildContext(ctx, []float32{1, 0, 0})
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
	// First fact should be "likes Go" (exact match to query embedding)
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines (header + fact), got %d", len(lines))
	}
	if !strings.Contains(lines[1], "likes Go") {
		t.Errorf("first fact should be 'likes Go' (best match), got: %s", lines[1])
	}
}

func TestBuildContextWithoutEmbedding(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	// Insert facts with different confidences to verify ordering
	insertFactDirect(t, s, "f1", "low confidence fact", "cat", 0.5, []float32{1, 0, 0}, now, now)
	insertFactDirect(t, s, "f2", "high confidence fact", "cat", 1.0, []float32{0, 1, 0}, now, now)

	result, err := s.BuildContext(ctx, nil)
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	if !strings.HasPrefix(result, "## What you know about the user\n") {
		t.Errorf("missing header, got:\n%s", result)
	}
	if !strings.Contains(result, "low confidence fact") {
		t.Errorf("missing 'low confidence fact', got:\n%s", result)
	}
	if !strings.Contains(result, "high confidence fact") {
		t.Errorf("missing 'high confidence fact', got:\n%s", result)
	}
	// getTopFacts sorts by confidence DESC, so high confidence should come first
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (header + 2 facts), got %d", len(lines))
	}
	if !strings.Contains(lines[1], "high confidence fact") {
		t.Errorf("first fact should be 'high confidence fact', got: %s", lines[1])
	}
}

func TestBuildContextEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	result, err := s.BuildContext(ctx, nil)
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string for no facts, got %q", result)
	}
}

func TestBuildContextEmptyWithEmbedding(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	result, err := s.BuildContext(ctx, []float32{1, 0, 0})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string for no facts, got %q", result)
	}
}

func TestDeleteMatchingFacts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	insertFactDirect(t, s, "f1", "likes Go programming", "preference", 1.0, []float32{1, 0, 0}, now, now)
	insertFactDirect(t, s, "f2", "lives in Jakarta", "location", 1.0, []float32{0, 1, 0}, now, now)
	insertFactDirect(t, s, "f3", "likes Python too", "preference", 1.0, []float32{0, 0, 1}, now, now)

	// Delete facts matching "likes"
	err := s.DeleteMatchingFacts(ctx, "likes")
	if err != nil {
		t.Fatalf("DeleteMatchingFacts: %v", err)
	}

	n := countFacts(t, s)
	if n != 1 {
		t.Errorf("expected 1 fact remaining, got %d", n)
	}

	facts, err := s.getTopFacts(ctx, 10)
	if err != nil {
		t.Fatalf("getTopFacts: %v", err)
	}
	if len(facts) != 1 || facts[0].ID != "f2" {
		t.Errorf("expected only f2 to remain, got %v", facts)
	}
}

func TestDeleteMatchingFactsNoMatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	insertFactDirect(t, s, "f1", "likes Go", "preference", 1.0, []float32{1, 0, 0}, now, now)

	err := s.DeleteMatchingFacts(ctx, "nonexistent pattern")
	if err != nil {
		t.Fatalf("DeleteMatchingFacts: %v", err)
	}

	if n := countFacts(t, s); n != 1 {
		t.Errorf("expected 1 fact (nothing deleted), got %d", n)
	}
}

func TestDecayOldFacts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	eightDaysAgo := now - (8 * 86400)

	// Insert a fact with old updated_at (> 7 days) and confidence > 0.3
	insertFactDirect(t, s, "old-fact", "old memory", "cat", 0.8, []float32{1, 0, 0}, eightDaysAgo, eightDaysAgo)
	// Insert a recent fact that should NOT decay
	insertFactDirect(t, s, "new-fact", "fresh memory", "cat", 0.8, []float32{0, 1, 0}, now, now)

	err := s.DecayOldFacts(ctx)
	if err != nil {
		t.Fatalf("DecayOldFacts: %v", err)
	}

	// Old fact confidence should have decayed: 0.8 * 0.95 = 0.76
	oldConf := getConfidence(t, s, "old-fact")
	expected := 0.8 * 0.95
	if math.Abs(oldConf-expected) > 1e-6 {
		t.Errorf("old fact confidence = %v, want %v", oldConf, expected)
	}

	// New fact confidence should be unchanged
	newConf := getConfidence(t, s, "new-fact")
	if math.Abs(newConf-0.8) > 1e-6 {
		t.Errorf("new fact confidence = %v, want 0.8 (unchanged)", newConf)
	}
}

func TestDecayOldFactsDeletesVeryOld(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	thirtyOneDaysAgo := now - (31 * 86400)

	// Insert a fact that is old AND has low confidence (should be deleted)
	insertFactDirect(t, s, "ancient-fact", "ancient memory", "cat", 0.2, []float32{1, 0, 0}, thirtyOneDaysAgo, thirtyOneDaysAgo)
	// Insert a fact that is old but has OK confidence (should survive)
	insertFactDirect(t, s, "old-ok-fact", "old but ok", "cat", 0.5, []float32{0, 1, 0}, thirtyOneDaysAgo, thirtyOneDaysAgo)

	err := s.DecayOldFacts(ctx)
	if err != nil {
		t.Fatalf("DecayOldFacts: %v", err)
	}

	// ancient-fact should be deleted (confidence < 0.3 AND updated_at > 30 days ago)
	db, err := s.openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	var count int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_facts WHERE id = ?`, "ancient-fact").Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Errorf("expected ancient-fact to be deleted, but it still exists")
	}

	// old-ok-fact should still exist (confidence >= 0.3 even after decay)
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_facts WHERE id = ?`, "old-ok-fact").Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected old-ok-fact to survive, but it was deleted")
	}
}

func TestDecayDoesNotDecayHighConfidenceBelowThreshold(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	eightDaysAgo := now - (8 * 86400)

	// Fact exactly at 0.3 threshold should NOT be decayed (condition is confidence > 0.3)
	insertFactDirect(t, s, "threshold-fact", "at threshold", "cat", 0.3, []float32{1, 0, 0}, eightDaysAgo, eightDaysAgo)

	err := s.DecayOldFacts(ctx)
	if err != nil {
		t.Fatalf("DecayOldFacts: %v", err)
	}

	conf := getConfidence(t, s, "threshold-fact")
	if math.Abs(conf-0.3) > 1e-6 {
		t.Errorf("threshold fact confidence = %v, want 0.3 (unchanged, not > 0.3)", conf)
	}
}

func TestGetTopFactsOrdering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	// Insert facts with varying confidence and timestamps
	insertFactDirect(t, s, "f1", "low conf old", "cat", 0.5, nil, now-100, now-100)
	insertFactDirect(t, s, "f2", "high conf old", "cat", 1.0, nil, now-100, now-100)
	insertFactDirect(t, s, "f3", "high conf new", "cat", 1.0, nil, now, now)

	facts, err := s.getTopFacts(ctx, 10)
	if err != nil {
		t.Fatalf("getTopFacts: %v", err)
	}

	if len(facts) != 3 {
		t.Fatalf("expected 3 facts, got %d", len(facts))
	}
	// ORDER BY confidence DESC, updated_at DESC
	// f3 (1.0, now) > f2 (1.0, now-100) > f1 (0.5, now-100)
	if facts[0].ID != "f3" {
		t.Errorf("first = %q, want f3", facts[0].ID)
	}
	if facts[1].ID != "f2" {
		t.Errorf("second = %q, want f2", facts[1].ID)
	}
	if facts[2].ID != "f1" {
		t.Errorf("third = %q, want f1", facts[2].ID)
	}
}

func TestGetTopFactsLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Unix()
	for i := 0; i < 5; i++ {
		id := strings.Replace("fact-X", "X", string(rune('a'+i)), 1)
		insertFactDirect(t, s, id, "fact", "cat", 1.0, nil, now, now)
	}

	facts, err := s.getTopFacts(ctx, 3)
	if err != nil {
		t.Fatalf("getTopFacts: %v", err)
	}
	if len(facts) != 3 {
		t.Errorf("expected 3 facts (limit=3), got %d", len(facts))
	}
}
