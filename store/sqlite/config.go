package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

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
