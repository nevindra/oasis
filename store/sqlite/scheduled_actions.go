package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/nevindra/oasis"
)

// --- Scheduled Actions ---

func (s *Store) CreateScheduledAction(ctx context.Context, action oasis.ScheduledAction) error {
	start := time.Now()
	s.logger.Debug("sqlite: create scheduled action", "id", action.ID, "description", action.Description, "schedule", action.Schedule)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduled_actions (id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		action.ID, action.Description, action.Schedule, action.ToolCalls,
		action.SynthesisPrompt, action.NextRun, boolToInt(action.Enabled), action.SkillID, action.CreatedAt)
	if err != nil {
		s.logger.Error("sqlite: create scheduled action failed", "id", action.ID, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: create scheduled action ok", "id", action.ID, "duration", time.Since(start))
	return nil
}

func (s *Store) ListScheduledActions(ctx context.Context) ([]oasis.ScheduledAction, error) {
	start := time.Now()
	s.logger.Debug("sqlite: list scheduled actions")

	rows, err := s.db.QueryContext(ctx, `SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at FROM scheduled_actions ORDER BY next_run`)
	if err != nil {
		s.logger.Error("sqlite: list scheduled actions failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	defer rows.Close()
	actions, err := scanScheduledActions(rows)
	if err != nil {
		s.logger.Error("sqlite: list scheduled actions scan failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	s.logger.Debug("sqlite: list scheduled actions ok", "count", len(actions), "duration", time.Since(start))
	return actions, nil
}

func (s *Store) GetDueScheduledActions(ctx context.Context, now int64) ([]oasis.ScheduledAction, error) {
	start := time.Now()
	s.logger.Debug("sqlite: get due scheduled actions", "now", now)

	rows, err := s.db.QueryContext(ctx, `SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at FROM scheduled_actions WHERE enabled = 1 AND next_run <= ?`, now)
	if err != nil {
		s.logger.Error("sqlite: get due scheduled actions failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	defer rows.Close()
	actions, err := scanScheduledActions(rows)
	if err != nil {
		s.logger.Error("sqlite: get due scheduled actions scan failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	s.logger.Debug("sqlite: get due scheduled actions ok", "count", len(actions), "duration", time.Since(start))
	return actions, nil
}

func (s *Store) UpdateScheduledAction(ctx context.Context, action oasis.ScheduledAction) error {
	start := time.Now()
	s.logger.Debug("sqlite: update scheduled action", "id", action.ID, "next_run", action.NextRun, "enabled", action.Enabled)

	_, err := s.db.ExecContext(ctx,
		`UPDATE scheduled_actions SET description=?, schedule=?, tool_calls=?, synthesis_prompt=?, next_run=?, enabled=?, skill_id=? WHERE id=?`,
		action.Description, action.Schedule, action.ToolCalls, action.SynthesisPrompt, action.NextRun, boolToInt(action.Enabled), action.SkillID, action.ID)
	if err != nil {
		s.logger.Error("sqlite: update scheduled action failed", "id", action.ID, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: update scheduled action ok", "id", action.ID, "duration", time.Since(start))
	return nil
}

func (s *Store) UpdateScheduledActionEnabled(ctx context.Context, id string, enabled bool) error {
	start := time.Now()
	s.logger.Debug("sqlite: update scheduled action enabled", "id", id, "enabled", enabled)

	_, err := s.db.ExecContext(ctx, `UPDATE scheduled_actions SET enabled=? WHERE id=?`, boolToInt(enabled), id)
	if err != nil {
		s.logger.Error("sqlite: update scheduled action enabled failed", "id", id, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: update scheduled action enabled ok", "id", id, "enabled", enabled, "duration", time.Since(start))
	return nil
}

func (s *Store) DeleteScheduledAction(ctx context.Context, id string) error {
	start := time.Now()
	s.logger.Debug("sqlite: delete scheduled action", "id", id)

	_, err := s.db.ExecContext(ctx, `DELETE FROM scheduled_actions WHERE id=?`, id)
	if err != nil {
		s.logger.Error("sqlite: delete scheduled action failed", "id", id, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: delete scheduled action ok", "id", id, "duration", time.Since(start))
	return nil
}

func (s *Store) DeleteAllScheduledActions(ctx context.Context) (int, error) {
	start := time.Now()
	s.logger.Debug("sqlite: delete all scheduled actions")

	res, err := s.db.ExecContext(ctx, `DELETE FROM scheduled_actions`)
	if err != nil {
		s.logger.Error("sqlite: delete all scheduled actions failed", "error", err, "duration", time.Since(start))
		return 0, err
	}
	n, _ := res.RowsAffected()
	s.logger.Debug("sqlite: delete all scheduled actions ok", "deleted", n, "duration", time.Since(start))
	return int(n), nil
}

func (s *Store) FindScheduledActionsByDescription(ctx context.Context, pattern string) ([]oasis.ScheduledAction, error) {
	start := time.Now()
	s.logger.Debug("sqlite: find scheduled actions by description", "pattern", pattern)

	rows, err := s.db.QueryContext(ctx, `SELECT id, description, schedule, tool_calls, synthesis_prompt, next_run, enabled, skill_id, created_at FROM scheduled_actions WHERE description LIKE ?`, "%"+pattern+"%")
	if err != nil {
		s.logger.Error("sqlite: find scheduled actions by description failed", "pattern", pattern, "error", err, "duration", time.Since(start))
		return nil, err
	}
	defer rows.Close()
	actions, err := scanScheduledActions(rows)
	if err != nil {
		s.logger.Error("sqlite: find scheduled actions by description scan failed", "pattern", pattern, "error", err, "duration", time.Since(start))
		return nil, err
	}
	s.logger.Debug("sqlite: find scheduled actions by description ok", "pattern", pattern, "count", len(actions), "duration", time.Since(start))
	return actions, nil
}

func scanScheduledActions(rows *sql.Rows) ([]oasis.ScheduledAction, error) {
	var actions []oasis.ScheduledAction
	for rows.Next() {
		var a oasis.ScheduledAction
		var enabled int
		if err := rows.Scan(&a.ID, &a.Description, &a.Schedule, &a.ToolCalls, &a.SynthesisPrompt, &a.NextRun, &enabled, &a.SkillID, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Enabled = enabled != 0
		actions = append(actions, a)
	}
	return actions, rows.Err()
}
