package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/nevindra/oasis"
)

// MemoryStore implements oasis.MemoryStore backed by SQLite.
// Embeddings are stored as JSON text and similarity search is done
// in-process using brute-force cosine similarity.
//
// Use NewMemoryStore with a shared *sql.DB from Store.DB() so both
// Store and MemoryStore share the same serialized connection.
type MemoryStore struct {
	db *sql.DB
}

var _ oasis.MemoryStore = (*MemoryStore)(nil)

// NewMemoryStore creates a MemoryStore using an existing *sql.DB.
// Pass store.DB() to share the same connection as Store.
func NewMemoryStore(db *sql.DB) *MemoryStore {
	return &MemoryStore{db: db}
}

// Init creates the user_facts table.
func (s *MemoryStore) Init(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS user_facts (
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

// UpsertFact inserts a new fact or merges with an existing one if cosine
// similarity exceeds 0.85. Merging updates the text and bumps confidence.
func (s *MemoryStore) UpsertFact(ctx context.Context, fact, category string, embedding []float32) error {
	now := oasis.NowUnix()
	embJSON := serializeEmbedding(embedding)

	// Brute-force: check existing facts for similarity.
	rows, err := s.db.QueryContext(ctx, `SELECT id, fact, confidence, embedding FROM user_facts WHERE confidence >= 0.3`)
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
		existing, parseErr := deserializeEmbedding(embText)
		if parseErr != nil || len(existing) == 0 {
			continue
		}
		sim := cosineSimilarity(embedding, existing)
		if sim > 0.85 && (best == nil || sim > best.similarity) {
			best = &candidate{id: id, confidence: conf, similarity: sim}
		}
	}
	rows.Close()

	if best != nil {
		// Merge: update existing fact.
		newConf := best.confidence + 0.1
		if newConf > 1.0 {
			newConf = 1.0
		}
		_, err = s.db.ExecContext(ctx,
			`UPDATE user_facts SET fact=?, category=?, embedding=?, confidence=?, updated_at=? WHERE id=?`,
			fact, category, embJSON, newConf, now, best.id)
		return err
	}

	// Insert new fact.
	id := oasis.NewID()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO user_facts (id, fact, category, confidence, embedding, created_at, updated_at) VALUES (?, ?, ?, 1.0, ?, ?, ?)`,
		id, fact, category, embJSON, now, now)
	return err
}

// SearchFacts returns facts semantically similar to the query embedding,
// sorted by Score descending. Only facts with confidence >= 0.3 are returned.
func (s *MemoryStore) SearchFacts(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredFact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, fact, category, confidence, embedding, created_at, updated_at FROM user_facts WHERE confidence >= 0.3`)
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
		emb, parseErr := deserializeEmbedding(embText)
		if parseErr != nil || len(emb) == 0 {
			continue
		}
		all = append(all, oasis.ScoredFact{Fact: f, Score: cosineSimilarity(embedding, emb)})
	}

	// Sort by score descending (selection sort — fine for small N).
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

// BuildContext builds a markdown summary of known user facts for LLM context.
func (s *MemoryStore) BuildContext(ctx context.Context, queryEmbedding []float32) (string, error) {
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

func (s *MemoryStore) getTopFacts(ctx context.Context, limit int) ([]oasis.Fact, error) {
	rows, err := s.db.QueryContext(ctx,
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

// DeleteFact removes a single fact by its ID.
func (s *MemoryStore) DeleteFact(ctx context.Context, factID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_facts WHERE id = ?`, factID)
	return err
}

// DeleteMatchingFacts removes facts whose text matches a LIKE pattern.
func (s *MemoryStore) DeleteMatchingFacts(ctx context.Context, pattern string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_facts WHERE fact LIKE ?`, "%"+pattern+"%")
	return err
}

// DecayOldFacts reduces confidence of stale facts and prunes very low ones.
// Facts older than 7 days get confidence * 0.95. Facts with confidence < 0.3
// and age > 30 days are deleted.
func (s *MemoryStore) DecayOldFacts(ctx context.Context) error {
	now := oasis.NowUnix()

	sevenDaysAgo := now - (7 * 86400)
	if _, err := s.db.ExecContext(ctx, `UPDATE user_facts SET confidence = confidence * 0.95 WHERE updated_at < ? AND confidence > 0.3`, sevenDaysAgo); err != nil {
		return fmt.Errorf("decay facts: %w", err)
	}

	thirtyDaysAgo := now - (30 * 86400)
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_facts WHERE confidence < 0.3 AND updated_at < ?`, thirtyDaysAgo)
	return err
}
