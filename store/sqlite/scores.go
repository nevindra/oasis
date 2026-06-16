package sqlite

import (
	"context"
	"database/sql"
	"time"

	oasis "github.com/nevindra/oasis/core"
)

// --- Scores (core.ScoreStore) ---

func (s *Store) SaveScores(ctx context.Context, rows []oasis.ScoreRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Why: deferred rollback guarantees no leaked transaction on any early
	// return; rollback after a successful Commit is a documented no-op.
	// LIFO ordering runs stmt.Close() before this rollback. Matches memory.go.
	defer tx.Rollback() //nolint:errcheck
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO scores (id, scorer_id, run_id, entity_id, entity_type, input, output, value, reason, details, source, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx,
			r.ID, r.ScorerID, r.RunID, r.EntityID, r.EntityType,
			r.Input, r.Output, r.Value, r.Reason, []byte(r.Details), string(r.Source), r.CreatedAt.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListScores(ctx context.Context, filter oasis.ScoreFilter) ([]oasis.ScoreRow, error) {
	q := `SELECT id, scorer_id, run_id, entity_id, entity_type, input, output, value, reason, details, source, created_at FROM scores WHERE 1=1`
	var args []any
	if filter.ScorerID != "" {
		q += " AND scorer_id = ?"
		args = append(args, filter.ScorerID)
	}
	if filter.RunID != "" {
		q += " AND run_id = ?"
		args = append(args, filter.RunID)
	}
	if filter.EntityID != "" {
		q += " AND entity_id = ?"
		args = append(args, filter.EntityID)
	}
	if filter.Source != "" {
		q += " AND source = ?"
		args = append(args, string(filter.Source))
	}
	if !filter.Since.IsZero() {
		q += " AND created_at >= ?"
		args = append(args, filter.Since.UnixMilli())
	}
	q += " ORDER BY created_at DESC"
	if filter.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScores(rows)
}

func (s *Store) GetScore(ctx context.Context, id string) (oasis.ScoreRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, scorer_id, run_id, entity_id, entity_type, input, output, value, reason, details, source, created_at FROM scores WHERE id = ?`, id)
	if err != nil {
		return oasis.ScoreRow{}, err
	}
	defer rows.Close()
	out, err := scanScores(rows)
	if err != nil {
		return oasis.ScoreRow{}, err
	}
	if len(out) == 0 {
		// Why: the ScoreStore contract promises core.ErrNotFound on a missing
		// id (mirrors MemoryItemStore.Get); never leak the driver's sql.ErrNoRows.
		return oasis.ScoreRow{}, oasis.ErrNotFound
	}
	return out[0], nil
}

func (s *Store) DeleteScores(ctx context.Context, filter oasis.ScoreFilter) (int, error) {
	q := `DELETE FROM scores WHERE 1=1`
	var args []any
	if filter.ScorerID != "" {
		q += " AND scorer_id = ?"
		args = append(args, filter.ScorerID)
	}
	if filter.RunID != "" {
		q += " AND run_id = ?"
		args = append(args, filter.RunID)
	}
	if filter.EntityID != "" {
		q += " AND entity_id = ?"
		args = append(args, filter.EntityID)
	}
	if filter.Source != "" {
		q += " AND source = ?"
		args = append(args, string(filter.Source))
	}
	if !filter.Since.IsZero() {
		q += " AND created_at >= ?"
		args = append(args, filter.Since.UnixMilli())
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func scanScores(rows *sql.Rows) ([]oasis.ScoreRow, error) {
	var out []oasis.ScoreRow
	for rows.Next() {
		var r oasis.ScoreRow
		var details []byte
		var source string
		var createdAt int64
		if err := rows.Scan(&r.ID, &r.ScorerID, &r.RunID, &r.EntityID, &r.EntityType,
			&r.Input, &r.Output, &r.Value, &r.Reason, &details, &source, &createdAt); err != nil {
			return nil, err
		}
		if len(details) > 0 {
			r.Details = details
		}
		r.Source = oasis.ScorerSource(source)
		r.CreatedAt = time.UnixMilli(createdAt)
		out = append(out, r)
	}
	return out, rows.Err()
}
