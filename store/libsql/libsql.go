// Package libsql implements oasis.Store using libSQL (SQLite-compatible)
// with DiskANN vector extensions for Turso.
package libsql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nevindra/oasis"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// Store implements oasis.Store backed by libSQL / Turso.
//
// It uses fresh connections per call to avoid STREAM_EXPIRED errors
// on remote Turso databases.
type Store struct {
	dbPath string
	dbURL  string // for Turso remote
	token  string // for Turso auth
	cfg    config
}

type config struct {
	embeddingDimension int // default: 1536
}

// Option configures a libSQL Store.
type Option func(*config)

// WithEmbeddingDimension sets the vector dimension for F32_BLOB columns.
// Default is 1536. Only affects new table creation (no ALTER on existing tables).
func WithEmbeddingDimension(dim int) Option {
	return func(c *config) { c.embeddingDimension = dim }
}

// compile-time checks
var _ oasis.Store = (*Store)(nil)
var _ oasis.KeywordSearcher = (*Store)(nil)
var _ oasis.GraphStore = (*Store)(nil)

// New creates a Store that uses a local SQLite file at dbPath.
func New(dbPath string, opts ...Option) *Store {
	cfg := config{embeddingDimension: 1536}
	for _, o := range opts {
		o(&cfg)
	}
	return &Store{dbPath: dbPath, cfg: cfg}
}

// NewRemote creates a Store that connects to a remote Turso database.
func NewRemote(url, token string, opts ...Option) *Store {
	cfg := config{embeddingDimension: 1536}
	for _, o := range opts {
		o(&cfg)
	}
	return &Store{dbURL: url, token: token, cfg: cfg}
}

// openDB opens a fresh database connection.
// For local mode it uses the pure-Go modernc.org/sqlite driver.
// For remote Turso, it uses the libsql:// URL scheme (requires the
// go-libsql driver in production; this implementation uses the sqlite
// driver for local/test use).
func (s *Store) openDB() (*sql.DB, error) {
	if s.dbURL != "" {
		// Remote Turso: use the URL with auth token as query param.
		// In production you'd use github.com/tursodatabase/go-libsql connector.
		// For now, this returns an error since pure-Go sqlite can't talk to Turso.
		return nil, fmt.Errorf("remote Turso connections require the go-libsql driver; use New() for local databases")
	}
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
}

// Init creates all required tables and attempts to create vector indexes.
// Vector index creation errors are silently ignored because local SQLite
// does not support libsql vector extensions.
func (s *Store) Init(ctx context.Context) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	dim := s.cfg.embeddingDimension
	tables := []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			source TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS chunks (
			id TEXT PRIMARY KEY,
			document_id TEXT NOT NULL,
			content TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			embedding F32_BLOB(%d)
		)`, dim),
		`CREATE TABLE IF NOT EXISTS threads (
			id TEXT PRIMARY KEY,
			chat_id TEXT NOT NULL,
			title TEXT,
			metadata TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			thread_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			embedding F32_BLOB(%d),
			created_at INTEGER NOT NULL
		)`, dim),
		`CREATE TABLE IF NOT EXISTS config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	}

	for _, ddl := range tables {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
	}

	// Scheduled actions
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS scheduled_actions (
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
	_, err = db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS skills (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL,
		instructions TEXT NOT NULL,
		tools TEXT,
		model TEXT,
		embedding F32_BLOB(%d),
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`, dim))
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// Migrations (best-effort, silent fail if already applied)
	_, _ = db.ExecContext(ctx, "ALTER TABLE scheduled_actions ADD COLUMN skill_id TEXT")
	_, _ = db.ExecContext(ctx, "ALTER TABLE chunks ADD COLUMN parent_id TEXT")
	_, _ = db.ExecContext(ctx, "ALTER TABLE chunks ADD COLUMN metadata TEXT")
	_, _ = db.ExecContext(ctx, "ALTER TABLE conversations RENAME TO threads")
	_, _ = db.ExecContext(ctx, "ALTER TABLE threads ADD COLUMN title TEXT")
	_, _ = db.ExecContext(ctx, "ALTER TABLE threads ADD COLUMN metadata TEXT")
	_, _ = db.ExecContext(ctx, "ALTER TABLE threads ADD COLUMN updated_at INTEGER")
	_, _ = db.ExecContext(ctx, "UPDATE threads SET updated_at = created_at WHERE updated_at IS NULL")
	_, _ = db.ExecContext(ctx, "ALTER TABLE messages RENAME COLUMN conversation_id TO thread_id")

	// FTS5 full-text index for keyword search over chunks.
	_, _ = db.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(chunk_id UNINDEXED, content)`)

	// Graph RAG edge table.
	_, _ = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS chunk_edges (
		id TEXT PRIMARY KEY,
		source_id TEXT NOT NULL,
		target_id TEXT NOT NULL,
		relation TEXT NOT NULL,
		weight REAL NOT NULL,
		UNIQUE(source_id, target_id, relation)
	)`)
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_chunk_edges_source ON chunk_edges(source_id)`)
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_chunk_edges_target ON chunk_edges(target_id)`)

	// Vector indexes -- these only work on real libsql, not standard SQLite.
	// We attempt creation but ignore errors.
	vectorIndexes := []string{
		`CREATE INDEX IF NOT EXISTS chunks_vector_idx ON chunks (libsql_vector_idx(embedding))`,
		`CREATE INDEX IF NOT EXISTS messages_vector_idx ON messages (libsql_vector_idx(embedding))`,
		`CREATE INDEX IF NOT EXISTS skills_vector_idx ON skills (libsql_vector_idx(embedding))`,
	}
	for _, ddl := range vectorIndexes {
		_, _ = db.ExecContext(ctx, ddl) // best-effort
	}

	return nil
}

// StoreMessage inserts or replaces a message. If the message's Embedding
// field is non-nil, it is stored using the vector() function; otherwise
// the embedding column is set to NULL.
func (s *Store) StoreMessage(ctx context.Context, msg oasis.Message) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	if len(msg.Embedding) > 0 {
		embJSON := serializeEmbedding(msg.Embedding)
		_, err = db.ExecContext(ctx,
			`INSERT OR REPLACE INTO messages (id, thread_id, role, content, embedding, created_at)
			 VALUES (?, ?, ?, ?, vector(?), ?)`,
			msg.ID, msg.ThreadID, msg.Role, msg.Content, embJSON, msg.CreatedAt,
		)
	} else {
		_, err = db.ExecContext(ctx,
			`INSERT OR REPLACE INTO messages (id, thread_id, role, content, embedding, created_at)
			 VALUES (?, ?, ?, ?, NULL, ?)`,
			msg.ID, msg.ThreadID, msg.Role, msg.Content, msg.CreatedAt,
		)
	}
	if err != nil {
		return fmt.Errorf("store message: %w", err)
	}
	return nil
}

// GetMessages returns the most recent messages for a thread,
// ordered chronologically (oldest first).
func (s *Store) GetMessages(ctx context.Context, threadID string, limit int) ([]oasis.Message, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT id, thread_id, role, content, created_at
		 FROM messages
		 WHERE thread_id = ?
		 ORDER BY created_at DESC
		 LIMIT ?`,
		threadID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer rows.Close()

	var messages []oasis.Message
	for rows.Next() {
		var m oasis.Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
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

	return messages, nil
}

// SearchMessages performs vector similarity search over messages using
// the libsql vector_top_k function. Cosine similarity scores are computed
// via vector_distance_cos on the topK results.
func (s *Store) SearchMessages(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredMessage, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	embJSON := serializeEmbedding(embedding)
	rows, err := db.QueryContext(ctx,
		`SELECT m.id, m.thread_id, m.role, m.content, m.created_at,
		        1.0 - vector_distance_cos(m.embedding, vector(?)) AS score
		 FROM vector_top_k('messages_vector_idx', vector(?), ?) AS v
		 JOIN messages AS m ON m.rowid = v.id`,
		embJSON, embJSON, topK,
	)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()

	var messages []oasis.ScoredMessage
	for rows.Next() {
		var m oasis.Message
		var score float32
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.CreatedAt, &score); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		if score < 0 {
			score = 0
		}
		messages = append(messages, oasis.ScoredMessage{Message: m, Score: score})
	}
	return messages, rows.Err()
}

// StoreDocument inserts a document and all its chunks in a single transaction.
func (s *Store) StoreDocument(ctx context.Context, doc oasis.Document, chunks []oasis.Chunk) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
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
		return fmt.Errorf("insert document: %w", err)
	}

	for _, chunk := range chunks {
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
		if len(chunk.Embedding) > 0 {
			embJSON := serializeEmbedding(chunk.Embedding)
			_, err = tx.ExecContext(ctx,
				`INSERT OR REPLACE INTO chunks (id, document_id, parent_id, content, chunk_index, embedding, metadata)
				 VALUES (?, ?, ?, ?, ?, vector(?), ?)`,
				chunk.ID, chunk.DocumentID, parentID, chunk.Content, chunk.ChunkIndex, embJSON, metaJSON,
			)
		} else {
			_, err = tx.ExecContext(ctx,
				`INSERT OR REPLACE INTO chunks (id, document_id, parent_id, content, chunk_index, embedding, metadata)
				 VALUES (?, ?, ?, ?, ?, NULL, ?)`,
				chunk.ID, chunk.DocumentID, parentID, chunk.Content, chunk.ChunkIndex, metaJSON,
			)
		}
		if err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}

		// Keep FTS index in sync.
		_, _ = tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE chunk_id = ?`, chunk.ID)
		if _, err2 := tx.ExecContext(ctx, `INSERT INTO chunks_fts(chunk_id, content) VALUES (?, ?)`, chunk.ID, chunk.Content); err2 != nil {
			return fmt.Errorf("insert chunk fts: %w", err2)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// ListDocuments returns the most recent documents, ordered by creation time
// (newest first), limited by limit.
func (s *Store) ListDocuments(ctx context.Context, limit int) ([]oasis.Document, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT id, title, source, content, created_at
		 FROM documents
		 ORDER BY created_at DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
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
	return docs, rows.Err()
}

// DeleteDocument removes a document, its chunks, and associated FTS entries
// in a single transaction.
func (s *Store) DeleteDocument(ctx context.Context, id string) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE chunk_id IN (SELECT id FROM chunks WHERE document_id = ?)`, id)
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
	return tx.Commit()
}

// matchesFilters checks if a scored chunk matches all filters.
// docMap is lazily populated when document-level filters are encountered.
func matchesFilters(chunk oasis.ScoredChunk, filters []oasis.ChunkFilter, docMap map[string]oasis.Document) bool {
	for _, f := range filters {
		switch {
		case f.Field == "document_id":
			if f.Op == oasis.OpIn {
				ids, _ := f.Value.([]string)
				found := false
				for _, id := range ids {
					if chunk.DocumentID == id {
						found = true
						break
					}
				}
				if !found {
					return false
				}
			} else if f.Op == oasis.OpEq {
				if chunk.DocumentID != f.Value {
					return false
				}
			}

		case f.Field == "source":
			doc, ok := docMap[chunk.DocumentID]
			if !ok || doc.Source != f.Value {
				return false
			}

		case f.Field == "created_at":
			doc, ok := docMap[chunk.DocumentID]
			if !ok {
				return false
			}
			ts, _ := f.Value.(int64)
			if f.Op == oasis.OpGt && doc.CreatedAt <= ts {
				return false
			}
			if f.Op == oasis.OpLt && doc.CreatedAt >= ts {
				return false
			}

		case strings.HasPrefix(f.Field, "meta."):
			if chunk.Metadata == nil {
				return false
			}
			key := strings.TrimPrefix(f.Field, "meta.")
			val, _ := f.Value.(string)
			switch key {
			case "section_heading":
				if chunk.Metadata.SectionHeading != val {
					return false
				}
			case "source_url":
				if chunk.Metadata.SourceURL != val {
					return false
				}
			case "page_number":
				if fmt.Sprint(chunk.Metadata.PageNumber) != val {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

// needsDocLookup returns true if any filter references document-level fields.
func needsDocLookup(filters []oasis.ChunkFilter) bool {
	for _, f := range filters {
		if f.Field == "source" || f.Field == "created_at" {
			return true
		}
	}
	return false
}

// loadDocMap fetches documents for the given IDs and returns a map.
func loadDocMap(ctx context.Context, db *sql.DB, ids []string) map[string]oasis.Document {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := db.QueryContext(ctx,
		"SELECT id, title, source, created_at FROM documents WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	m := make(map[string]oasis.Document)
	for rows.Next() {
		var d oasis.Document
		if err := rows.Scan(&d.ID, &d.Title, &d.Source, &d.CreatedAt); err != nil {
			continue
		}
		m[d.ID] = d
	}
	return m
}

// SearchChunks performs vector similarity search over document chunks
// using the libsql vector_top_k function. Cosine similarity scores are
// computed via vector_distance_cos on the topK results.
// When filters are present, overfetches topK*3 and filters in-memory since
// vector_top_k does not support WHERE clauses.
func (s *Store) SearchChunks(ctx context.Context, embedding []float32, topK int, filters ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	fetchK := topK
	if len(filters) > 0 {
		fetchK = topK * 3
	}

	embJSON := serializeEmbedding(embedding)
	rows, err := db.QueryContext(ctx,
		`SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.metadata,
		        1.0 - vector_distance_cos(c.embedding, vector(?)) AS score
		 FROM vector_top_k('chunks_vector_idx', vector(?), ?) AS v
		 JOIN chunks AS c ON c.rowid = v.id`,
		embJSON, embJSON, fetchK,
	)
	if err != nil {
		return nil, fmt.Errorf("search chunks: %w", err)
	}
	defer rows.Close()

	var chunks []oasis.ScoredChunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID sql.NullString
		var metaJSON sql.NullString
		var score float32
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &metaJSON, &score); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		if parentID.Valid {
			c.ParentID = parentID.String
		}
		if metaJSON.Valid {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal([]byte(metaJSON.String), c.Metadata)
		}
		if score < 0 {
			score = 0
		}
		chunks = append(chunks, oasis.ScoredChunk{Chunk: c, Score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(filters) == 0 {
		return chunks, nil
	}

	// Load document map if needed for doc-level filters.
	var docMap map[string]oasis.Document
	if needsDocLookup(filters) {
		docIDs := make([]string, 0, len(chunks))
		seen := make(map[string]bool)
		for _, sc := range chunks {
			if !seen[sc.DocumentID] {
				seen[sc.DocumentID] = true
				docIDs = append(docIDs, sc.DocumentID)
			}
		}
		docMap = loadDocMap(ctx, db, docIDs)
	}

	var filtered []oasis.ScoredChunk
	for _, sc := range chunks {
		if matchesFilters(sc, filters, docMap) {
			filtered = append(filtered, sc)
			if len(filtered) >= topK {
				break
			}
		}
	}
	return filtered, nil
}

// SearchChunksKeyword performs full-text keyword search over document chunks
// using FTS5. Results are sorted by relevance.
func (s *Store) SearchChunksKeyword(ctx context.Context, query string, topK int, filters ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Build optional WHERE clauses for chunk-level filters (document_id).
	// Doc-level filters are applied post-query.
	var extraWhere string
	var extraArgs []any
	for _, f := range filters {
		if f.Field == "document_id" && f.Op == oasis.OpIn {
			ids, ok := f.Value.([]string)
			if !ok || len(ids) == 0 {
				continue
			}
			placeholders := make([]string, len(ids))
			for i, id := range ids {
				placeholders[i] = "?"
				extraArgs = append(extraArgs, id)
			}
			extraWhere += " AND c.document_id IN (" + strings.Join(placeholders, ",") + ")"
		}
	}

	fetchK := topK
	if needsDocLookup(filters) {
		fetchK = topK * 3
	}

	q := `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.metadata, f.rank
		 FROM chunks_fts f
		 JOIN chunks c ON c.id = f.chunk_id
		 WHERE chunks_fts MATCH ?` + extraWhere + `
		 ORDER BY f.rank
		 LIMIT ?`
	allArgs := []any{query}
	allArgs = append(allArgs, extraArgs...)
	allArgs = append(allArgs, fetchK)

	rows, err := db.QueryContext(ctx, q, allArgs...)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Apply remaining doc-level filters post-query.
	if !needsDocLookup(filters) {
		return results, nil
	}

	docIDs := make([]string, 0, len(results))
	seen := make(map[string]bool)
	for _, sc := range results {
		if !seen[sc.DocumentID] {
			seen[sc.DocumentID] = true
			docIDs = append(docIDs, sc.DocumentID)
		}
	}
	docMap := loadDocMap(ctx, db, docIDs)

	var filtered []oasis.ScoredChunk
	for _, sc := range results {
		if matchesFilters(sc, filters, docMap) {
			filtered = append(filtered, sc)
			if len(filtered) >= topK {
				break
			}
		}
	}
	return filtered, nil
}

// GetChunksByIDs returns chunks matching the given IDs.
func (s *Store) GetChunksByIDs(ctx context.Context, ids []string) ([]oasis.Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`SELECT id, document_id, parent_id, content, chunk_index, metadata FROM chunks WHERE id IN (%s)`,
		strings.Join(placeholders, ","))

	rows, err := db.QueryContext(ctx, query, args...)
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
	return chunks, rows.Err()
}

// CreateThread inserts a new thread.
func (s *Store) CreateThread(ctx context.Context, thread oasis.Thread) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var metaJSON *string
	if len(thread.Metadata) > 0 {
		data, _ := json.Marshal(thread.Metadata)
		v := string(data)
		metaJSON = &v
	}

	_, err = db.ExecContext(ctx,
		`INSERT INTO threads (id, chat_id, title, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		thread.ID, thread.ChatID, thread.Title, metaJSON, thread.CreatedAt, thread.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create thread: %w", err)
	}
	return nil
}

// GetThread returns a thread by ID.
func (s *Store) GetThread(ctx context.Context, id string) (oasis.Thread, error) {
	db, err := s.openDB()
	if err != nil {
		return oasis.Thread{}, err
	}
	defer db.Close()

	var t oasis.Thread
	var title sql.NullString
	var metaJSON sql.NullString
	err = db.QueryRowContext(ctx,
		`SELECT id, chat_id, title, metadata, created_at, updated_at FROM threads WHERE id = ?`,
		id,
	).Scan(&t.ID, &t.ChatID, &title, &metaJSON, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return oasis.Thread{}, fmt.Errorf("get thread: %w", err)
	}
	if title.Valid {
		t.Title = title.String
	}
	if metaJSON.Valid {
		_ = json.Unmarshal([]byte(metaJSON.String), &t.Metadata)
	}
	return t, nil
}

// ListThreads returns threads for a chatID, ordered by most recently updated first.
func (s *Store) ListThreads(ctx context.Context, chatID string, limit int) ([]oasis.Thread, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT id, chat_id, title, metadata, created_at, updated_at
		 FROM threads WHERE chat_id = ?
		 ORDER BY updated_at DESC
		 LIMIT ?`,
		chatID, limit,
	)
	if err != nil {
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
	return threads, rows.Err()
}

// UpdateThread updates a thread's title, metadata, and updated_at.
func (s *Store) UpdateThread(ctx context.Context, thread oasis.Thread) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var metaJSON *string
	if len(thread.Metadata) > 0 {
		data, _ := json.Marshal(thread.Metadata)
		v := string(data)
		metaJSON = &v
	}

	_, err = db.ExecContext(ctx,
		`UPDATE threads SET title=?, metadata=?, updated_at=? WHERE id=?`,
		thread.Title, metaJSON, thread.UpdatedAt, thread.ID,
	)
	if err != nil {
		return fmt.Errorf("update thread: %w", err)
	}
	return nil
}

// DeleteThread removes a thread and its messages.
func (s *Store) DeleteThread(ctx context.Context, id string) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE thread_id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete thread messages: %w", err)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM threads WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete thread: %w", err)
	}
	return tx.Commit()
}

// GetConfig returns the value for a config key. If the key does not exist,
// an empty string is returned with no error.
func (s *Store) GetConfig(ctx context.Context, key string) (string, error) {
	db, err := s.openDB()
	if err != nil {
		return "", err
	}
	defer db.Close()

	var value string
	err = db.QueryRowContext(ctx,
		`SELECT value FROM config WHERE key = ?`,
		key,
	).Scan(&value)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get config: %w", err)
	}
	return value, nil
}

// SetConfig inserts or replaces a config key-value pair.
func (s *Store) SetConfig(ctx context.Context, key, value string) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.ExecContext(ctx,
		`INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("set config: %w", err)
	}
	return nil
}

// --- Scheduled Actions ---

func (s *Store) CreateScheduledAction(ctx context.Context, action oasis.ScheduledAction) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx,
		`INSERT INTO scheduled_actions (id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		action.ID, action.Description, action.Schedule, action.ToolCalls,
		action.SynthesisPrompt, action.NextRun, boolToInt(action.Enabled), action.SkillID, action.CreatedAt)
	return err
}

func (s *Store) ListScheduledActions(ctx context.Context) ([]oasis.ScheduledAction, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at FROM scheduled_actions ORDER BY next_run`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScheduledActions(rows)
}

func (s *Store) GetDueScheduledActions(ctx context.Context, now int64) ([]oasis.ScheduledAction, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at FROM scheduled_actions WHERE enabled = 1 AND next_run <= ?`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScheduledActions(rows)
}

func (s *Store) UpdateScheduledAction(ctx context.Context, action oasis.ScheduledAction) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx,
		`UPDATE scheduled_actions SET description=?, schedule=?, tool_calls=?, synthesis_prompt=?, next_run=?, enabled=?, skill_id=? WHERE id=?`,
		action.Description, action.Schedule, action.ToolCalls, action.SynthesisPrompt, action.NextRun, boolToInt(action.Enabled), action.SkillID, action.ID)
	return err
}

func (s *Store) UpdateScheduledActionEnabled(ctx context.Context, id string, enabled bool) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `UPDATE scheduled_actions SET enabled=? WHERE id=?`, boolToInt(enabled), id)
	return err
}

func (s *Store) DeleteScheduledAction(ctx context.Context, id string) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `DELETE FROM scheduled_actions WHERE id=?`, id)
	return err
}

func (s *Store) DeleteAllScheduledActions(ctx context.Context) (int, error) {
	db, err := s.openDB()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	res, err := db.ExecContext(ctx, `DELETE FROM scheduled_actions`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) FindScheduledActionsByDescription(ctx context.Context, pattern string) ([]oasis.ScheduledAction, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at FROM scheduled_actions WHERE description LIKE ?`, "%"+pattern+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScheduledActions(rows)
}

// --- Skills ---

func (s *Store) CreateSkill(ctx context.Context, skill oasis.Skill) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var toolsJSON *string
	if len(skill.Tools) > 0 {
		data, _ := json.Marshal(skill.Tools)
		v := string(data)
		toolsJSON = &v
	}

	if len(skill.Embedding) > 0 {
		embJSON := serializeEmbedding(skill.Embedding)
		_, err = db.ExecContext(ctx,
			`INSERT INTO skills (id, name, description, instructions, tools, model, embedding, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, vector(?), ?, ?)`,
			skill.ID, skill.Name, skill.Description, skill.Instructions,
			toolsJSON, skill.Model, embJSON, skill.CreatedAt, skill.UpdatedAt)
	} else {
		_, err = db.ExecContext(ctx,
			`INSERT INTO skills (id, name, description, instructions, tools, model, embedding, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
			skill.ID, skill.Name, skill.Description, skill.Instructions,
			toolsJSON, skill.Model, skill.CreatedAt, skill.UpdatedAt)
	}
	return err
}

func (s *Store) GetSkill(ctx context.Context, id string) (oasis.Skill, error) {
	db, err := s.openDB()
	if err != nil {
		return oasis.Skill{}, err
	}
	defer db.Close()

	var sk oasis.Skill
	var tools sql.NullString
	var model sql.NullString
	err = db.QueryRowContext(ctx,
		`SELECT id, name, description, instructions, tools, model, created_at, updated_at
		 FROM skills WHERE id = ?`, id,
	).Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &sk.CreatedAt, &sk.UpdatedAt)
	if err != nil {
		return oasis.Skill{}, fmt.Errorf("get skill: %w", err)
	}
	if tools.Valid {
		_ = json.Unmarshal([]byte(tools.String), &sk.Tools)
	}
	if model.Valid {
		sk.Model = model.String
	}
	return sk, nil
}

func (s *Store) ListSkills(ctx context.Context) ([]oasis.Skill, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT id, name, description, instructions, tools, model, created_at, updated_at
		 FROM skills ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	defer rows.Close()

	var skills []oasis.Skill
	for rows.Next() {
		var sk oasis.Skill
		var tools sql.NullString
		var model sql.NullString
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		if tools.Valid {
			_ = json.Unmarshal([]byte(tools.String), &sk.Tools)
		}
		if model.Valid {
			sk.Model = model.String
		}
		skills = append(skills, sk)
	}
	return skills, rows.Err()
}

func (s *Store) UpdateSkill(ctx context.Context, skill oasis.Skill) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var toolsJSON *string
	if len(skill.Tools) > 0 {
		data, _ := json.Marshal(skill.Tools)
		v := string(data)
		toolsJSON = &v
	}

	if len(skill.Embedding) > 0 {
		embJSON := serializeEmbedding(skill.Embedding)
		_, err = db.ExecContext(ctx,
			`UPDATE skills SET name=?, description=?, instructions=?, tools=?, model=?, embedding=vector(?), updated_at=? WHERE id=?`,
			skill.Name, skill.Description, skill.Instructions, toolsJSON, skill.Model, embJSON, skill.UpdatedAt, skill.ID)
	} else {
		_, err = db.ExecContext(ctx,
			`UPDATE skills SET name=?, description=?, instructions=?, tools=?, model=?, embedding=NULL, updated_at=? WHERE id=?`,
			skill.Name, skill.Description, skill.Instructions, toolsJSON, skill.Model, skill.UpdatedAt, skill.ID)
	}
	return err
}

func (s *Store) DeleteSkill(ctx context.Context, id string) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `DELETE FROM skills WHERE id=?`, id)
	return err
}

// SearchSkills performs vector similarity search over skills using the libsql
// vector_top_k function. Cosine similarity scores are computed via
// vector_distance_cos on the topK results.
func (s *Store) SearchSkills(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredSkill, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	embJSON := serializeEmbedding(embedding)
	rows, err := db.QueryContext(ctx,
		`SELECT sk.id, sk.name, sk.description, sk.instructions, sk.tools, sk.model, sk.created_at, sk.updated_at,
		        1.0 - vector_distance_cos(sk.embedding, vector(?)) AS score
		 FROM vector_top_k('skills_vector_idx', vector(?), ?) AS v
		 JOIN skills AS sk ON sk.rowid = v.id`,
		embJSON, embJSON, topK,
	)
	if err != nil {
		return nil, fmt.Errorf("search skills: %w", err)
	}
	defer rows.Close()

	var skills []oasis.ScoredSkill
	for rows.Next() {
		var sk oasis.Skill
		var tools sql.NullString
		var model sql.NullString
		var score float32
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &sk.CreatedAt, &sk.UpdatedAt, &score); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		if tools.Valid {
			_ = json.Unmarshal([]byte(tools.String), &sk.Tools)
		}
		if model.Valid {
			sk.Model = model.String
		}
		if score < 0 {
			score = 0
		}
		skills = append(skills, oasis.ScoredSkill{Skill: sk, Score: score})
	}
	return skills, rows.Err()
}

// --- GraphStore ---

func (s *Store) StoreEdges(ctx context.Context, edges []oasis.ChunkEdge) error {
	if len(edges) == 0 {
		return nil
	}
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, e := range edges {
		_, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO chunk_edges (id, source_id, target_id, relation, weight)
			 VALUES (?, ?, ?, ?, ?)`,
			e.ID, e.SourceID, e.TargetID, string(e.Relation), e.Weight,
		)
		if err != nil {
			return fmt.Errorf("store edge: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) GetEdges(ctx context.Context, chunkIDs []string) ([]oasis.ChunkEdge, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	placeholders := make([]string, len(chunkIDs))
	args := make([]any, len(chunkIDs))
	for i, id := range chunkIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT id, source_id, target_id, relation, weight FROM chunk_edges WHERE source_id IN (%s)`,
		strings.Join(placeholders, ","),
	)
	return scanEdgesLibsql(ctx, db, query, args)
}

func (s *Store) GetIncomingEdges(ctx context.Context, chunkIDs []string) ([]oasis.ChunkEdge, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	placeholders := make([]string, len(chunkIDs))
	args := make([]any, len(chunkIDs))
	for i, id := range chunkIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT id, source_id, target_id, relation, weight FROM chunk_edges WHERE target_id IN (%s)`,
		strings.Join(placeholders, ","),
	)
	return scanEdgesLibsql(ctx, db, query, args)
}

func (s *Store) PruneOrphanEdges(ctx context.Context) (int, error) {
	db, err := s.openDB()
	if err != nil {
		return 0, err
	}
	defer db.Close()

	result, err := db.ExecContext(ctx,
		`DELETE FROM chunk_edges WHERE source_id NOT IN (SELECT id FROM chunks) OR target_id NOT IN (SELECT id FROM chunks)`)
	if err != nil {
		return 0, fmt.Errorf("prune orphan edges: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

func scanEdgesLibsql(ctx context.Context, db *sql.DB, query string, args []any) ([]oasis.ChunkEdge, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query edges: %w", err)
	}
	defer rows.Close()

	var edges []oasis.ChunkEdge
	for rows.Next() {
		var e oasis.ChunkEdge
		var rel string
		if err := rows.Scan(&e.ID, &e.SourceID, &e.TargetID, &rel, &e.Weight); err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}
		e.Relation = oasis.RelationType(rel)
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// Close is a no-op since we use fresh connections per call.
func (s *Store) Close() error {
	return nil
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

// serializeEmbedding converts a float32 slice to a JSON array string
// suitable for libsql's vector() function, e.g. "[0.1,0.2,0.3]".
func serializeEmbedding(embedding []float32) string {
	parts := make([]string, len(embedding))
	for i, v := range embedding {
		parts[i] = strconv.FormatFloat(float64(v), 'f', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
