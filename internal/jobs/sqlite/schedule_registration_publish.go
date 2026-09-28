package sqlite

import (
	"context"
	"errors"
	"fmt"
)

// PublishScheduleRegistration atomically publishes the scheduler compatibility
// row and its already-prepared redesigned schedule. ResourceStore is the
// production store wired to the same SQLite database as scheduler.Repository;
// the base Store intentionally does not expose this cross-table capability so
// split-database tests and alternate stores keep using the conservative staged
// fallback.
func (s *ResourceStore) PublishScheduleRegistration(ctx context.Context, scheduleID string, scheduledJobID int64) error {
	if s == nil || s.Store == nil || s.db == nil {
		return errors.New("schedule registration store is not configured")
	}
	if scheduleID == "" || scheduledJobID <= 0 {
		return errors.New("schedule registration identifiers are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schedule registration publish: %w", err)
	}
	defer tx.Rollback()

	rowResult, err := tx.ExecContext(ctx, `
		UPDATE scheduled_jobs
		SET status = 'pending'
		WHERE id = ? AND status = 'initializing'`, scheduledJobID)
	if err != nil {
		return fmt.Errorf("activate scheduled compatibility row %d: %w", scheduledJobID, err)
	}
	rows, err := rowResult.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("scheduled compatibility row %d is not initializing", scheduledJobID)
	}

	scheduleResult, err := tx.ExecContext(ctx, `
		UPDATE job_schedules
		SET enabled = 1, revision = revision + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND enabled = 0 AND revision = 1`, scheduleID)
	if err != nil {
		return fmt.Errorf("publish redesigned schedule %s: %w", scheduleID, err)
	}
	rows, err = scheduleResult.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("redesigned schedule %s is not prepared", scheduleID)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schedule registration publish: %w", err)
	}
	return nil
}
