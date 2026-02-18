// Package sqlite implements oasis.MemoryStore using pure-Go SQLite with
// brute-force cosine similarity for semantic deduplication and decay.
//
// Swap in a different backend (e.g. pgvector) by implementing
// oasis.MemoryStore with your own package.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"

	oasis "github.com/nevindra/oasis"
	_ "modernc.org/sqlite"
)

// Store implements oasis.MemoryStore backed by SQLite.
// Embeddings are stored as JSON text and similarity search is done
// in-process using brute-force cosine similarity.
type Store struct {
	dbPath string
}

var _ oasis.MemoryStore = (*Store)(nil)

// New creates a semantic memory store using a local SQLite file.
func New(dbPath string) *Store {
	return &Store{dbPath: dbPath}
}

func (s *Store) openDB() (*sql.DB, error) {
	return sql.Open("sqlite", s.dbPath)
}

func (s *Store) Init(ctx context.Context) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS user_facts (
		id TEXT PRIMARY KEY,
		fact TEXT NOT NULL,
		category TEXT NOT NULL,
		confidence REAL DEFAULT 1.0,
		embedding TEXT,
		source_message_id TEXT,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`)
	return err
}

func (s *Store) UpsertFact(ctx context.Context, fact, category string, embedding []float32) error {
	now := oasis.NowUnix()
	embJSON := serializeEmbedding(embedding)

	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	// Brute-force: check existing facts for similarity
	rows, err := db.QueryContext(ctx, `SELECT id, fact, confidence, embedding FROM user_facts WHERE confidence >= 0.3`)
	if err != nil {
		return err
	}

	type candidate struct {
		id         string
		confidence float64
		similarity float32
	}
	var best *candidate

	for rows.Next() {
		var id, factText, embText string
		var conf float64
		if err := rows.Scan(&id, &factText, &conf, &embText); err != nil {
			continue
		}
		existing := deserializeEmbedding(embText)
		if len(existing) == 0 {
			continue
		}
		sim := cosineSimilarity(embedding, existing)
		if sim > 0.85 && (best == nil || sim > best.similarity) {
			best = &candidate{id: id, confidence: conf, similarity: sim}
		}
	}
	rows.Close()

	if best != nil {
		// Merge: update existing fact
		newConf := best.confidence + 0.1
		if newConf > 1.0 {
			newConf = 1.0
		}
		_, err = db.ExecContext(ctx,
			`UPDATE user_facts SET fact=?, category=?, embedding=?, confidence=?, updated_at=? WHERE id=?`,
			fact, category, embJSON, newConf, now, best.id)
		return err
	}

	// Insert new fact
	id := oasis.NewID()
	_, err = db.ExecContext(ctx,
		`INSERT INTO user_facts (id, fact, category, confidence, embedding, created_at, updated_at) VALUES (?, ?, ?, 1.0, ?, ?, ?)`,
		id, fact, category, embJSON, now, now)
	return err
}

func (s *Store) SearchFacts(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredFact, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT id, fact, category, confidence, embedding, created_at, updated_at FROM user_facts WHERE confidence >= 0.3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []oasis.ScoredFact

	for rows.Next() {
		var f oasis.Fact
		var embText string
		if err := rows.Scan(&f.ID, &f.Fact, &f.Category, &f.Confidence, &embText, &f.CreatedAt, &f.UpdatedAt); err != nil {
			continue
		}
		emb := deserializeEmbedding(embText)
		if len(emb) > 0 {
			all = append(all, oasis.ScoredFact{Fact: f, Score: cosineSimilarity(embedding, emb)})
		}
	}

	// Sort by score descending (selection sort — fine for small N)
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[j].Score > all[i].Score {
				all[i], all[j] = all[j], all[i]
			}
		}
	}

	if len(all) > topK {
		all = all[:topK]
	}
	return all, nil
}

func (s *Store) BuildContext(ctx context.Context, queryEmbedding []float32) (string, error) {
	var facts []oasis.ScoredFact
	var err error

	if len(queryEmbedding) > 0 {
		facts, err = s.SearchFacts(ctx, queryEmbedding, 10)
	} else {
		rawFacts, ferr := s.getTopFacts(ctx, 15)
		if ferr != nil {
			return "", ferr
		}
		for _, f := range rawFacts {
			facts = append(facts, oasis.ScoredFact{Fact: f})
		}
	}
	if err != nil || len(facts) == 0 {
		return "", err
	}

	var b strings.Builder
	b.WriteString("## What you know about the user\n")
	for _, sf := range facts {
		fmt.Fprintf(&b, "- %s [%s]\n", sf.Fact.Fact, sf.Fact.Category)
	}
	return b.String(), nil
}

func (s *Store) getTopFacts(ctx context.Context, limit int) ([]oasis.Fact, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT id, fact, category, confidence, created_at, updated_at FROM user_facts WHERE confidence >= 0.3 ORDER BY confidence DESC, updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []oasis.Fact
	for rows.Next() {
		var f oasis.Fact
		if err := rows.Scan(&f.ID, &f.Fact, &f.Category, &f.Confidence, &f.CreatedAt, &f.UpdatedAt); err != nil {
			continue
		}
		facts = append(facts, f)
	}
	return facts, nil
}

func (s *Store) DeleteFact(ctx context.Context, factID string) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `DELETE FROM user_facts WHERE id = ?`, factID)
	return err
}

func (s *Store) DeleteMatchingFacts(ctx context.Context, pattern string) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `DELETE FROM user_facts WHERE fact LIKE ?`, "%"+pattern+"%")
	return err
}

func (s *Store) DecayOldFacts(ctx context.Context) error {
	now := oasis.NowUnix()
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	sevenDaysAgo := now - (7 * 86400)
	_, _ = db.ExecContext(ctx, `UPDATE user_facts SET confidence = confidence * 0.95 WHERE updated_at < ? AND confidence > 0.3`, sevenDaysAgo)

	thirtyDaysAgo := now - (30 * 86400)
	_, err = db.ExecContext(ctx, `DELETE FROM user_facts WHERE confidence < 0.3 AND updated_at < ?`, thirtyDaysAgo)
	return err
}

// --- helpers ---

func serializeEmbedding(emb []float32) string {
	if len(emb) == 0 {
		return ""
	}
	parts := make([]string, len(emb))
	for i, v := range emb {
		parts[i] = fmt.Sprintf("%g", v)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func deserializeEmbedding(s string) []float32 {
	if s == "" {
		return nil
	}
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	parts := strings.Split(s, ",")
	emb := make([]float32, 0, len(parts))
	for _, p := range parts {
		var v float32
		if _, err := fmt.Sscanf(strings.TrimSpace(p), "%g", &v); err == nil {
			emb = append(emb, v)
		}
	}
	return emb
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}
