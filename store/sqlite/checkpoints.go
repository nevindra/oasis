package sqlite

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
		return fmt.Errorf("sqlite: save checkpoint: marshal: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO ingest_checkpoints (id, type, status, data, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   type       = excluded.type,
		   status     = excluded.status,
		   data       = excluded.data,
		   updated_at = excluded.updated_at`,
		cp.ID, cp.Type, string(cp.Status), data, cp.CreatedAt, cp.UpdatedAt)
	if err != nil {
		return fmt.Errorf("sqlite: save checkpoint: %w", err)
	}
	return nil
}

// LoadCheckpoint retrieves an ingest checkpoint by ID.
func (s *Store) LoadCheckpoint(ctx context.Context, id string) (oasis.IngestCheckpoint, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT data FROM ingest_checkpoints WHERE id = ?`, id)
	var blob []byte
	if err := row.Scan(&blob); err != nil {
		return oasis.IngestCheckpoint{}, fmt.Errorf("sqlite: load checkpoint %s: %w", id, err)
	}
	var cp oasis.IngestCheckpoint
	if err := json.Unmarshal(blob, &cp); err != nil {
		return oasis.IngestCheckpoint{}, fmt.Errorf("sqlite: load checkpoint %s: unmarshal: %w", id, err)
	}
	return cp, nil
}

// DeleteCheckpoint removes an ingest checkpoint by ID.
func (s *Store) DeleteCheckpoint(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM ingest_checkpoints WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: delete checkpoint %s: %w", id, err)
	}
	return nil
}

// ListCheckpoints returns all stored ingest checkpoints.
func (s *Store) ListCheckpoints(ctx context.Context) ([]oasis.IngestCheckpoint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT data FROM ingest_checkpoints ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list checkpoints: %w", err)
	}
	defer rows.Close()
	var out []oasis.IngestCheckpoint
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, fmt.Errorf("sqlite: list checkpoints: scan: %w", err)
		}
		var cp oasis.IngestCheckpoint
		if err := json.Unmarshal(blob, &cp); err != nil {
			return nil, fmt.Errorf("sqlite: list checkpoints: unmarshal: %w", err)
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}
