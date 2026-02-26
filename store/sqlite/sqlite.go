// Package sqlite implements oasis.Store using pure-Go SQLite
// with in-process brute-force vector search. Zero CGO required.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
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

// nopLogger is a logger that discards all output.
var nopLogger = slog.New(discardHandler{})

type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler            { return d }

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

	s.logger.Info("sqlite: init completed", "duration", time.Since(start))
	return nil
}

// StoreMessage inserts or replaces a message.
func (s *Store) StoreMessage(ctx context.Context, msg oasis.Message) error {
	start := time.Now()
	s.logger.Debug("sqlite: store message", "id", msg.ID, "thread_id", msg.ThreadID, "role", msg.Role, "has_embedding", len(msg.Embedding) > 0)

	var embJSON *string
	if len(msg.Embedding) > 0 {
		v := serializeEmbedding(msg.Embedding)
		embJSON = &v
	}
	var metaJSON *string
	if len(msg.Metadata) > 0 {
		data, _ := json.Marshal(msg.Metadata)
		v := string(data)
		metaJSON = &v
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO messages (id, thread_id, role, content, embedding, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.ThreadID, msg.Role, msg.Content, embJSON, metaJSON, msg.CreatedAt,
	)
	if err != nil {
		s.logger.Error("sqlite: store message failed", "id", msg.ID, "error", err, "duration", time.Since(start))
		return fmt.Errorf("store message: %w", err)
	}
	s.logger.Debug("sqlite: store message ok", "id", msg.ID, "duration", time.Since(start))
	return nil
}

// GetMessages returns the most recent messages for a thread,
// ordered chronologically (oldest first).
func (s *Store) GetMessages(ctx context.Context, threadID string, limit int) ([]oasis.Message, error) {
	start := time.Now()
	s.logger.Debug("sqlite: get messages", "thread_id", threadID, "limit", limit)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, thread_id, role, content, metadata, created_at
		 FROM messages
		 WHERE thread_id = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT ?`,
		threadID, limit,
	)
	if err != nil {
		s.logger.Error("sqlite: get messages failed", "thread_id", threadID, "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer rows.Close()

	var messages []oasis.Message
	for rows.Next() {
		var m oasis.Message
		var metaJSON sql.NullString
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &metaJSON, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		if metaJSON.Valid {
			_ = json.Unmarshal([]byte(metaJSON.String), &m.Metadata)
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}

	// Reverse to chronological order (oldest first).
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	s.logger.Debug("sqlite: get messages ok", "thread_id", threadID, "count", len(messages), "duration", time.Since(start))
	return messages, nil
}

// SearchMessages performs brute-force cosine similarity search over messages.
func (s *Store) SearchMessages(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredMessage, error) {
	start := time.Now()
	s.logger.Debug("sqlite: search messages", "top_k", topK, "embedding_dim", len(embedding))

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, thread_id, role, content, embedding, metadata, created_at
		 FROM messages WHERE embedding IS NOT NULL`,
	)
	if err != nil {
		s.logger.Error("sqlite: search messages failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredMessage
	scanned := 0

	for rows.Next() {
		var m oasis.Message
		var embJSON string
		var metaJSON sql.NullString
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &embJSON, &metaJSON, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		scanned++
		if metaJSON.Valid {
			_ = json.Unmarshal([]byte(metaJSON.String), &m.Metadata)
		}
		stored, err := deserializeEmbedding(embJSON)
		if err != nil {
			continue
		}
		results = append(results, oasis.ScoredMessage{Message: m, Score: cosineSimilarity(embedding, stored)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}
	s.logger.Debug("sqlite: search messages ok", "scanned", scanned, "returned", len(results), "duration", time.Since(start))
	return results, nil
}

// StoreDocument inserts a document and all its chunks in a single transaction.
func (s *Store) StoreDocument(ctx context.Context, doc oasis.Document, chunks []oasis.Chunk) error {
	start := time.Now()
	s.logger.Debug("sqlite: store document", "id", doc.ID, "title", doc.Title, "source", doc.Source, "chunks", len(chunks))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO documents (id, title, source, content, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		doc.ID, doc.Title, doc.Source, doc.Content, doc.CreatedAt,
	)
	if err != nil {
		s.logger.Error("sqlite: insert document failed", "id", doc.ID, "error", err)
		return fmt.Errorf("insert document: %w", err)
	}

	for _, chunk := range chunks {
		var embJSON *string
		if len(chunk.Embedding) > 0 {
			v := serializeEmbedding(chunk.Embedding)
			embJSON = &v
		}
		var parentID *string
		if chunk.ParentID != "" {
			parentID = &chunk.ParentID
		}
		var metaJSON *string
		if chunk.Metadata != nil {
			data, _ := json.Marshal(chunk.Metadata)
			v := string(data)
			metaJSON = &v
		}
		_, err = tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO chunks (id, document_id, parent_id, content, chunk_index, embedding, metadata)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			chunk.ID, chunk.DocumentID, parentID, chunk.Content, chunk.ChunkIndex, embJSON, metaJSON,
		)
		if err != nil {
			s.logger.Error("sqlite: insert chunk failed", "chunk_id", chunk.ID, "doc_id", doc.ID, "error", err)
			return fmt.Errorf("insert chunk: %w", err)
		}

		// Keep FTS index in sync.
		_, _ = tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE chunk_id = ?`, chunk.ID)
		if _, err2 := tx.ExecContext(ctx, `INSERT INTO chunks_fts(chunk_id, content) VALUES (?, ?)`, chunk.ID, chunk.Content); err2 != nil {
			return fmt.Errorf("insert chunk fts: %w", err2)
		}
	}

	if err := tx.Commit(); err != nil {
		s.logger.Error("sqlite: store document commit failed", "id", doc.ID, "error", err)
		return fmt.Errorf("commit tx: %w", err)
	}
	s.logger.Debug("sqlite: store document ok", "id", doc.ID, "chunks", len(chunks), "duration", time.Since(start))
	return nil
}

// ListDocuments returns all documents ordered by creation time (newest first).
func (s *Store) ListDocuments(ctx context.Context, limit int) ([]oasis.Document, error) {
	start := time.Now()
	s.logger.Debug("sqlite: list documents", "limit", limit)

	query := `SELECT id, title, source, content, created_at FROM documents ORDER BY created_at DESC`
	var args []any
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		s.logger.Error("sqlite: list documents failed", "error", err)
		return nil, fmt.Errorf("list documents: %w", err)
	}
	defer rows.Close()

	var docs []oasis.Document
	for rows.Next() {
		var d oasis.Document
		if err := rows.Scan(&d.ID, &d.Title, &d.Source, &d.Content, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		docs = append(docs, d)
	}
	s.logger.Debug("sqlite: list documents ok", "count", len(docs), "duration", time.Since(start))
	return docs, rows.Err()
}

// DeleteDocument removes a document, its chunks, and associated FTS entries.
func (s *Store) DeleteDocument(ctx context.Context, id string) error {
	start := time.Now()
	s.logger.Debug("sqlite: delete document", "id", id)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx,
		`DELETE FROM chunks_fts WHERE chunk_id IN (SELECT id FROM chunks WHERE document_id = ?)`, id)
	if err != nil {
		return fmt.Errorf("delete document fts: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`DELETE FROM chunk_edges WHERE source_id IN (SELECT id FROM chunks WHERE document_id = ?) OR target_id IN (SELECT id FROM chunks WHERE document_id = ?)`, id, id)
	if err != nil {
		return fmt.Errorf("delete document edges: %w", err)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete document chunks: %w", err)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	if err := tx.Commit(); err != nil {
		s.logger.Error("sqlite: delete document commit failed", "id", id, "error", err)
		return err
	}
	s.logger.Debug("sqlite: delete document ok", "id", id, "duration", time.Since(start))
	return nil
}

// safeMetaKey returns true if the key contains only alphanumeric chars and underscores.
// This prevents SQL injection when the key is interpolated into JSON path expressions.
func safeMetaKey(key string) bool {
	for _, c := range key {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return len(key) > 0
}

// buildChunkFilters translates ChunkFilter values into SQL WHERE clauses.
// Returns (whereClause, args, needsDocJoin). The whereClause includes a leading " AND ..."
// for each filter. needsDocJoin is true when any filter references document-level fields.
func buildChunkFilters(filters []oasis.ChunkFilter) (string, []any, bool) {
	if len(filters) == 0 {
		return "", nil, false
	}
	var clauses []string
	var args []any
	needsDocJoin := false

	for _, f := range filters {
		switch {
		case f.Field == "document_id":
			if f.Op == oasis.OpIn {
				ids, ok := f.Value.([]string)
				if !ok || len(ids) == 0 {
					continue
				}
				placeholders := make([]string, len(ids))
				for i, id := range ids {
					placeholders[i] = "?"
					args = append(args, id)
				}
				clauses = append(clauses, "c.document_id IN ("+strings.Join(placeholders, ",")+")") //nolint:gocritic
			} else if f.Op == oasis.OpEq {
				clauses = append(clauses, "c.document_id = ?")
				args = append(args, f.Value)
			} else if f.Op == oasis.OpNeq {
				clauses = append(clauses, "c.document_id != ?")
				args = append(args, f.Value)
			}

		case f.Field == "source":
			if f.Op != oasis.OpEq {
				continue
			}
			needsDocJoin = true
			clauses = append(clauses, "d.source = ?")
			args = append(args, f.Value)

		case f.Field == "created_at":
			needsDocJoin = true
			if f.Op == oasis.OpGt {
				clauses = append(clauses, "d.created_at > ?")
				args = append(args, f.Value)
			} else if f.Op == oasis.OpLt {
				clauses = append(clauses, "d.created_at < ?")
				args = append(args, f.Value)
			}

		case strings.HasPrefix(f.Field, "meta."):
			key := strings.TrimPrefix(f.Field, "meta.")
			if !safeMetaKey(key) {
				continue
			}
			clauses = append(clauses, "json_extract(c.metadata, '$."+key+"') = ?")
			args = append(args, f.Value)
		}
	}

	if len(clauses) == 0 {
		return "", nil, false
	}
	return " AND " + strings.Join(clauses, " AND "), args, needsDocJoin
}

// SearchChunks performs brute-force cosine similarity search over chunks.
func (s *Store) SearchChunks(ctx context.Context, embedding []float32, topK int, filters ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	start := time.Now()
	s.logger.Debug("sqlite: search chunks", "top_k", topK, "embedding_dim", len(embedding), "filters", len(filters))

	whereExtra, filterArgs, needsDocJoin := buildChunkFilters(filters)

	var query string
	if needsDocJoin {
		query = `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.embedding, c.metadata
			FROM chunks c JOIN documents d ON d.id = c.document_id
			WHERE c.embedding IS NOT NULL` + whereExtra
	} else {
		query = `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.embedding, c.metadata
			FROM chunks c WHERE c.embedding IS NOT NULL` + whereExtra
	}

	rows, err := s.db.QueryContext(ctx, query, filterArgs...)
	if err != nil {
		return nil, fmt.Errorf("search chunks: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredChunk
	scanned := 0

	for rows.Next() {
		var c oasis.Chunk
		var parentID sql.NullString
		var embJSON string
		var metaJSON sql.NullString
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &embJSON, &metaJSON); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		scanned++
		if parentID.Valid {
			c.ParentID = parentID.String
		}
		if metaJSON.Valid {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal([]byte(metaJSON.String), c.Metadata)
		}
		stored, err := deserializeEmbedding(embJSON)
		if err != nil {
			continue
		}
		results = append(results, oasis.ScoredChunk{Chunk: c, Score: cosineSimilarity(embedding, stored)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunks: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}
	s.logger.Debug("sqlite: search chunks ok", "scanned", scanned, "returned", len(results), "duration", time.Since(start))
	return results, nil
}

// SearchChunksKeyword performs full-text keyword search over document chunks
// using SQLite FTS5. Results are sorted by relevance (FTS5 rank).
func (s *Store) SearchChunksKeyword(ctx context.Context, query string, topK int, filters ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	start := time.Now()
	s.logger.Debug("sqlite: search chunks keyword", "query", query, "top_k", topK, "filters", len(filters))

	whereExtra, filterArgs, needsDocJoin := buildChunkFilters(filters)

	var q string
	baseArgs := []any{query}
	if needsDocJoin {
		q = `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.metadata, f.rank
			FROM chunks_fts f
			JOIN chunks c ON c.id = f.chunk_id
			JOIN documents d ON d.id = c.document_id
			WHERE chunks_fts MATCH ?` + whereExtra + `
			ORDER BY f.rank LIMIT ?`
	} else {
		q = `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.metadata, f.rank
			FROM chunks_fts f
			JOIN chunks c ON c.id = f.chunk_id
			WHERE chunks_fts MATCH ?` + whereExtra + `
			ORDER BY f.rank LIMIT ?`
	}
	allArgs := append(baseArgs, filterArgs...)
	allArgs = append(allArgs, topK)

	rows, err := s.db.QueryContext(ctx, q, allArgs...)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredChunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID sql.NullString
		var metaJSON sql.NullString
		var rank float64
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &metaJSON, &rank); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		if parentID.Valid {
			c.ParentID = parentID.String
		}
		if metaJSON.Valid {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal([]byte(metaJSON.String), c.Metadata)
		}
		// FTS5 rank is negative (closer to 0 = better). Use -rank as score.
		score := float32(-rank)
		if score < 0 {
			score = 0
		}
		results = append(results, oasis.ScoredChunk{Chunk: c, Score: score})
	}
	s.logger.Debug("sqlite: search chunks keyword ok", "returned", len(results), "duration", time.Since(start))
	return results, rows.Err()
}

// GetChunksByDocument returns all chunks belonging to a specific document,
// including their embeddings. This implements ingest.DocumentChunkLister.
func (s *Store) GetChunksByDocument(ctx context.Context, docID string) ([]oasis.Chunk, error) {
	start := time.Now()
	s.logger.Debug("sqlite: get chunks by document", "doc_id", docID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, document_id, parent_id, content, chunk_index, embedding, metadata
		 FROM chunks WHERE document_id = ? ORDER BY chunk_index`, docID)
	if err != nil {
		return nil, fmt.Errorf("get chunks by document: %w", err)
	}
	defer rows.Close()

	var chunks []oasis.Chunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID sql.NullString
		var embJSON sql.NullString
		var metaJSON sql.NullString
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &embJSON, &metaJSON); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		if parentID.Valid {
			c.ParentID = parentID.String
		}
		if embJSON.Valid {
			c.Embedding, _ = deserializeEmbedding(embJSON.String)
		}
		if metaJSON.Valid {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal([]byte(metaJSON.String), c.Metadata)
		}
		chunks = append(chunks, c)
	}
	s.logger.Debug("sqlite: get chunks by document ok", "doc_id", docID, "count", len(chunks), "duration", time.Since(start))
	return chunks, rows.Err()
}

// GetChunksByIDs returns chunks matching the given IDs.
func (s *Store) GetChunksByIDs(ctx context.Context, ids []string) ([]oasis.Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("sqlite: get chunks by ids", "count", len(ids))

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`SELECT id, document_id, parent_id, content, chunk_index, metadata FROM chunks WHERE id IN (%s)`,
		strings.Join(placeholders, ","))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get chunks by ids: %w", err)
	}
	defer rows.Close()

	var chunks []oasis.Chunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID sql.NullString
		var metaJSON sql.NullString
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &metaJSON); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		if parentID.Valid {
			c.ParentID = parentID.String
		}
		if metaJSON.Valid {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal([]byte(metaJSON.String), c.Metadata)
		}
		chunks = append(chunks, c)
	}
	s.logger.Debug("sqlite: get chunks by ids ok", "requested", len(ids), "returned", len(chunks), "duration", time.Since(start))
	return chunks, rows.Err()
}

// CreateThread inserts a new thread.
func (s *Store) CreateThread(ctx context.Context, thread oasis.Thread) error {
	start := time.Now()
	s.logger.Debug("sqlite: create thread", "id", thread.ID, "chat_id", thread.ChatID, "title", thread.Title)

	var metaJSON *string
	if len(thread.Metadata) > 0 {
		data, _ := json.Marshal(thread.Metadata)
		v := string(data)
		metaJSON = &v
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO threads (id, chat_id, title, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		thread.ID, thread.ChatID, thread.Title, metaJSON, thread.CreatedAt, thread.UpdatedAt,
	)
	if err != nil {
		s.logger.Error("sqlite: create thread failed", "id", thread.ID, "error", err, "duration", time.Since(start))
		return fmt.Errorf("create thread: %w", err)
	}
	s.logger.Debug("sqlite: create thread ok", "id", thread.ID, "duration", time.Since(start))
	return nil
}

// GetThread returns a thread by ID.
func (s *Store) GetThread(ctx context.Context, id string) (oasis.Thread, error) {
	start := time.Now()
	s.logger.Debug("sqlite: get thread", "id", id)

	var t oasis.Thread
	var title sql.NullString
	var metaJSON sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, chat_id, title, metadata, created_at, updated_at FROM threads WHERE id = ?`,
		id,
	).Scan(&t.ID, &t.ChatID, &title, &metaJSON, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		s.logger.Error("sqlite: get thread failed", "id", id, "error", err, "duration", time.Since(start))
		return oasis.Thread{}, fmt.Errorf("get thread: %w", err)
	}
	if title.Valid {
		t.Title = title.String
	}
	if metaJSON.Valid {
		_ = json.Unmarshal([]byte(metaJSON.String), &t.Metadata)
	}
	s.logger.Debug("sqlite: get thread ok", "id", id, "duration", time.Since(start))
	return t, nil
}

// ListThreads returns threads for a chatID, ordered by most recently updated first.
func (s *Store) ListThreads(ctx context.Context, chatID string, limit int) ([]oasis.Thread, error) {
	start := time.Now()
	s.logger.Debug("sqlite: list threads", "chat_id", chatID, "limit", limit)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, chat_id, title, metadata, created_at, updated_at
		 FROM threads WHERE chat_id = ?
		 ORDER BY updated_at DESC
		 LIMIT ?`,
		chatID, limit,
	)
	if err != nil {
		s.logger.Error("sqlite: list threads failed", "chat_id", chatID, "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("list threads: %w", err)
	}
	defer rows.Close()

	var threads []oasis.Thread
	for rows.Next() {
		var t oasis.Thread
		var title sql.NullString
		var metaJSON sql.NullString
		if err := rows.Scan(&t.ID, &t.ChatID, &title, &metaJSON, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan thread: %w", err)
		}
		if title.Valid {
			t.Title = title.String
		}
		if metaJSON.Valid {
			_ = json.Unmarshal([]byte(metaJSON.String), &t.Metadata)
		}
		threads = append(threads, t)
	}
	s.logger.Debug("sqlite: list threads ok", "chat_id", chatID, "count", len(threads), "duration", time.Since(start))
	return threads, rows.Err()
}

// UpdateThread updates a thread's title, metadata, and updated_at.
func (s *Store) UpdateThread(ctx context.Context, thread oasis.Thread) error {
	start := time.Now()
	s.logger.Debug("sqlite: update thread", "id", thread.ID, "title", thread.Title)

	var metaJSON *string
	if len(thread.Metadata) > 0 {
		data, _ := json.Marshal(thread.Metadata)
		v := string(data)
		metaJSON = &v
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE threads SET title=?, metadata=?, updated_at=? WHERE id=?`,
		thread.Title, metaJSON, thread.UpdatedAt, thread.ID,
	)
	if err != nil {
		s.logger.Error("sqlite: update thread failed", "id", thread.ID, "error", err, "duration", time.Since(start))
		return fmt.Errorf("update thread: %w", err)
	}
	s.logger.Debug("sqlite: update thread ok", "id", thread.ID, "duration", time.Since(start))
	return nil
}

// DeleteThread removes a thread and its messages.
func (s *Store) DeleteThread(ctx context.Context, id string) error {
	start := time.Now()
	s.logger.Debug("sqlite: delete thread", "id", id)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE thread_id = ?`, id)
	if err != nil {
		s.logger.Error("sqlite: delete thread messages failed", "id", id, "error", err)
		return fmt.Errorf("delete thread messages: %w", err)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM threads WHERE id = ?`, id)
	if err != nil {
		s.logger.Error("sqlite: delete thread failed", "id", id, "error", err)
		return fmt.Errorf("delete thread: %w", err)
	}
	if err := tx.Commit(); err != nil {
		s.logger.Error("sqlite: delete thread commit failed", "id", id, "error", err)
		return err
	}
	s.logger.Debug("sqlite: delete thread ok", "id", id, "duration", time.Since(start))
	return nil
}

func (s *Store) GetConfig(ctx context.Context, key string) (string, error) {
	start := time.Now()
	s.logger.Debug("sqlite: get config", "key", key)

	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		s.logger.Debug("sqlite: get config not found", "key", key, "duration", time.Since(start))
		return "", nil
	}
	if err != nil {
		s.logger.Error("sqlite: get config failed", "key", key, "error", err, "duration", time.Since(start))
		return "", fmt.Errorf("get config: %w", err)
	}
	s.logger.Debug("sqlite: get config ok", "key", key, "duration", time.Since(start))
	return value, nil
}

func (s *Store) SetConfig(ctx context.Context, key, value string) error {
	start := time.Now()
	s.logger.Debug("sqlite: set config", "key", key)

	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)`,
		key, value,
	)
	if err != nil {
		s.logger.Error("sqlite: set config failed", "key", key, "error", err, "duration", time.Since(start))
		return fmt.Errorf("set config: %w", err)
	}
	s.logger.Debug("sqlite: set config ok", "key", key, "duration", time.Since(start))
	return nil
}

// --- Scheduled Actions ---

func (s *Store) CreateScheduledAction(ctx context.Context, action oasis.ScheduledAction) error {
	start := time.Now()
	s.logger.Debug("sqlite: create scheduled action", "id", action.ID, "description", action.Description, "schedule", action.Schedule)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduled_actions (id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		action.ID, action.Description, action.Schedule, action.ToolCalls,
		action.SynthesisPrompt, action.NextRun, boolToInt(action.Enabled), action.SkillID, action.CreatedAt)
	if err != nil {
		s.logger.Error("sqlite: create scheduled action failed", "id", action.ID, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: create scheduled action ok", "id", action.ID, "duration", time.Since(start))
	return nil
}

func (s *Store) ListScheduledActions(ctx context.Context) ([]oasis.ScheduledAction, error) {
	start := time.Now()
	s.logger.Debug("sqlite: list scheduled actions")

	rows, err := s.db.QueryContext(ctx, `SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at FROM scheduled_actions ORDER BY next_run`)
	if err != nil {
		s.logger.Error("sqlite: list scheduled actions failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	defer rows.Close()
	actions, err := scanScheduledActions(rows)
	if err != nil {
		s.logger.Error("sqlite: list scheduled actions scan failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	s.logger.Debug("sqlite: list scheduled actions ok", "count", len(actions), "duration", time.Since(start))
	return actions, nil
}

func (s *Store) GetDueScheduledActions(ctx context.Context, now int64) ([]oasis.ScheduledAction, error) {
	start := time.Now()
	s.logger.Debug("sqlite: get due scheduled actions", "now", now)

	rows, err := s.db.QueryContext(ctx, `SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at FROM scheduled_actions WHERE enabled = 1 AND next_run <= ?`, now)
	if err != nil {
		s.logger.Error("sqlite: get due scheduled actions failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	defer rows.Close()
	actions, err := scanScheduledActions(rows)
	if err != nil {
		s.logger.Error("sqlite: get due scheduled actions scan failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	s.logger.Debug("sqlite: get due scheduled actions ok", "count", len(actions), "duration", time.Since(start))
	return actions, nil
}

func (s *Store) UpdateScheduledAction(ctx context.Context, action oasis.ScheduledAction) error {
	start := time.Now()
	s.logger.Debug("sqlite: update scheduled action", "id", action.ID, "next_run", action.NextRun, "enabled", action.Enabled)

	_, err := s.db.ExecContext(ctx,
		`UPDATE scheduled_actions SET description=?, schedule=?, tool_calls=?, synthesis_prompt=?, next_run=?, enabled=?, skill_id=? WHERE id=?`,
		action.Description, action.Schedule, action.ToolCalls, action.SynthesisPrompt, action.NextRun, boolToInt(action.Enabled), action.SkillID, action.ID)
	if err != nil {
		s.logger.Error("sqlite: update scheduled action failed", "id", action.ID, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: update scheduled action ok", "id", action.ID, "duration", time.Since(start))
	return nil
}

func (s *Store) UpdateScheduledActionEnabled(ctx context.Context, id string, enabled bool) error {
	start := time.Now()
	s.logger.Debug("sqlite: update scheduled action enabled", "id", id, "enabled", enabled)

	_, err := s.db.ExecContext(ctx, `UPDATE scheduled_actions SET enabled=? WHERE id=?`, boolToInt(enabled), id)
	if err != nil {
		s.logger.Error("sqlite: update scheduled action enabled failed", "id", id, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: update scheduled action enabled ok", "id", id, "enabled", enabled, "duration", time.Since(start))
	return nil
}

func (s *Store) DeleteScheduledAction(ctx context.Context, id string) error {
	start := time.Now()
	s.logger.Debug("sqlite: delete scheduled action", "id", id)

	_, err := s.db.ExecContext(ctx, `DELETE FROM scheduled_actions WHERE id=?`, id)
	if err != nil {
		s.logger.Error("sqlite: delete scheduled action failed", "id", id, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: delete scheduled action ok", "id", id, "duration", time.Since(start))
	return nil
}

func (s *Store) DeleteAllScheduledActions(ctx context.Context) (int, error) {
	start := time.Now()
	s.logger.Debug("sqlite: delete all scheduled actions")

	res, err := s.db.ExecContext(ctx, `DELETE FROM scheduled_actions`)
	if err != nil {
		s.logger.Error("sqlite: delete all scheduled actions failed", "error", err, "duration", time.Since(start))
		return 0, err
	}
	n, _ := res.RowsAffected()
	s.logger.Debug("sqlite: delete all scheduled actions ok", "deleted", n, "duration", time.Since(start))
	return int(n), nil
}

func (s *Store) FindScheduledActionsByDescription(ctx context.Context, pattern string) ([]oasis.ScheduledAction, error) {
	start := time.Now()
	s.logger.Debug("sqlite: find scheduled actions by description", "pattern", pattern)

	rows, err := s.db.QueryContext(ctx, `SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at FROM scheduled_actions WHERE description LIKE ?`, "%"+pattern+"%")
	if err != nil {
		s.logger.Error("sqlite: find scheduled actions by description failed", "pattern", pattern, "error", err, "duration", time.Since(start))
		return nil, err
	}
	defer rows.Close()
	actions, err := scanScheduledActions(rows)
	if err != nil {
		s.logger.Error("sqlite: find scheduled actions by description scan failed", "pattern", pattern, "error", err, "duration", time.Since(start))
		return nil, err
	}
	s.logger.Debug("sqlite: find scheduled actions by description ok", "pattern", pattern, "count", len(actions), "duration", time.Since(start))
	return actions, nil
}

// --- Skills ---

func (s *Store) CreateSkill(ctx context.Context, skill oasis.Skill) error {
	start := time.Now()
	s.logger.Debug("sqlite: create skill", "id", skill.ID, "name", skill.Name, "has_embedding", len(skill.Embedding) > 0)

	var toolsJSON *string
	if len(skill.Tools) > 0 {
		data, _ := json.Marshal(skill.Tools)
		v := string(data)
		toolsJSON = &v
	}
	var tagsJSON *string
	if len(skill.Tags) > 0 {
		data, _ := json.Marshal(skill.Tags)
		v := string(data)
		tagsJSON = &v
	}
	var refsJSON *string
	if len(skill.References) > 0 {
		data, _ := json.Marshal(skill.References)
		v := string(data)
		refsJSON = &v
	}
	var embJSON *string
	if len(skill.Embedding) > 0 {
		v := serializeEmbedding(skill.Embedding)
		embJSON = &v
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO skills (id, name, description, instructions, tools, model, tags, created_by, refs, embedding, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		skill.ID, skill.Name, skill.Description, skill.Instructions,
		toolsJSON, skill.Model, tagsJSON, skill.CreatedBy, refsJSON, embJSON, skill.CreatedAt, skill.UpdatedAt)
	if err != nil {
		s.logger.Error("sqlite: create skill failed", "id", skill.ID, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: create skill ok", "id", skill.ID, "duration", time.Since(start))
	return nil
}

func (s *Store) GetSkill(ctx context.Context, id string) (oasis.Skill, error) {
	start := time.Now()
	s.logger.Debug("sqlite: get skill", "id", id)

	var sk oasis.Skill
	var tools, model, tags, createdBy, refs sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, instructions, tools, model, tags, created_by, refs, created_at, updated_at
		 FROM skills WHERE id = ?`, id,
	).Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &tags, &createdBy, &refs, &sk.CreatedAt, &sk.UpdatedAt)
	if err != nil {
		s.logger.Error("sqlite: get skill failed", "id", id, "error", err, "duration", time.Since(start))
		return oasis.Skill{}, fmt.Errorf("get skill: %w", err)
	}
	if tools.Valid {
		_ = json.Unmarshal([]byte(tools.String), &sk.Tools)
	}
	if model.Valid {
		sk.Model = model.String
	}
	if tags.Valid {
		_ = json.Unmarshal([]byte(tags.String), &sk.Tags)
	}
	if createdBy.Valid {
		sk.CreatedBy = createdBy.String
	}
	if refs.Valid {
		_ = json.Unmarshal([]byte(refs.String), &sk.References)
	}
	s.logger.Debug("sqlite: get skill ok", "id", id, "name", sk.Name, "duration", time.Since(start))
	return sk, nil
}

func (s *Store) ListSkills(ctx context.Context) ([]oasis.Skill, error) {
	start := time.Now()
	s.logger.Debug("sqlite: list skills")

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, instructions, tools, model, tags, created_by, refs, created_at, updated_at
		 FROM skills ORDER BY created_at`)
	if err != nil {
		s.logger.Error("sqlite: list skills failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("list skills: %w", err)
	}
	defer rows.Close()

	var skills []oasis.Skill
	for rows.Next() {
		var sk oasis.Skill
		var tools, model, tags, createdBy, refs sql.NullString
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &tags, &createdBy, &refs, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		if tools.Valid {
			_ = json.Unmarshal([]byte(tools.String), &sk.Tools)
		}
		if model.Valid {
			sk.Model = model.String
		}
		if tags.Valid {
			_ = json.Unmarshal([]byte(tags.String), &sk.Tags)
		}
		if createdBy.Valid {
			sk.CreatedBy = createdBy.String
		}
		if refs.Valid {
			_ = json.Unmarshal([]byte(refs.String), &sk.References)
		}
		skills = append(skills, sk)
	}
	s.logger.Debug("sqlite: list skills ok", "count", len(skills), "duration", time.Since(start))
	return skills, rows.Err()
}

func (s *Store) UpdateSkill(ctx context.Context, skill oasis.Skill) error {
	start := time.Now()
	s.logger.Debug("sqlite: update skill", "id", skill.ID, "name", skill.Name, "has_embedding", len(skill.Embedding) > 0)

	var toolsJSON *string
	if len(skill.Tools) > 0 {
		data, _ := json.Marshal(skill.Tools)
		v := string(data)
		toolsJSON = &v
	}
	var tagsJSON *string
	if len(skill.Tags) > 0 {
		data, _ := json.Marshal(skill.Tags)
		v := string(data)
		tagsJSON = &v
	}
	var refsJSON *string
	if len(skill.References) > 0 {
		data, _ := json.Marshal(skill.References)
		v := string(data)
		refsJSON = &v
	}
	var embJSON *string
	if len(skill.Embedding) > 0 {
		v := serializeEmbedding(skill.Embedding)
		embJSON = &v
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE skills SET name=?, description=?, instructions=?, tools=?, model=?, tags=?, created_by=?, refs=?, embedding=?, updated_at=? WHERE id=?`,
		skill.Name, skill.Description, skill.Instructions, toolsJSON, skill.Model, tagsJSON, skill.CreatedBy, refsJSON, embJSON, skill.UpdatedAt, skill.ID)
	if err != nil {
		s.logger.Error("sqlite: update skill failed", "id", skill.ID, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: update skill ok", "id", skill.ID, "duration", time.Since(start))
	return nil
}

func (s *Store) DeleteSkill(ctx context.Context, id string) error {
	start := time.Now()
	s.logger.Debug("sqlite: delete skill", "id", id)

	_, err := s.db.ExecContext(ctx, `DELETE FROM skills WHERE id=?`, id)
	if err != nil {
		s.logger.Error("sqlite: delete skill failed", "id", id, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: delete skill ok", "id", id, "duration", time.Since(start))
	return nil
}

func (s *Store) SearchSkills(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredSkill, error) {
	start := time.Now()
	s.logger.Debug("sqlite: search skills", "top_k", topK, "embedding_dim", len(embedding))

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, instructions, tools, model, tags, created_by, refs, embedding, created_at, updated_at
		 FROM skills WHERE embedding IS NOT NULL`)
	if err != nil {
		s.logger.Error("sqlite: search skills failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("search skills: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredSkill
	scanned := 0

	for rows.Next() {
		var sk oasis.Skill
		var tools, model, tags, createdBy, refs sql.NullString
		var embJSON string
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &tags, &createdBy, &refs, &embJSON, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		scanned++
		if tools.Valid {
			_ = json.Unmarshal([]byte(tools.String), &sk.Tools)
		}
		if model.Valid {
			sk.Model = model.String
		}
		if tags.Valid {
			_ = json.Unmarshal([]byte(tags.String), &sk.Tags)
		}
		if createdBy.Valid {
			sk.CreatedBy = createdBy.String
		}
		if refs.Valid {
			_ = json.Unmarshal([]byte(refs.String), &sk.References)
		}
		stored, err := deserializeEmbedding(embJSON)
		if err != nil {
			continue
		}
		results = append(results, oasis.ScoredSkill{Skill: sk, Score: cosineSimilarity(embedding, stored)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate skills: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}
	s.logger.Debug("sqlite: search skills ok", "scanned", scanned, "returned", len(results), "duration", time.Since(start))
	return results, nil
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

func scanScheduledActions(rows *sql.Rows) ([]oasis.ScheduledAction, error) {
	var actions []oasis.ScheduledAction
	for rows.Next() {
		var a oasis.ScheduledAction
		var enabled int
		if err := rows.Scan(&a.ID, &a.Description, &a.Schedule, &a.ToolCalls, &a.SynthesisPrompt, &a.NextRun, &enabled, &a.SkillID, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Enabled = enabled != 0
		actions = append(actions, a)
	}
	return actions, rows.Err()
}

// --- GraphStore ---

func (s *Store) StoreEdges(ctx context.Context, edges []oasis.ChunkEdge) error {
	if len(edges) == 0 {
		return nil
	}
	start := time.Now()
	s.logger.Debug("sqlite: store edges", "count", len(edges))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, e := range edges {
		_, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO chunk_edges (id, source_id, target_id, relation, weight, description)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			e.ID, e.SourceID, e.TargetID, string(e.Relation), e.Weight, e.Description,
		)
		if err != nil {
			s.logger.Error("sqlite: store edge failed", "id", e.ID, "error", err)
			return fmt.Errorf("store edge: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		s.logger.Error("sqlite: store edges commit failed", "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: store edges ok", "count", len(edges), "duration", time.Since(start))
	return nil
}

func (s *Store) GetEdges(ctx context.Context, chunkIDs []string) ([]oasis.ChunkEdge, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("sqlite: get edges", "chunk_count", len(chunkIDs))

	placeholders := make([]string, len(chunkIDs))
	args := make([]any, len(chunkIDs))
	for i, id := range chunkIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT id, source_id, target_id, relation, weight, description FROM chunk_edges WHERE source_id IN (%s)`,
		strings.Join(placeholders, ","),
	)
	edges, err := s.scanEdges(ctx, query, args)
	if err != nil {
		s.logger.Error("sqlite: get edges failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	s.logger.Debug("sqlite: get edges ok", "returned", len(edges), "duration", time.Since(start))
	return edges, nil
}

func (s *Store) GetIncomingEdges(ctx context.Context, chunkIDs []string) ([]oasis.ChunkEdge, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("sqlite: get incoming edges", "chunk_count", len(chunkIDs))

	placeholders := make([]string, len(chunkIDs))
	args := make([]any, len(chunkIDs))
	for i, id := range chunkIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT id, source_id, target_id, relation, weight, description FROM chunk_edges WHERE target_id IN (%s)`,
		strings.Join(placeholders, ","),
	)
	edges, err := s.scanEdges(ctx, query, args)
	if err != nil {
		s.logger.Error("sqlite: get incoming edges failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	s.logger.Debug("sqlite: get incoming edges ok", "returned", len(edges), "duration", time.Since(start))
	return edges, nil
}

func (s *Store) PruneOrphanEdges(ctx context.Context) (int, error) {
	start := time.Now()
	s.logger.Debug("sqlite: prune orphan edges")

	result, err := s.db.ExecContext(ctx,
		`DELETE FROM chunk_edges WHERE source_id NOT IN (SELECT id FROM chunks) OR target_id NOT IN (SELECT id FROM chunks)`)
	if err != nil {
		s.logger.Error("sqlite: prune orphan edges failed", "error", err, "duration", time.Since(start))
		return 0, fmt.Errorf("prune orphan edges: %w", err)
	}
	n, _ := result.RowsAffected()
	s.logger.Debug("sqlite: prune orphan edges ok", "deleted", n, "duration", time.Since(start))
	return int(n), nil
}

func (s *Store) scanEdges(ctx context.Context, query string, args []any) ([]oasis.ChunkEdge, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query edges: %w", err)
	}
	defer rows.Close()

	var edges []oasis.ChunkEdge
	for rows.Next() {
		var e oasis.ChunkEdge
		var rel string
		if err := rows.Scan(&e.ID, &e.SourceID, &e.TargetID, &rel, &e.Weight, &e.Description); err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}
		e.Relation = oasis.RelationType(rel)
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// --- Vector math ---

// cosineSimilarity computes the cosine similarity between two vectors.
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

// serializeEmbedding converts []float32 to a JSON array string.
func serializeEmbedding(embedding []float32) string {
	data, _ := json.Marshal(embedding)
	return string(data)
}

// deserializeEmbedding parses a JSON array string back to []float32.
func deserializeEmbedding(s string) ([]float32, error) {
	var v []float32
	err := json.Unmarshal([]byte(s), &v)
	return v, err
}
