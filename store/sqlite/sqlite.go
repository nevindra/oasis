// Package sqlite implements oasis.Store using pure-Go SQLite
// with in-process brute-force vector search. Zero CGO required.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/nevindra/oasis"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// StoreOption configures a SQLite Store.
type StoreOption func(*Store)

// WithLogger sets a structured logger for the store.
// When set, the store emits debug logs for every operation including
// timing, row counts, and key parameters. If not set, no logs are emitted.
func WithLogger(l *slog.Logger) StoreOption {
	return func(s *Store) { s.logger = l }
}

// Store implements oasis.Store backed by a local SQLite file.
// Embeddings are stored as JSON text and vector search is done
// in-process using brute-force cosine similarity.
type Store struct {
	db     *sql.DB
	logger *slog.Logger
}

var _ oasis.Store = (*Store)(nil)
var _ oasis.KeywordSearcher = (*Store)(nil)
var _ oasis.GraphStore = (*Store)(nil)
var _ oasis.CheckpointStore = (*Store)(nil)

// nopLogger is a logger that discards all output.
var nopLogger = slog.New(discardHandler{})

type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler           { return d }

// New creates a Store using a local SQLite file at dbPath.
// It opens a single shared connection pool with SetMaxOpenConns(1) so that
// all goroutines serialize through one connection, eliminating SQLITE_BUSY
// errors caused by concurrent writers opening independent connections.
func New(dbPath string, opts ...StoreOption) *Store {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		// sql.Open only fails when the driver is not registered; with the
		// blank import above that never happens.
		panic(fmt.Sprintf("sqlite: open driver: %v", err))
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, logger: nopLogger}
	for _, o := range opts {
		o(s)
	}
	s.logger.Debug("sqlite: store opened", "path", dbPath)
	return s
}

// Init creates all required tables.
func (s *Store) Init(ctx context.Context) error {
	start := time.Now()
	s.logger.Debug("sqlite: init started")
	tables := []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			source TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS chunks (
			id TEXT PRIMARY KEY,
			document_id TEXT NOT NULL,
			content TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			embedding TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS threads (
			id TEXT PRIMARY KEY,
			chat_id TEXT NOT NULL,
			title TEXT,
			metadata TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			thread_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			embedding TEXT,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	}

	for _, ddl := range tables {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
	}

	// Scheduled actions
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS scheduled_actions (
		id TEXT PRIMARY KEY,
		description TEXT,
		schedule TEXT,
		tool_calls TEXT,
		synthesis_prompt TEXT,
		next_run INTEGER,
		enabled INTEGER DEFAULT 1,
		skill_id TEXT,
		created_at INTEGER
	)`)
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// Skills
	_, err = s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS skills (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL,
		instructions TEXT NOT NULL,
		tools TEXT,
		model TEXT,
		tags TEXT,
		created_by TEXT,
		refs TEXT,
		embedding TEXT,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// Migrations (best-effort, silent fail if already applied)
	_, _ = s.db.ExecContext(ctx, "ALTER TABLE scheduled_actions ADD COLUMN skill_id TEXT")
	_, _ = s.db.ExecContext(ctx, "ALTER TABLE chunks ADD COLUMN parent_id TEXT")
	_, _ = s.db.ExecContext(ctx, "ALTER TABLE chunks ADD COLUMN metadata TEXT")
	_, _ = s.db.ExecContext(ctx, "ALTER TABLE messages ADD COLUMN metadata TEXT")

	// Migrate conversations → threads
	_, _ = s.db.ExecContext(ctx, "ALTER TABLE conversations RENAME TO threads")
	_, _ = s.db.ExecContext(ctx, "ALTER TABLE threads ADD COLUMN title TEXT")
	_, _ = s.db.ExecContext(ctx, "ALTER TABLE threads ADD COLUMN metadata TEXT")
	_, _ = s.db.ExecContext(ctx, "ALTER TABLE threads ADD COLUMN updated_at INTEGER")
	_, _ = s.db.ExecContext(ctx, "UPDATE threads SET updated_at = created_at WHERE updated_at IS NULL")
	_, _ = s.db.ExecContext(ctx, "ALTER TABLE messages RENAME COLUMN conversation_id TO thread_id")

	// Indexes on frequently queried columns.
	_, _ = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(thread_id)`)
	_, _ = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_threads_chat ON threads(chat_id)`)
	_, _ = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_chunks_document ON chunks(document_id)`)

	// FTS5 full-text index for keyword search over chunks.
	_, _ = s.db.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(chunk_id UNINDEXED, content)`)

	// Graph RAG edge table.
	_, _ = s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS chunk_edges (
		id TEXT PRIMARY KEY,
		source_id TEXT NOT NULL,
		target_id TEXT NOT NULL,
		relation TEXT NOT NULL,
		weight REAL NOT NULL,
		description TEXT DEFAULT '',
		UNIQUE(source_id, target_id, relation)
	)`)
	_, _ = s.db.ExecContext(ctx, `ALTER TABLE chunk_edges ADD COLUMN description TEXT DEFAULT ''`)
	_, _ = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_chunk_edges_source ON chunk_edges(source_id)`)
	_, _ = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_chunk_edges_target ON chunk_edges(target_id)`)

	// Ingest checkpoint table for retry/resume support.
	_, _ = s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ingest_checkpoints (
		id         TEXT PRIMARY KEY,
		type       TEXT NOT NULL,
		status     TEXT NOT NULL,
		data       BLOB NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`)

	s.logger.Info("sqlite: init completed", "duration", time.Since(start))
	return nil
}

// DB returns the underlying *sql.DB for sharing with MemoryStore.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	s.logger.Debug("sqlite: closing store")
	err := s.db.Close()
	if err != nil {
		s.logger.Error("sqlite: close failed", "error", err)
	}
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
