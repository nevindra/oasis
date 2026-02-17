// Package libsql implements oasis.VectorStore using libSQL (SQLite-compatible)
// with DiskANN vector extensions for Turso.
package libsql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nevindra/oasis"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// Store implements oasis.VectorStore backed by libSQL / Turso.
//
// It uses fresh connections per call to avoid STREAM_EXPIRED errors
// on remote Turso databases.
type Store struct {
	dbPath string
	dbURL  string // for Turso remote
	token  string // for Turso auth
}

// compile-time check
var _ oasis.VectorStore = (*Store)(nil)

// New creates a Store that uses a local SQLite file at dbPath.
func New(dbPath string) *Store {
	return &Store{dbPath: dbPath}
}

// NewRemote creates a Store that connects to a remote Turso database.
func NewRemote(url, token string) *Store {
	return &Store{dbURL: url, token: token}
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
			embedding F32_BLOB(1536)
		)`,
		`CREATE TABLE IF NOT EXISTS conversations (
			id TEXT PRIMARY KEY,
			chat_id TEXT UNIQUE NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			embedding F32_BLOB(1536),
			created_at INTEGER NOT NULL
		)`,
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
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS skills (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL,
		instructions TEXT NOT NULL,
		tools TEXT,
		model TEXT,
		embedding F32_BLOB(1536),
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// Migrations (best-effort, silent fail if column already exists)
	_, _ = db.ExecContext(ctx, "ALTER TABLE scheduled_actions ADD COLUMN skill_id TEXT")

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
			`INSERT OR REPLACE INTO messages (id, conversation_id, role, content, embedding, created_at)
			 VALUES (?, ?, ?, ?, vector(?), ?)`,
			msg.ID, msg.ConversationID, msg.Role, msg.Content, embJSON, msg.CreatedAt,
		)
	} else {
		_, err = db.ExecContext(ctx,
			`INSERT OR REPLACE INTO messages (id, conversation_id, role, content, embedding, created_at)
			 VALUES (?, ?, ?, ?, NULL, ?)`,
			msg.ID, msg.ConversationID, msg.Role, msg.Content, msg.CreatedAt,
		)
	}
	if err != nil {
		return fmt.Errorf("store message: %w", err)
	}
	return nil
}

// GetMessages returns the most recent messages for a conversation,
// ordered chronologically (oldest first).
func (s *Store) GetMessages(ctx context.Context, conversationID string, limit int) ([]oasis.Message, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT id, conversation_id, role, content, created_at
		 FROM messages
		 WHERE conversation_id = ?
		 ORDER BY created_at DESC
		 LIMIT ?`,
		conversationID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer rows.Close()

	var messages []oasis.Message
	for rows.Next() {
		var m oasis.Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
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
// the libsql vector_top_k function.
func (s *Store) SearchMessages(ctx context.Context, embedding []float32, topK int) ([]oasis.Message, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	embJSON := serializeEmbedding(embedding)
	rows, err := db.QueryContext(ctx,
		`SELECT m.id, m.conversation_id, m.role, m.content, m.created_at
		 FROM vector_top_k('messages_vector_idx', vector(?), ?) AS v
		 JOIN messages AS m ON m.rowid = v.id`,
		embJSON, topK,
	)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()

	var messages []oasis.Message
	for rows.Next() {
		var m oasis.Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, m)
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
		if len(chunk.Embedding) > 0 {
			embJSON := serializeEmbedding(chunk.Embedding)
			_, err = tx.ExecContext(ctx,
				`INSERT OR REPLACE INTO chunks (id, document_id, content, chunk_index, embedding)
				 VALUES (?, ?, ?, ?, vector(?))`,
				chunk.ID, chunk.DocumentID, chunk.Content, chunk.ChunkIndex, embJSON,
			)
		} else {
			_, err = tx.ExecContext(ctx,
				`INSERT OR REPLACE INTO chunks (id, document_id, content, chunk_index, embedding)
				 VALUES (?, ?, ?, ?, NULL)`,
				chunk.ID, chunk.DocumentID, chunk.Content, chunk.ChunkIndex,
			)
		}
		if err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// SearchChunks performs vector similarity search over document chunks
// using the libsql vector_top_k function.
func (s *Store) SearchChunks(ctx context.Context, embedding []float32, topK int) ([]oasis.Chunk, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	embJSON := serializeEmbedding(embedding)
	rows, err := db.QueryContext(ctx,
		`SELECT c.id, c.document_id, c.content, c.chunk_index
		 FROM vector_top_k('chunks_vector_idx', vector(?), ?) AS v
		 JOIN chunks AS c ON c.rowid = v.id`,
		embJSON, topK,
	)
	if err != nil {
		return nil, fmt.Errorf("search chunks: %w", err)
	}
	defer rows.Close()

	var chunks []oasis.Chunk
	for rows.Next() {
		var c oasis.Chunk
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.Content, &c.ChunkIndex); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// GetOrCreateConversation returns an existing conversation for the given
// chatID, or creates and returns a new one if none exists.
func (s *Store) GetOrCreateConversation(ctx context.Context, chatID string) (oasis.Conversation, error) {
	db, err := s.openDB()
	if err != nil {
		return oasis.Conversation{}, err
	}
	defer db.Close()

	var conv oasis.Conversation
	err = db.QueryRowContext(ctx,
		`SELECT id, chat_id, created_at FROM conversations WHERE chat_id = ?`,
		chatID,
	).Scan(&conv.ID, &conv.ChatID, &conv.CreatedAt)

	if err == nil {
		return conv, nil
	}
	if err != sql.ErrNoRows {
		return oasis.Conversation{}, fmt.Errorf("get conversation: %w", err)
	}

	// Not found -- create a new one.
	conv = oasis.Conversation{
		ID:        oasis.NewID(),
		ChatID:    chatID,
		CreatedAt: oasis.NowUnix(),
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO conversations (id, chat_id, created_at) VALUES (?, ?, ?)`,
		conv.ID, conv.ChatID, conv.CreatedAt,
	)
	if err != nil {
		return oasis.Conversation{}, fmt.Errorf("insert conversation: %w", err)
	}
	return conv, nil
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
		`INSERT INTO scheduled_actions (id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		action.ID, action.Description, action.Schedule, action.ToolCalls,
		action.SynthesisPrompt, action.NextRun, boolToInt(action.Enabled), action.CreatedAt)
	return err
}

func (s *Store) ListScheduledActions(ctx context.Context) ([]oasis.ScheduledAction, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, created_at FROM scheduled_actions ORDER BY next_run`)
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
	rows, err := db.QueryContext(ctx, `SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, created_at FROM scheduled_actions WHERE enabled = 1 AND next_run <= ?`, now)
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
		`UPDATE scheduled_actions SET description=?, schedule=?, tool_calls=?, synthesis_prompt=?, next_run=?, enabled=? WHERE id=?`,
		action.Description, action.Schedule, action.ToolCalls, action.SynthesisPrompt, action.NextRun, boolToInt(action.Enabled), action.ID)
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
	rows, err := db.QueryContext(ctx, `SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, created_at FROM scheduled_actions WHERE description LIKE ?`, "%"+pattern+"%")
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
			`UPDATE skills SET name=?, description=?, instructions=?, tools=?, model=?, updated_at=? WHERE id=?`,
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

func (s *Store) SearchSkills(ctx context.Context, embedding []float32, topK int) ([]oasis.Skill, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	embJSON := serializeEmbedding(embedding)
	rows, err := db.QueryContext(ctx,
		`SELECT sk.id, sk.name, sk.description, sk.instructions, sk.tools, sk.model, sk.created_at, sk.updated_at
		 FROM vector_top_k('skills_vector_idx', vector(?), ?) AS v
		 JOIN skills AS sk ON sk.rowid = v.id`,
		embJSON, topK,
	)
	if err != nil {
		return nil, fmt.Errorf("search skills: %w", err)
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
		if err := rows.Scan(&a.ID, &a.Description, &a.Schedule, &a.ToolCalls, &a.SynthesisPrompt, &a.NextRun, &enabled, &a.CreatedAt); err != nil {
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
		parts[i] = fmt.Sprintf("%g", v)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
