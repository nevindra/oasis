// Package postgres implements oasis.Store and oasis.MemoryStore using
// PostgreSQL with pgvector for native vector similarity search and
// tsvector for full-text keyword search.
//
// Both Store and MemoryStore accept an externally-owned *pgxpool.Pool
// via constructor injection. The caller creates and closes the pool.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nevindra/oasis"
)

// Store implements oasis.Store backed by PostgreSQL with pgvector.
// Vector search uses HNSW indexes with cosine distance.
type Store struct {
	pool *pgxpool.Pool
}

var _ oasis.Store = (*Store)(nil)
var _ oasis.KeywordSearcher = (*Store)(nil)

// New creates a Store using an existing pgxpool.Pool.
// The caller owns the pool and is responsible for closing it.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Init creates the pgvector extension, all required tables, and indexes.
// Safe to call multiple times (all statements are idempotent).
func (s *Store) Init(ctx context.Context) error {
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

		`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			thread_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			embedding vector,
			created_at BIGINT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS messages_thread_idx ON messages(thread_id)`,
		`CREATE INDEX IF NOT EXISTS messages_embedding_idx ON messages USING hnsw (embedding vector_cosine_ops)`,

		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			source TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at BIGINT NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS chunks (
			id TEXT PRIMARY KEY,
			document_id TEXT NOT NULL,
			content TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			embedding vector,
			parent_id TEXT,
			metadata JSONB
		)`,
		`CREATE INDEX IF NOT EXISTS chunks_document_idx ON chunks(document_id)`,
		`CREATE INDEX IF NOT EXISTS chunks_embedding_idx ON chunks USING hnsw (embedding vector_cosine_ops)`,
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

		`CREATE TABLE IF NOT EXISTS skills (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL,
			instructions TEXT NOT NULL,
			tools TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			embedding vector,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS skills_embedding_idx ON skills USING hnsw (embedding vector_cosine_ops)`,
	}

	for _, stmt := range stmts {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("postgres: init: %w", err)
		}
	}
	return nil
}

// --- Messages ---

// StoreMessage inserts or replaces a message.
func (s *Store) StoreMessage(ctx context.Context, msg oasis.Message) error {
	if len(msg.Embedding) > 0 {
		embStr := serializeEmbedding(msg.Embedding)
		_, err := s.pool.Exec(ctx,
			`INSERT INTO messages (id, thread_id, role, content, embedding, created_at)
			 VALUES ($1, $2, $3, $4, $5::vector, $6)
			 ON CONFLICT (id) DO UPDATE SET
			   thread_id = EXCLUDED.thread_id,
			   role = EXCLUDED.role,
			   content = EXCLUDED.content,
			   embedding = EXCLUDED.embedding,
			   created_at = EXCLUDED.created_at`,
			msg.ID, msg.ThreadID, msg.Role, msg.Content, embStr, msg.CreatedAt)
		if err != nil {
			return fmt.Errorf("postgres: store message: %w", err)
		}
		return nil
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO messages (id, thread_id, role, content, embedding, created_at)
		 VALUES ($1, $2, $3, $4, NULL, $5)
		 ON CONFLICT (id) DO UPDATE SET
		   thread_id = EXCLUDED.thread_id,
		   role = EXCLUDED.role,
		   content = EXCLUDED.content,
		   embedding = NULL,
		   created_at = EXCLUDED.created_at`,
		msg.ID, msg.ThreadID, msg.Role, msg.Content, msg.CreatedAt)
	if err != nil {
		return fmt.Errorf("postgres: store message: %w", err)
	}
	return nil
}

// GetMessages returns the most recent messages for a thread,
// ordered chronologically (oldest first).
func (s *Store) GetMessages(ctx context.Context, threadID string, limit int) ([]oasis.Message, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, thread_id, role, content, created_at
		 FROM messages
		 WHERE thread_id = $1
		 ORDER BY created_at DESC
		 LIMIT $2`,
		threadID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: get messages: %w", err)
	}
	defer rows.Close()

	var messages []oasis.Message
	for rows.Next() {
		var m oasis.Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan message: %w", err)
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate messages: %w", err)
	}

	// Reverse to chronological order (oldest first).
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, nil
}

// SearchMessages performs vector similarity search over messages
// using pgvector's cosine distance operator with HNSW index.
func (s *Store) SearchMessages(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredMessage, error) {
	embStr := serializeEmbedding(embedding)
	rows, err := s.pool.Query(ctx,
		`SELECT id, thread_id, role, content, created_at,
		        1 - (embedding <=> $1::vector) AS score
		 FROM messages
		 WHERE embedding IS NOT NULL
		 ORDER BY embedding <=> $1::vector
		 LIMIT $2`,
		embStr, topK)
	if err != nil {
		return nil, fmt.Errorf("postgres: search messages: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredMessage
	for rows.Next() {
		var m oasis.Message
		var score float32
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.CreatedAt, &score); err != nil {
			return nil, fmt.Errorf("postgres: scan message: %w", err)
		}
		results = append(results, oasis.ScoredMessage{Message: m, Score: score})
	}
	return results, rows.Err()
}

// --- Documents + Chunks ---

// StoreDocument inserts a document and all its chunks in a single transaction.
func (s *Store) StoreDocument(ctx context.Context, doc oasis.Document, chunks []oasis.Chunk) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	_, err = tx.Exec(ctx,
		`INSERT INTO documents (id, title, source, content, created_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (id) DO UPDATE SET
		   title = EXCLUDED.title,
		   source = EXCLUDED.source,
		   content = EXCLUDED.content,
		   created_at = EXCLUDED.created_at`,
		doc.ID, doc.Title, doc.Source, doc.Content, doc.CreatedAt)
	if err != nil {
		return fmt.Errorf("postgres: insert document: %w", err)
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
			embStr := serializeEmbedding(chunk.Embedding)
			_, err = tx.Exec(ctx,
				`INSERT INTO chunks (id, document_id, parent_id, content, chunk_index, embedding, metadata)
				 VALUES ($1, $2, $3, $4, $5, $6::vector, $7::jsonb)
				 ON CONFLICT (id) DO UPDATE SET
				   document_id = EXCLUDED.document_id,
				   parent_id = EXCLUDED.parent_id,
				   content = EXCLUDED.content,
				   chunk_index = EXCLUDED.chunk_index,
				   embedding = EXCLUDED.embedding,
				   metadata = EXCLUDED.metadata`,
				chunk.ID, chunk.DocumentID, parentID, chunk.Content, chunk.ChunkIndex, embStr, metaJSON)
		} else {
			_, err = tx.Exec(ctx,
				`INSERT INTO chunks (id, document_id, parent_id, content, chunk_index, embedding, metadata)
				 VALUES ($1, $2, $3, $4, $5, NULL, $6::jsonb)
				 ON CONFLICT (id) DO UPDATE SET
				   document_id = EXCLUDED.document_id,
				   parent_id = EXCLUDED.parent_id,
				   content = EXCLUDED.content,
				   chunk_index = EXCLUDED.chunk_index,
				   embedding = NULL,
				   metadata = EXCLUDED.metadata`,
				chunk.ID, chunk.DocumentID, parentID, chunk.Content, chunk.ChunkIndex, metaJSON)
		}
		if err != nil {
			return fmt.Errorf("postgres: insert chunk: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit tx: %w", err)
	}
	return nil
}

// SearchChunks performs vector similarity search over document chunks
// using pgvector's cosine distance operator with HNSW index.
func (s *Store) SearchChunks(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredChunk, error) {
	embStr := serializeEmbedding(embedding)
	rows, err := s.pool.Query(ctx,
		`SELECT id, document_id, parent_id, content, chunk_index, metadata,
		        1 - (embedding <=> $1::vector) AS score
		 FROM chunks
		 WHERE embedding IS NOT NULL
		 ORDER BY embedding <=> $1::vector
		 LIMIT $2`,
		embStr, topK)
	if err != nil {
		return nil, fmt.Errorf("postgres: search chunks: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredChunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID *string
		var metaJSON []byte
		var score float32
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &metaJSON, &score); err != nil {
			return nil, fmt.Errorf("postgres: scan chunk: %w", err)
		}
		if parentID != nil {
			c.ParentID = *parentID
		}
		if metaJSON != nil {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal(metaJSON, c.Metadata)
		}
		results = append(results, oasis.ScoredChunk{Chunk: c, Score: score})
	}
	return results, rows.Err()
}

// SearchChunksKeyword performs full-text keyword search over document chunks
// using PostgreSQL tsvector/tsquery with a GIN index.
func (s *Store) SearchChunksKeyword(ctx context.Context, query string, topK int) ([]oasis.ScoredChunk, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, document_id, parent_id, content, chunk_index, metadata,
		        ts_rank(to_tsvector('english', content), plainto_tsquery('english', $1)) AS score
		 FROM chunks
		 WHERE to_tsvector('english', content) @@ plainto_tsquery('english', $1)
		 ORDER BY score DESC
		 LIMIT $2`,
		query, topK)
	if err != nil {
		return nil, fmt.Errorf("postgres: keyword search: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredChunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID *string
		var metaJSON []byte
		var score float32
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &metaJSON, &score); err != nil {
			return nil, fmt.Errorf("postgres: scan chunk: %w", err)
		}
		if parentID != nil {
			c.ParentID = *parentID
		}
		if metaJSON != nil {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal(metaJSON, c.Metadata)
		}
		results = append(results, oasis.ScoredChunk{Chunk: c, Score: score})
	}
	return results, rows.Err()
}

// GetChunksByIDs returns chunks matching the given IDs.
func (s *Store) GetChunksByIDs(ctx context.Context, ids []string) ([]oasis.Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, document_id, parent_id, content, chunk_index, metadata
		 FROM chunks WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("postgres: get chunks by ids: %w", err)
	}
	defer rows.Close()

	var chunks []oasis.Chunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID *string
		var metaJSON []byte
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &metaJSON); err != nil {
			return nil, fmt.Errorf("postgres: scan chunk: %w", err)
		}
		if parentID != nil {
			c.ParentID = *parentID
		}
		if metaJSON != nil {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal(metaJSON, c.Metadata)
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// --- Threads ---

// CreateThread inserts a new thread.
func (s *Store) CreateThread(ctx context.Context, thread oasis.Thread) error {
	var metaJSON *string
	if len(thread.Metadata) > 0 {
		data, _ := json.Marshal(thread.Metadata)
		v := string(data)
		metaJSON = &v
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO threads (id, chat_id, title, metadata, created_at, updated_at)
		 VALUES ($1, $2, $3, $4::jsonb, $5, $6)`,
		thread.ID, thread.ChatID, thread.Title, metaJSON, thread.CreatedAt, thread.UpdatedAt)
	if err != nil {
		return fmt.Errorf("postgres: create thread: %w", err)
	}
	return nil
}

// GetThread returns a thread by ID.
func (s *Store) GetThread(ctx context.Context, id string) (oasis.Thread, error) {
	var t oasis.Thread
	var metaJSON []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, chat_id, title, metadata, created_at, updated_at FROM threads WHERE id = $1`, id,
	).Scan(&t.ID, &t.ChatID, &t.Title, &metaJSON, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return oasis.Thread{}, fmt.Errorf("postgres: get thread: %w", err)
	}
	if metaJSON != nil {
		_ = json.Unmarshal(metaJSON, &t.Metadata)
	}
	return t, nil
}

// ListThreads returns threads for a chatID, ordered by most recently updated first.
func (s *Store) ListThreads(ctx context.Context, chatID string, limit int) ([]oasis.Thread, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, chat_id, title, metadata, created_at, updated_at
		 FROM threads WHERE chat_id = $1
		 ORDER BY updated_at DESC
		 LIMIT $2`,
		chatID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list threads: %w", err)
	}
	defer rows.Close()

	var threads []oasis.Thread
	for rows.Next() {
		var t oasis.Thread
		var metaJSON []byte
		if err := rows.Scan(&t.ID, &t.ChatID, &t.Title, &metaJSON, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan thread: %w", err)
		}
		if metaJSON != nil {
			_ = json.Unmarshal(metaJSON, &t.Metadata)
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

// UpdateThread updates a thread's title, metadata, and updated_at.
func (s *Store) UpdateThread(ctx context.Context, thread oasis.Thread) error {
	var metaJSON *string
	if len(thread.Metadata) > 0 {
		data, _ := json.Marshal(thread.Metadata)
		v := string(data)
		metaJSON = &v
	}

	_, err := s.pool.Exec(ctx,
		`UPDATE threads SET title=$1, metadata=$2::jsonb, updated_at=$3 WHERE id=$4`,
		thread.Title, metaJSON, thread.UpdatedAt, thread.ID)
	if err != nil {
		return fmt.Errorf("postgres: update thread: %w", err)
	}
	return nil
}

// DeleteThread removes a thread and its messages.
func (s *Store) DeleteThread(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM messages WHERE thread_id = $1`, id); err != nil {
		return fmt.Errorf("postgres: delete thread messages: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM threads WHERE id = $1`, id); err != nil {
		return fmt.Errorf("postgres: delete thread: %w", err)
	}
	return tx.Commit(ctx)
}

// --- Config ---

func (s *Store) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx, `SELECT value FROM config WHERE key = $1`, key).Scan(&value)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("postgres: get config: %w", err)
	}
	return value, nil
}

func (s *Store) SetConfig(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO config (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		key, value)
	if err != nil {
		return fmt.Errorf("postgres: set config: %w", err)
	}
	return nil
}

// --- Scheduled Actions ---

func (s *Store) CreateScheduledAction(ctx context.Context, action oasis.ScheduledAction) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO scheduled_actions (id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		action.ID, action.Description, action.Schedule, action.ToolCalls,
		action.SynthesisPrompt, action.NextRun, action.Enabled, action.CreatedAt)
	return err
}

func (s *Store) ListScheduledActions(ctx context.Context) ([]oasis.ScheduledAction, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, created_at
		 FROM scheduled_actions ORDER BY next_run`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScheduledActions(rows)
}

func (s *Store) GetDueScheduledActions(ctx context.Context, now int64) ([]oasis.ScheduledAction, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, created_at
		 FROM scheduled_actions WHERE enabled = TRUE AND next_run <= $1`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScheduledActions(rows)
}

func (s *Store) UpdateScheduledAction(ctx context.Context, action oasis.ScheduledAction) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE scheduled_actions SET description=$1, schedule=$2, tool_calls=$3, synthesis_prompt=$4, next_run=$5, enabled=$6 WHERE id=$7`,
		action.Description, action.Schedule, action.ToolCalls, action.SynthesisPrompt, action.NextRun, action.Enabled, action.ID)
	return err
}

func (s *Store) UpdateScheduledActionEnabled(ctx context.Context, id string, enabled bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE scheduled_actions SET enabled=$1 WHERE id=$2`, enabled, id)
	return err
}

func (s *Store) DeleteScheduledAction(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM scheduled_actions WHERE id=$1`, id)
	return err
}

func (s *Store) DeleteAllScheduledActions(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM scheduled_actions`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) FindScheduledActionsByDescription(ctx context.Context, pattern string) ([]oasis.ScheduledAction, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, created_at
		 FROM scheduled_actions WHERE description LIKE $1`,
		"%"+pattern+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScheduledActions(rows)
}

// --- Skills ---

func (s *Store) CreateSkill(ctx context.Context, skill oasis.Skill) error {
	var toolsJSON string
	if len(skill.Tools) > 0 {
		data, _ := json.Marshal(skill.Tools)
		toolsJSON = string(data)
	}

	if len(skill.Embedding) > 0 {
		embStr := serializeEmbedding(skill.Embedding)
		_, err := s.pool.Exec(ctx,
			`INSERT INTO skills (id, name, description, instructions, tools, model, embedding, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7::vector, $8, $9)`,
			skill.ID, skill.Name, skill.Description, skill.Instructions,
			toolsJSON, skill.Model, embStr, skill.CreatedAt, skill.UpdatedAt)
		return err
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO skills (id, name, description, instructions, tools, model, embedding, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NULL, $7, $8)`,
		skill.ID, skill.Name, skill.Description, skill.Instructions,
		toolsJSON, skill.Model, skill.CreatedAt, skill.UpdatedAt)
	return err
}

func (s *Store) GetSkill(ctx context.Context, id string) (oasis.Skill, error) {
	var sk oasis.Skill
	var tools, model string
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, description, instructions, tools, model, created_at, updated_at
		 FROM skills WHERE id = $1`, id,
	).Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &sk.CreatedAt, &sk.UpdatedAt)
	if err != nil {
		return oasis.Skill{}, fmt.Errorf("postgres: get skill: %w", err)
	}
	if tools != "" {
		_ = json.Unmarshal([]byte(tools), &sk.Tools)
	}
	sk.Model = model
	return sk, nil
}

func (s *Store) ListSkills(ctx context.Context) ([]oasis.Skill, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, description, instructions, tools, model, created_at, updated_at
		 FROM skills ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list skills: %w", err)
	}
	defer rows.Close()

	var skills []oasis.Skill
	for rows.Next() {
		var sk oasis.Skill
		var tools, model string
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan skill: %w", err)
		}
		if tools != "" {
			_ = json.Unmarshal([]byte(tools), &sk.Tools)
		}
		sk.Model = model
		skills = append(skills, sk)
	}
	return skills, rows.Err()
}

func (s *Store) UpdateSkill(ctx context.Context, skill oasis.Skill) error {
	var toolsJSON string
	if len(skill.Tools) > 0 {
		data, _ := json.Marshal(skill.Tools)
		toolsJSON = string(data)
	}

	if len(skill.Embedding) > 0 {
		embStr := serializeEmbedding(skill.Embedding)
		_, err := s.pool.Exec(ctx,
			`UPDATE skills SET name=$1, description=$2, instructions=$3, tools=$4, model=$5, embedding=$6::vector, updated_at=$7 WHERE id=$8`,
			skill.Name, skill.Description, skill.Instructions, toolsJSON, skill.Model, embStr, skill.UpdatedAt, skill.ID)
		return err
	}

	_, err := s.pool.Exec(ctx,
		`UPDATE skills SET name=$1, description=$2, instructions=$3, tools=$4, model=$5, updated_at=$6 WHERE id=$7`,
		skill.Name, skill.Description, skill.Instructions, toolsJSON, skill.Model, skill.UpdatedAt, skill.ID)
	return err
}

func (s *Store) DeleteSkill(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM skills WHERE id=$1`, id)
	return err
}

// SearchSkills performs vector similarity search over stored skills
// using pgvector's cosine distance operator with HNSW index.
func (s *Store) SearchSkills(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredSkill, error) {
	embStr := serializeEmbedding(embedding)
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, description, instructions, tools, model, created_at, updated_at,
		        1 - (embedding <=> $1::vector) AS score
		 FROM skills
		 WHERE embedding IS NOT NULL
		 ORDER BY embedding <=> $1::vector
		 LIMIT $2`,
		embStr, topK)
	if err != nil {
		return nil, fmt.Errorf("postgres: search skills: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredSkill
	for rows.Next() {
		var sk oasis.Skill
		var tools, model string
		var score float32
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &sk.CreatedAt, &sk.UpdatedAt, &score); err != nil {
			return nil, fmt.Errorf("postgres: scan skill: %w", err)
		}
		if tools != "" {
			_ = json.Unmarshal([]byte(tools), &sk.Tools)
		}
		sk.Model = model
		results = append(results, oasis.ScoredSkill{Skill: sk, Score: score})
	}
	return results, rows.Err()
}

// Close is a no-op. The caller owns the pool and manages its lifecycle.
func (s *Store) Close() error {
	return nil
}

// --- Helpers ---

func scanScheduledActions(rows pgx.Rows) ([]oasis.ScheduledAction, error) {
	var actions []oasis.ScheduledAction
	for rows.Next() {
		var a oasis.ScheduledAction
		if err := rows.Scan(&a.ID, &a.Description, &a.Schedule, &a.ToolCalls, &a.SynthesisPrompt, &a.NextRun, &a.Enabled, &a.CreatedAt); err != nil {
			return nil, err
		}
		actions = append(actions, a)
	}
	return actions, rows.Err()
}

// serializeEmbedding converts []float32 to a string like "[0.1,0.2,0.3]"
// suitable for pgvector's text input format.
func serializeEmbedding(embedding []float32) string {
	parts := make([]string, len(embedding))
	for i, v := range embedding {
		parts[i] = fmt.Sprintf("%g", v)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
