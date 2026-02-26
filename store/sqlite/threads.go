package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nevindra/oasis"
)

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
