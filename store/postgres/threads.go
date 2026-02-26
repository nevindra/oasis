package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nevindra/oasis"
)

// --- Threads ---

// CreateThread inserts a new thread.
func (s *Store) CreateThread(ctx context.Context, thread oasis.Thread) error {
	start := time.Now()
	s.logger.Debug("postgres: create thread", "id", thread.ID, "chat_id", thread.ChatID, "title", thread.Title)
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
		s.logger.Error("postgres: create thread failed", "id", thread.ID, "error", err, "duration", time.Since(start))
		return fmt.Errorf("postgres: create thread: %w", err)
	}
	s.logger.Debug("postgres: create thread ok", "id", thread.ID, "duration", time.Since(start))
	return nil
}

// GetThread returns a thread by ID.
func (s *Store) GetThread(ctx context.Context, id string) (oasis.Thread, error) {
	start := time.Now()
	s.logger.Debug("postgres: get thread", "id", id)
	var t oasis.Thread
	var metaJSON []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, chat_id, title, metadata, created_at, updated_at FROM threads WHERE id = $1`, id,
	).Scan(&t.ID, &t.ChatID, &t.Title, &metaJSON, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		s.logger.Error("postgres: get thread failed", "id", id, "error", err, "duration", time.Since(start))
		return oasis.Thread{}, fmt.Errorf("postgres: get thread: %w", err)
	}
	if metaJSON != nil {
		_ = json.Unmarshal(metaJSON, &t.Metadata)
	}
	s.logger.Debug("postgres: get thread ok", "id", id, "duration", time.Since(start))
	return t, nil
}

// ListThreads returns threads for a chatID, ordered by most recently updated first.
func (s *Store) ListThreads(ctx context.Context, chatID string, limit int) ([]oasis.Thread, error) {
	start := time.Now()
	s.logger.Debug("postgres: list threads", "chat_id", chatID, "limit", limit)
	rows, err := s.pool.Query(ctx,
		`SELECT id, chat_id, title, metadata, created_at, updated_at
		 FROM threads WHERE chat_id = $1
		 ORDER BY updated_at DESC
		 LIMIT $2`,
		chatID, limit)
	if err != nil {
		s.logger.Error("postgres: list threads failed", "chat_id", chatID, "error", err, "duration", time.Since(start))
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
	s.logger.Debug("postgres: list threads ok", "chat_id", chatID, "count", len(threads), "duration", time.Since(start))
	return threads, rows.Err()
}

// UpdateThread updates a thread's title, metadata, and updated_at.
func (s *Store) UpdateThread(ctx context.Context, thread oasis.Thread) error {
	start := time.Now()
	s.logger.Debug("postgres: update thread", "id", thread.ID, "title", thread.Title)
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
		s.logger.Error("postgres: update thread failed", "id", thread.ID, "error", err, "duration", time.Since(start))
		return fmt.Errorf("postgres: update thread: %w", err)
	}
	s.logger.Debug("postgres: update thread ok", "id", thread.ID, "duration", time.Since(start))
	return nil
}

// DeleteThread removes a thread and its messages.
func (s *Store) DeleteThread(ctx context.Context, id string) error {
	start := time.Now()
	s.logger.Debug("postgres: delete thread", "id", id)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM messages WHERE thread_id = $1`, id); err != nil {
		s.logger.Error("postgres: delete thread messages failed", "id", id, "error", err, "duration", time.Since(start))
		return fmt.Errorf("postgres: delete thread messages: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM threads WHERE id = $1`, id); err != nil {
		s.logger.Error("postgres: delete thread failed", "id", id, "error", err, "duration", time.Since(start))
		return fmt.Errorf("postgres: delete thread: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.logger.Debug("postgres: delete thread ok", "id", id, "duration", time.Since(start))
	return nil
}
