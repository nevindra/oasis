package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	oasis "github.com/nevindra/oasis/core"
)

// --- Scores (core.ScoreStore) ---

func (s *Store) SaveScores(ctx context.Context, rows []oasis.ScoreRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(
			`INSERT INTO scores (id, scorer_id, run_id, entity_id, entity_type, input, output, value, reason, details, source, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			r.ID, r.ScorerID, r.RunID, r.EntityID, r.EntityType,
			r.Input, r.Output, r.Value, r.Reason, []byte(r.Details), string(r.Source), r.CreatedAt.UnixMilli())
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range rows {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListScores(ctx context.Context, filter oasis.ScoreFilter) ([]oasis.ScoreRow, error) {
	q := `SELECT id, scorer_id, run_id, entity_id, entity_type, input, output, value, reason, details, source, created_at FROM scores WHERE TRUE`
	var args []any
	i := 1
	add := func(cond string, val any) {
		q += cond
		args = append(args, val)
		i++
	}
	if filter.ScorerID != "" {
		add(" AND scorer_id = $"+itoa(i), filter.ScorerID)
	}
	if filter.RunID != "" {
		add(" AND run_id = $"+itoa(i), filter.RunID)
	}
	if filter.EntityID != "" {
		add(" AND entity_id = $"+itoa(i), filter.EntityID)
	}
	if filter.Source != "" {
		add(" AND source = $"+itoa(i), string(filter.Source))
	}
	if !filter.Since.IsZero() {
		add(" AND created_at >= $"+itoa(i), filter.Since.UnixMilli())
	}
	q += " ORDER BY created_at DESC"
	if filter.Limit > 0 {
		add(" LIMIT $"+itoa(i), filter.Limit)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScores(rows)
}

func (s *Store) GetScore(ctx context.Context, id string) (oasis.ScoreRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, scorer_id, run_id, entity_id, entity_type, input, output, value, reason, details, source, created_at FROM scores WHERE id = $1`, id)
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
		// id (mirrors MemoryItemStore.Get); never leak the driver's pgx.ErrNoRows.
		return oasis.ScoreRow{}, oasis.ErrNotFound
	}
	return out[0], nil
}

func (s *Store) DeleteScores(ctx context.Context, filter oasis.ScoreFilter) (int, error) {
	q := `DELETE FROM scores WHERE TRUE`
	var args []any
	i := 1
	add := func(cond string, val any) {
		q += cond
		args = append(args, val)
		i++
	}
	if filter.ScorerID != "" {
		add(" AND scorer_id = $"+itoa(i), filter.ScorerID)
	}
	if filter.RunID != "" {
		add(" AND run_id = $"+itoa(i), filter.RunID)
	}
	if filter.EntityID != "" {
		add(" AND entity_id = $"+itoa(i), filter.EntityID)
	}
	if filter.Source != "" {
		add(" AND source = $"+itoa(i), string(filter.Source))
	}
	if !filter.Since.IsZero() {
		add(" AND created_at >= $"+itoa(i), filter.Since.UnixMilli())
	}
	tag, err := s.pool.Exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func scanScores(rows pgx.Rows) ([]oasis.ScoreRow, error) {
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

// itoa renders a positional placeholder index. It wraps strconv.Itoa so the
// call sites stay terse.
//
// Why: the old string(rune('0'+i)) hack only produced correct digits for
// i<10 — at i>=10 it emitted ':' (the byte after '9'), silently corrupting
// placeholders once the filter list grew. strconv.Itoa is correct for any i.
func itoa(i int) string {
	return strconv.Itoa(i)
}
