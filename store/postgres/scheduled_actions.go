package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nevindra/oasis"
)

// --- Scheduled Actions ---

func (s *Store) CreateScheduledAction(ctx context.Context, action oasis.ScheduledAction) error {
	start := time.Now()
	s.logger.Debug("postgres: create scheduled action", "id", action.ID, "description", action.Description)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO scheduled_actions (id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		action.ID, action.Description, action.Schedule, action.ToolCalls,
		action.SynthesisPrompt, action.NextRun, action.Enabled, action.SkillID, action.CreatedAt)
	if err != nil {
		s.logger.Error("postgres: create scheduled action failed", "id", action.ID, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("postgres: create scheduled action ok", "id", action.ID, "duration", time.Since(start))
	return nil
}

func (s *Store) ListScheduledActions(ctx context.Context) ([]oasis.ScheduledAction, error) {
	start := time.Now()
	s.logger.Debug("postgres: list scheduled actions")
	rows, err := s.pool.Query(ctx,
		`SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at
		 FROM scheduled_actions ORDER BY next_run`)
	if err != nil {
		s.logger.Error("postgres: list scheduled actions failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	defer rows.Close()
	actions, err := scanScheduledActions(rows)
	s.logger.Debug("postgres: list scheduled actions ok", "count", len(actions), "duration", time.Since(start))
	return actions, err
}

func (s *Store) GetDueScheduledActions(ctx context.Context, now int64) ([]oasis.ScheduledAction, error) {
	start := time.Now()
	s.logger.Debug("postgres: get due scheduled actions", "now", now)
	rows, err := s.pool.Query(ctx,
		`SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at
		 FROM scheduled_actions WHERE enabled = TRUE AND next_run <= $1`, now)
	if err != nil {
		s.logger.Error("postgres: get due scheduled actions failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	defer rows.Close()
	actions, err := scanScheduledActions(rows)
	s.logger.Debug("postgres: get due scheduled actions ok", "count", len(actions), "duration", time.Since(start))
	return actions, err
}

func (s *Store) UpdateScheduledAction(ctx context.Context, action oasis.ScheduledAction) error {
	start := time.Now()
	s.logger.Debug("postgres: update scheduled action", "id", action.ID)
	_, err := s.pool.Exec(ctx,
		`UPDATE scheduled_actions SET description=$1, schedule=$2, tool_calls=$3, synthesis_prompt=$4, next_run=$5, enabled=$6, skill_id=$7 WHERE id=$8`,
		action.Description, action.Schedule, action.ToolCalls, action.SynthesisPrompt, action.NextRun, action.Enabled, action.SkillID, action.ID)
	if err != nil {
		s.logger.Error("postgres: update scheduled action failed", "id", action.ID, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("postgres: update scheduled action ok", "id", action.ID, "duration", time.Since(start))
	return nil
}

func (s *Store) UpdateScheduledActionEnabled(ctx context.Context, id string, enabled bool) error {
	start := time.Now()
	s.logger.Debug("postgres: update scheduled action enabled", "id", id, "enabled", enabled)
	_, err := s.pool.Exec(ctx, `UPDATE scheduled_actions SET enabled=$1 WHERE id=$2`, enabled, id)
	if err != nil {
		s.logger.Error("postgres: update scheduled action enabled failed", "id", id, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("postgres: update scheduled action enabled ok", "id", id, "enabled", enabled, "duration", time.Since(start))
	return nil
}

func (s *Store) DeleteScheduledAction(ctx context.Context, id string) error {
	start := time.Now()
	s.logger.Debug("postgres: delete scheduled action", "id", id)
	_, err := s.pool.Exec(ctx, `DELETE FROM scheduled_actions WHERE id=$1`, id)
	if err != nil {
		s.logger.Error("postgres: delete scheduled action failed", "id", id, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("postgres: delete scheduled action ok", "id", id, "duration", time.Since(start))
	return nil
}

func (s *Store) DeleteAllScheduledActions(ctx context.Context) (int, error) {
	start := time.Now()
	s.logger.Debug("postgres: delete all scheduled actions")
	tag, err := s.pool.Exec(ctx, `DELETE FROM scheduled_actions`)
	if err != nil {
		s.logger.Error("postgres: delete all scheduled actions failed", "error", err, "duration", time.Since(start))
		return 0, err
	}
	n := int(tag.RowsAffected())
	s.logger.Debug("postgres: delete all scheduled actions ok", "deleted", n, "duration", time.Since(start))
	return n, nil
}

func (s *Store) FindScheduledActionsByDescription(ctx context.Context, pattern string) ([]oasis.ScheduledAction, error) {
	start := time.Now()
	s.logger.Debug("postgres: find scheduled actions by description", "pattern", pattern)
	rows, err := s.pool.Query(ctx,
		`SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at
		 FROM scheduled_actions WHERE description LIKE $1`,
		"%"+pattern+"%")
	if err != nil {
		s.logger.Error("postgres: find scheduled actions by description failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	defer rows.Close()
	actions, err := scanScheduledActions(rows)
	s.logger.Debug("postgres: find scheduled actions by description ok", "count", len(actions), "duration", time.Since(start))
	return actions, err
}

func scanScheduledActions(rows pgx.Rows) ([]oasis.ScheduledAction, error) {
	var actions []oasis.ScheduledAction
	for rows.Next() {
		var a oasis.ScheduledAction
		if err := rows.Scan(&a.ID, &a.Description, &a.Schedule, &a.ToolCalls, &a.SynthesisPrompt, &a.NextRun, &a.Enabled, &a.SkillID, &a.CreatedAt); err != nil {
			return nil, err
		}
		actions = append(actions, a)
	}
	return actions, rows.Err()
}
