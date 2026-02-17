// Package sqlite implements oasis.Store using pure-Go SQLite
// with in-process brute-force vector search. Zero CGO required.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/nevindra/oasis"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// Store implements oasis.Store backed by a local SQLite file.
// Embeddings are stored as JSON text and vector search is done
// in-process using brute-force cosine similarity.
type Store struct {
	dbPath string
}

var _ oasis.Store = (*Store)(nil)

// New creates a Store using a local SQLite file at dbPath.
func New(dbPath string) *Store {
	return &Store{dbPath: dbPath}
}

func (s *Store) openDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
}

// Init creates all required tables.
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
		embedding TEXT,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// Migrations (best-effort, silent fail if already applied)
	_, _ = db.ExecContext(ctx, "ALTER TABLE scheduled_actions ADD COLUMN skill_id TEXT")

	// Migrate conversations → threads
	_, _ = db.ExecContext(ctx, "ALTER TABLE conversations RENAME TO threads")
	_, _ = db.ExecContext(ctx, "ALTER TABLE threads ADD COLUMN title TEXT")
	_, _ = db.ExecContext(ctx, "ALTER TABLE threads ADD COLUMN metadata TEXT")
	_, _ = db.ExecContext(ctx, "ALTER TABLE threads ADD COLUMN updated_at INTEGER")
	_, _ = db.ExecContext(ctx, "UPDATE threads SET updated_at = created_at WHERE updated_at IS NULL")
	_, _ = db.ExecContext(ctx, "ALTER TABLE messages RENAME COLUMN conversation_id TO thread_id")

	return nil
}

// StoreMessage inserts or replaces a message.
func (s *Store) StoreMessage(ctx context.Context, msg oasis.Message) error {
	db, err := s.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var embJSON *string
	if len(msg.Embedding) > 0 {
		v := serializeEmbedding(msg.Embedding)
		embJSON = &v
	}

	_, err = db.ExecContext(ctx,
		`INSERT OR REPLACE INTO messages (id, thread_id, role, content, embedding, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.ThreadID, msg.Role, msg.Content, embJSON, msg.CreatedAt,
	)
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

// SearchMessages performs brute-force cosine similarity search over messages.
func (s *Store) SearchMessages(ctx context.Context, embedding []float32, topK int) ([]oasis.Message, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT id, thread_id, role, content, embedding, created_at
		 FROM messages WHERE embedding IS NOT NULL`,
	)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()

	type scored struct {
		msg   oasis.Message
		score float32
	}
	var results []scored

	for rows.Next() {
		var m oasis.Message
		var embJSON string
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &embJSON, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		stored, err := deserializeEmbedding(embJSON)
		if err != nil {
			continue
		}
		score := cosineSimilarity(embedding, stored)
		results = append(results, scored{msg: m, score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > topK {
		results = results[:topK]
	}

	messages := make([]oasis.Message, len(results))
	for i, r := range results {
		messages[i] = r.msg
	}
	return messages, nil
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
		var embJSON *string
		if len(chunk.Embedding) > 0 {
			v := serializeEmbedding(chunk.Embedding)
			embJSON = &v
		}
		_, err = tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO chunks (id, document_id, content, chunk_index, embedding)
			 VALUES (?, ?, ?, ?, ?)`,
			chunk.ID, chunk.DocumentID, chunk.Content, chunk.ChunkIndex, embJSON,
		)
		if err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// SearchChunks performs brute-force cosine similarity search over chunks.
func (s *Store) SearchChunks(ctx context.Context, embedding []float32, topK int) ([]oasis.Chunk, error) {
	db, err := s.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT id, document_id, content, chunk_index, embedding
		 FROM chunks WHERE embedding IS NOT NULL`,
	)
	if err != nil {
		return nil, fmt.Errorf("search chunks: %w", err)
	}
	defer rows.Close()

	type scored struct {
		chunk oasis.Chunk
		score float32
	}
	var results []scored

	for rows.Next() {
		var c oasis.Chunk
		var embJSON string
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.Content, &c.ChunkIndex, &embJSON); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		stored, err := deserializeEmbedding(embJSON)
		if err != nil {
			continue
		}
		score := cosineSimilarity(embedding, stored)
		results = append(results, scored{chunk: c, score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunks: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > topK {
		results = results[:topK]
	}

	chunks := make([]oasis.Chunk, len(results))
	for i, r := range results {
		chunks[i] = r.chunk
	}
	return chunks, nil
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

func (s *Store) GetConfig(ctx context.Context, key string) (string, error) {
	db, err := s.openDB()
	if err != nil {
		return "", err
	}
	defer db.Close()

	var value string
	err = db.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get config: %w", err)
	}
	return value, nil
}

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
	var embJSON *string
	if len(skill.Embedding) > 0 {
		v := serializeEmbedding(skill.Embedding)
		embJSON = &v
	}

	_, err = db.ExecContext(ctx,
		`INSERT INTO skills (id, name, description, instructions, tools, model, embedding, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		skill.ID, skill.Name, skill.Description, skill.Instructions,
		toolsJSON, skill.Model, embJSON, skill.CreatedAt, skill.UpdatedAt)
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
	var embJSON *string
	if len(skill.Embedding) > 0 {
		v := serializeEmbedding(skill.Embedding)
		embJSON = &v
	}

	_, err = db.ExecContext(ctx,
		`UPDATE skills SET name=?, description=?, instructions=?, tools=?, model=?, embedding=?, updated_at=? WHERE id=?`,
		skill.Name, skill.Description, skill.Instructions, toolsJSON, skill.Model, embJSON, skill.UpdatedAt, skill.ID)
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

	rows, err := db.QueryContext(ctx,
		`SELECT id, name, description, instructions, tools, model, embedding, created_at, updated_at
		 FROM skills WHERE embedding IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("search skills: %w", err)
	}
	defer rows.Close()

	type scored struct {
		skill oasis.Skill
		score float32
	}
	var results []scored

	for rows.Next() {
		var sk oasis.Skill
		var tools sql.NullString
		var model sql.NullString
		var embJSON string
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &embJSON, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		if tools.Valid {
			_ = json.Unmarshal([]byte(tools.String), &sk.Tools)
		}
		if model.Valid {
			sk.Model = model.String
		}
		stored, err := deserializeEmbedding(embJSON)
		if err != nil {
			continue
		}
		score := cosineSimilarity(embedding, stored)
		results = append(results, scored{skill: sk, score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate skills: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > topK {
		results = results[:topK]
	}

	skills := make([]oasis.Skill, len(results))
	for i, r := range results {
		skills[i] = r.skill
	}
	return skills, nil
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
