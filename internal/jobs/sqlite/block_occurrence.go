package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

// BlockOccurrence records an occurrence that cannot be safely executed because
// its definition/handler is unavailable. Blocked is intentionally distinct from
// Failed: no physical attempt failed, and operators must be able to diagnose or
// repair the missing execution capability without recovery repeatedly redriving
// the occurrence.
func (s *Store) BlockOccurrence(ctx context.Context, occurrenceID, reason string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin block occurrence: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE job_occurrences SET revision = revision WHERE id = ?`, occurrenceID); err != nil {
		return fmt.Errorf("acquire block writer intent: %w", err)
	}
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM job_occurrences WHERE id = ?`, occurrenceID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOccurrenceNotFound
		}
		return err
	}
	if state == string(jobs.OccurrenceBlocked) {
		return tx.Commit()
	}
	switch jobs.OccurrenceState(state) {
	case jobs.OccurrenceReady, jobs.OccurrenceDispatched:
	default:
		return fmt.Errorf("%w: occurrence %s is %s", ErrLeaseFencingLost, occurrenceID, state)
	}

	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
		UPDATE job_occurrences
		SET state = ?, revision = revision + 1, updated_at = ?
		WHERE id = ? AND state IN ('ready', 'dispatched')`,
		string(jobs.OccurrenceBlocked), now, occurrenceID,
	)
	if err != nil {
		return fmt.Errorf("block occurrence: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("block rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: occurrence %s changed concurrently", ErrLeaseFencingLost, occurrenceID)
	}

	// Keep diagnostic reason durable and observable through the outbox without
	// inventing a physical JobAttempt for work that never started.
	eventID := fmt.Sprintf("outbox:block:%s:%d", occurrenceID, now.UnixNano())
	payload := []byte(fmt.Sprintf(`{"occurrence_id":%q,"reason":%q}`, occurrenceID, reason))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO job_outbox (event_id, occurrence_id, kind, payload, committed_at, delivery_state)
		VALUES (?, ?, 'occurrence_blocked', ?, ?, 'pending')`,
		eventID, occurrenceID, payload, now,
	); err != nil {
		return fmt.Errorf("record blocked occurrence event: %w", err)
	}
	return tx.Commit()
}
