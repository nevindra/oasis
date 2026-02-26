package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nevindra/oasis"
)

// --- CheckpointStore ---

// SaveCheckpoint upserts an ingest checkpoint by ID.
func (s *Store) SaveCheckpoint(ctx context.Context, cp oasis.IngestCheckpoint) error {
	data, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("postgres: save checkpoint: marshal: %w", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO ingest_checkpoints (id, type, status, data, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT(id) DO UPDATE SET
		   type       = EXCLUDED.type,
		   status     = EXCLUDED.status,
		   data       = EXCLUDED.data,
		   updated_at = EXCLUDED.updated_at`,
		cp.ID, cp.Type, string(cp.Status), data, cp.CreatedAt, cp.UpdatedAt)
	if err != nil {
		return fmt.Errorf("postgres: save checkpoint: %w", err)
	}
	return nil
}

// LoadCheckpoint retrieves an ingest checkpoint by ID.
func (s *Store) LoadCheckpoint(ctx context.Context, id string) (oasis.IngestCheckpoint, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT data FROM ingest_checkpoints WHERE id = $1`, id)
	var blob []byte
	if err := row.Scan(&blob); err != nil {
		return oasis.IngestCheckpoint{}, fmt.Errorf("postgres: load checkpoint %s: %w", id, err)
	}
	var cp oasis.IngestCheckpoint
	if err := json.Unmarshal(blob, &cp); err != nil {
		return oasis.IngestCheckpoint{}, fmt.Errorf("postgres: load checkpoint %s: unmarshal: %w", id, err)
	}
	return cp, nil
}

// DeleteCheckpoint removes an ingest checkpoint by ID.
func (s *Store) DeleteCheckpoint(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM ingest_checkpoints WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete checkpoint %s: %w", id, err)
	}
	return nil
}

// ListCheckpoints returns all stored ingest checkpoints.
func (s *Store) ListCheckpoints(ctx context.Context) ([]oasis.IngestCheckpoint, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT data FROM ingest_checkpoints ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list checkpoints: %w", err)
	}
	defer rows.Close()
	var out []oasis.IngestCheckpoint
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, fmt.Errorf("postgres: list checkpoints: scan: %w", err)
		}
		var cp oasis.IngestCheckpoint
		if err := json.Unmarshal(blob, &cp); err != nil {
			return nil, fmt.Errorf("postgres: list checkpoints: unmarshal: %w", err)
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}
