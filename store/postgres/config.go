package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// --- Config ---

func (s *Store) GetConfig(ctx context.Context, key string) (string, error) {
	start := time.Now()
	s.logger.Debug("postgres: get config", "key", key)
	var value string
	err := s.pool.QueryRow(ctx, `SELECT value FROM config WHERE key = $1`, key).Scan(&value)
	if err == pgx.ErrNoRows {
		s.logger.Debug("postgres: get config not found", "key", key, "duration", time.Since(start))
		return "", nil
	}
	if err != nil {
		s.logger.Error("postgres: get config failed", "key", key, "error", err, "duration", time.Since(start))
		return "", fmt.Errorf("postgres: get config: %w", err)
	}
	s.logger.Debug("postgres: get config ok", "key", key, "duration", time.Since(start))
	return value, nil
}

func (s *Store) SetConfig(ctx context.Context, key, value string) error {
	start := time.Now()
	s.logger.Debug("postgres: set config", "key", key)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO config (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		key, value)
	if err != nil {
		s.logger.Error("postgres: set config failed", "key", key, "error", err, "duration", time.Since(start))
		return fmt.Errorf("postgres: set config: %w", err)
	}
	s.logger.Debug("postgres: set config ok", "key", key, "duration", time.Since(start))
	return nil
}
