package database

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// JobStatusCancelled is a terminal scheduler state. It is intentionally kept
// out of Repository for backward compatibility with existing repository mocks;
// Engine uses the optional schedulerCanceller capability when available.
const JobStatusCancelled = "cancelled"

// CancelScheduledJob transitions a pending/running job to a durable cancelled
// state instead of deleting it. Execution history remains queryable, while the
// claim token and lease are fenced so the worker cannot commit the old job.
func (d *DB) CancelScheduledJob(ctx context.Context, id int64, now time.Time) error {
	if id <= 0 {
		return errors.New("invalid scheduled job ID")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	res, err := d.ExecContext(ctx, `
		UPDATE scheduled_jobs
		SET status = ?,
		    last_error = 'cancelled by user',
		    lease_until = NULL,
		    claim_token = '',
		    last_finished_at = ?
		WHERE id = ? AND status IN ('pending', 'running')
	`, JobStatusCancelled, now, id)
	if err != nil {
		return fmt.Errorf("failed to cancel scheduled job: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("scheduled job not found or already terminal")
	}
	return nil
}
