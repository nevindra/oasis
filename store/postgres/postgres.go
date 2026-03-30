// Package postgres implements oasis.Store and oasis.MemoryStore using
// PostgreSQL with pgvector for native vector similarity search and
// tsvector for full-text keyword search.
//
// Both Store and MemoryStore accept an externally-owned *pgxpool.Pool
// via constructor injection. The caller creates and closes the pool.
package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nevindra/oasis"
)

// Store implements oasis.Store backed by PostgreSQL with pgvector.
// Vector search uses HNSW indexes with cosine distance.
type Store struct {
	pool   *pgxpool.Pool
	cfg    pgConfig
	logger *slog.Logger
}

// pgConfig holds store configuration set via Option functions.
type pgConfig struct {
	embeddingDimension int          // required — pgvector HNSW indexes need vector(N)
	hnswM              int          // 0 = pgvector default (16)
	hnswEFConstruction int          // 0 = pgvector default (64)
	hnswEFSearch       int          // 0 = pgvector default (40)
	logger             *slog.Logger // nil = no logs
}

// Option configures a PostgreSQL Store or MemoryStore.
type Option func(*pgConfig)

// WithEmbeddingDimension sets the vector column dimension (e.g. 1536, 768).
// When set, CREATE TABLE uses vector(N) instead of untyped vector, enabling
// better index optimization and catching dimension mismatches at insert time.
// Only affects new table creation (no ALTER on existing tables).
func WithEmbeddingDimension(dim int) Option {
	return func(c *pgConfig) { c.embeddingDimension = dim }
}

// WithHNSWM sets the HNSW m parameter (max connections per node).
// Higher values improve recall at the cost of memory. Default: pgvector's 16.
// Only affects index creation (CREATE INDEX IF NOT EXISTS).
func WithHNSWM(m int) Option {
	return func(c *pgConfig) { c.hnswM = m }
}

// WithEFConstruction sets the HNSW ef_construction parameter (build-time
// candidate list size). Higher values improve index quality at the cost of
// slower builds. Default: pgvector's 64.
// Only affects index creation (CREATE INDEX IF NOT EXISTS).
func WithEFConstruction(ef int) Option {
	return func(c *pgConfig) { c.hnswEFConstruction = ef }
}

// WithLogger sets a structured logger for the store.
// When set, the store emits debug logs for every operation including
// timing, row counts, and key parameters. If not set, no logs are emitted.
func WithLogger(l *slog.Logger) Option {
	return func(c *pgConfig) { c.logger = l }
}

// WithEFSearch sets the HNSW ef_search parameter (query-time candidate list
// size). Higher values improve recall at the cost of latency. Default:
// pgvector's 40. Applied via SET (session-level) during Init().
func WithEFSearch(ef int) Option {
	return func(c *pgConfig) { c.hnswEFSearch = ef }
}

var _ oasis.Store = (*Store)(nil)
var _ oasis.KeywordSearcher = (*Store)(nil)
var _ oasis.GraphStore = (*Store)(nil)
var _ oasis.BidirectionalGraphStore = (*Store)(nil)
var _ oasis.CheckpointStore = (*Store)(nil)
var _ oasis.DocumentMetaLister = (*Store)(nil)

// nopLogger is a logger that discards all output.
var nopLogger = slog.New(pgDiscardHandler{})

type pgDiscardHandler struct{}

func (pgDiscardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (pgDiscardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d pgDiscardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d pgDiscardHandler) WithGroup(string) slog.Handler           { return d }

// ConfigurePoolConfig applies store settings that require per-connection
// setup to a pgxpool.Config. Call this before pgxpool.NewWithConfig so
// every connection in the pool inherits the settings.
//
// Currently applies: hnsw.ef_search (HNSW query-time candidate list size).
// If no relevant options are set, cfg is returned unmodified.
func ConfigurePoolConfig(cfg *pgxpool.Config, opts ...Option) {
	var pgCfg pgConfig
	for _, o := range opts {
		o(&pgCfg)
	}
	if pgCfg.hnswEFSearch <= 0 {
		return
	}
	existing := cfg.AfterConnect
	efSearch := pgCfg.hnswEFSearch
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if existing != nil {
			if err := existing(ctx, conn); err != nil {
				return err
			}
		}
		_, err := conn.Exec(ctx, fmt.Sprintf("SET hnsw.ef_search = %d", efSearch))
		return err
	}
}

// New creates a Store using an existing pgxpool.Pool.
// The caller owns the pool and is responsible for closing it.
func New(pool *pgxpool.Pool, opts ...Option) *Store {
	var cfg pgConfig
	for _, o := range opts {
		o(&cfg)
	}
	logger := cfg.logger
	if logger == nil {
		logger = nopLogger
	}
	return &Store{pool: pool, cfg: cfg, logger: logger}
}

// vectorType returns "vector" or "vector(N)" depending on config.
func (s *Store) vectorType() string {
	if s.cfg.embeddingDimension > 0 {
		return fmt.Sprintf("vector(%d)", s.cfg.embeddingDimension)
	}
	return "vector"
}

// hnswWithClause returns the WITH (...) clause for HNSW index creation,
// or an empty string if no tuning params are set.
func (s *Store) hnswWithClause() string {
	var parts []string
	if s.cfg.hnswM > 0 {
		parts = append(parts, fmt.Sprintf("m = %d", s.cfg.hnswM))
	}
	if s.cfg.hnswEFConstruction > 0 {
		parts = append(parts, fmt.Sprintf("ef_construction = %d", s.cfg.hnswEFConstruction))
	}
	if len(parts) == 0 {
		return ""
	}
	return " WITH (" + strings.Join(parts, ", ") + ")"
}

// Init creates the pgvector extension, all required tables, and indexes.
// Safe to call multiple times (all statements are idempotent).
//
// Requires WithEmbeddingDimension to be set — pgvector HNSW indexes need
// typed vector(N) columns.
func (s *Store) Init(ctx context.Context) error {
	start := time.Now()
	s.logger.Debug("postgres: init started")
	if s.cfg.embeddingDimension <= 0 {
		return fmt.Errorf("postgres: init: embedding dimension is required (use WithEmbeddingDimension)")
	}
	vtype := s.vectorType()
	hnswWith := s.hnswWithClause()

	// pgvector HNSW indexes support at most 2000 dimensions.
	// For larger vectors, skip the index (brute-force sequential scan still works).
	const maxHNSWDim = 2000
	useHNSW := s.cfg.embeddingDimension <= maxHNSWDim
	if !useHNSW {
		s.logger.Warn("postgres: embedding dimension exceeds HNSW limit, skipping vector indexes (sequential scan will be used)",
			"dimension", s.cfg.embeddingDimension, "max_hnsw", maxHNSWDim)
	}

	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,

		`CREATE TABLE IF NOT EXISTS threads (
			id TEXT PRIMARY KEY,
			chat_id TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			metadata JSONB,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)`,

		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			thread_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			embedding %s,
			metadata JSONB,
			created_at BIGINT NOT NULL
		)`, vtype),
		`CREATE INDEX IF NOT EXISTS messages_thread_idx ON messages(thread_id)`,
	}
	if useHNSW {
		stmts = append(stmts, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS messages_embedding_idx ON messages USING hnsw (embedding vector_cosine_ops)%s`, hnswWith))
	}

	stmts = append(stmts,
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			source TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at BIGINT NOT NULL
		)`,

		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS chunks (
			id TEXT PRIMARY KEY,
			document_id TEXT NOT NULL,
			content TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			embedding %s,
			parent_id TEXT,
			metadata JSONB
		)`, vtype),
		`CREATE INDEX IF NOT EXISTS chunks_document_idx ON chunks(document_id)`,
	)
	if useHNSW {
		stmts = append(stmts, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS chunks_embedding_idx ON chunks USING hnsw (embedding vector_cosine_ops)%s`, hnswWith))
	}
	stmts = append(stmts,
		`CREATE INDEX IF NOT EXISTS chunks_fts_idx ON chunks USING gin(to_tsvector('english', content))`,

		`CREATE TABLE IF NOT EXISTS config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS scheduled_actions (
			id TEXT PRIMARY KEY,
			description TEXT NOT NULL DEFAULT '',
			schedule TEXT NOT NULL DEFAULT '',
			tool_calls TEXT NOT NULL DEFAULT '',
			synthesis_prompt TEXT NOT NULL DEFAULT '',
			next_run BIGINT NOT NULL DEFAULT 0,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			skill_id TEXT NOT NULL DEFAULT '',
			created_at BIGINT NOT NULL DEFAULT 0
		)`,

		`CREATE TABLE IF NOT EXISTS chunk_edges (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			relation TEXT NOT NULL,
			weight REAL NOT NULL,
			description TEXT DEFAULT '',
			UNIQUE(source_id, target_id, relation)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chunk_edges_source ON chunk_edges(source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_chunk_edges_target ON chunk_edges(target_id)`,
		`ALTER TABLE chunk_edges ADD COLUMN IF NOT EXISTS description TEXT DEFAULT ''`,

		`CREATE TABLE IF NOT EXISTS ingest_checkpoints (
			id         TEXT PRIMARY KEY,
			type       TEXT NOT NULL,
			status     TEXT NOT NULL,
			data       BYTEA NOT NULL,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)`,
	)

	for _, stmt := range stmts {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("postgres: init: %w", err)
		}
	}

	s.logger.Info("postgres: init completed", "duration", time.Since(start))
	return nil
}

// Close is a no-op. The caller owns the pool and manages its lifecycle.
func (s *Store) Close() error {
	return nil
}
