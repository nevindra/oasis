package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nevindra/oasis"
)

// MemoryStore implements oasis.MemoryStore backed by PostgreSQL with pgvector.
// Semantic deduplication uses pgvector cosine distance instead of brute-force.
type MemoryStore struct {
	pool *pgxpool.Pool
}

var _ oasis.MemoryStore = (*MemoryStore)(nil)

// NewMemoryStore creates a MemoryStore using an existing pgxpool.Pool.
// The caller owns the pool and is responsible for closing it.
func NewMemoryStore(pool *pgxpool.Pool) *MemoryStore {
	return &MemoryStore{pool: pool}
}

// Init creates the pgvector extension, user_facts table, and HNSW index.
// Safe to call multiple times (all statements are idempotent).
func (s *MemoryStore) Init(ctx context.Context) error {
	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		`CREATE TABLE IF NOT EXISTS user_facts (
			id TEXT PRIMARY KEY,
			fact TEXT NOT NULL,
			category TEXT NOT NULL,
			confidence REAL DEFAULT 1.0,
			embedding vector,
			source_message_id TEXT,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS user_facts_embedding_idx ON user_facts USING hnsw (embedding vector_cosine_ops)`,
	}
	for _, stmt := range stmts {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("postgres: memory init: %w", err)
		}
	}
	return nil
}

// UpsertFact inserts a new fact or merges with an existing one if cosine
// similarity exceeds 0.85. Merging updates the text and bumps confidence.
func (s *MemoryStore) UpsertFact(ctx context.Context, fact, category string, embedding []float32) error {
	now := oasis.NowUnix()
	embStr := serializeEmbedding(embedding)

	// Find most similar existing fact using pgvector.
	var bestID string
	var bestConf float64
	var bestScore float32

	rows, err := s.pool.Query(ctx,
		`SELECT id, confidence, 1 - (embedding <=> $1::vector) AS score
		 FROM user_facts
		 WHERE confidence >= 0.3 AND embedding IS NOT NULL
		 ORDER BY embedding <=> $1::vector
		 LIMIT 1`,
		embStr)
	if err != nil {
		return fmt.Errorf("postgres: upsert fact search: %w", err)
	}
	defer rows.Close()

	found := false
	if rows.Next() {
		if err := rows.Scan(&bestID, &bestConf, &bestScore); err == nil && bestScore > 0.85 {
			found = true
		}
	}
	rows.Close()

	if found {
		newConf := bestConf + 0.1
		if newConf > 1.0 {
			newConf = 1.0
		}
		_, err := s.pool.Exec(ctx,
			`UPDATE user_facts SET fact=$1, category=$2, embedding=$3::vector, confidence=$4, updated_at=$5 WHERE id=$6`,
			fact, category, embStr, newConf, now, bestID)
		if err != nil {
			return fmt.Errorf("postgres: merge fact: %w", err)
		}
		return nil
	}

	id := oasis.NewID()
	_, err = s.pool.Exec(ctx,
		`INSERT INTO user_facts (id, fact, category, confidence, embedding, created_at, updated_at)
		 VALUES ($1, $2, $3, 1.0, $4::vector, $5, $6)`,
		id, fact, category, embStr, now, now)
	if err != nil {
		return fmt.Errorf("postgres: insert fact: %w", err)
	}
	return nil
}

// SearchFacts returns facts semantically similar to the query embedding,
// sorted by score descending. Only facts with confidence >= 0.3 are returned.
func (s *MemoryStore) SearchFacts(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredFact, error) {
	embStr := serializeEmbedding(embedding)
	rows, err := s.pool.Query(ctx,
		`SELECT id, fact, category, confidence, created_at, updated_at,
		        1 - (embedding <=> $1::vector) AS score
		 FROM user_facts
		 WHERE confidence >= 0.3 AND embedding IS NOT NULL
		 ORDER BY embedding <=> $1::vector
		 LIMIT $2`,
		embStr, topK)
	if err != nil {
		return nil, fmt.Errorf("postgres: search facts: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredFact
	for rows.Next() {
		var f oasis.Fact
		var score float32
		if err := rows.Scan(&f.ID, &f.Fact, &f.Category, &f.Confidence, &f.CreatedAt, &f.UpdatedAt, &score); err != nil {
			return nil, fmt.Errorf("postgres: scan fact: %w", err)
		}
		results = append(results, oasis.ScoredFact{Fact: f, Score: score})
	}
	return results, rows.Err()
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
	rows, err := s.pool.Query(ctx,
		`SELECT id, fact, category, confidence, created_at, updated_at
		 FROM user_facts WHERE confidence >= 0.3
		 ORDER BY confidence DESC, updated_at DESC
		 LIMIT $1`, limit)
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
	_, err := s.pool.Exec(ctx, `DELETE FROM user_facts WHERE id = $1`, factID)
	return err
}

// DeleteMatchingFacts removes facts whose text matches a LIKE pattern.
func (s *MemoryStore) DeleteMatchingFacts(ctx context.Context, pattern string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM user_facts WHERE fact LIKE $1`, "%"+pattern+"%")
	return err
}

// DecayOldFacts reduces confidence of stale facts and prunes very low ones.
// Facts older than 7 days get confidence * 0.95. Facts with confidence < 0.3
// and age > 30 days are deleted.
func (s *MemoryStore) DecayOldFacts(ctx context.Context) error {
	now := oasis.NowUnix()

	sevenDaysAgo := now - (7 * 86400)
	if _, err := s.pool.Exec(ctx,
		`UPDATE user_facts SET confidence = confidence * 0.95 WHERE updated_at < $1 AND confidence > 0.3`,
		sevenDaysAgo); err != nil {
		return fmt.Errorf("postgres: decay facts: %w", err)
	}

	thirtyDaysAgo := now - (30 * 86400)
	_, err := s.pool.Exec(ctx,
		`DELETE FROM user_facts WHERE confidence < 0.3 AND updated_at < $1`,
		thirtyDaysAgo)
	return err
}
