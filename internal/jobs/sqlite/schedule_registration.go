package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

// ListPreparedSchedules returns scheduler registration rows that were inserted
// disabled at revision 1 and never reached the publish update. Higher revisions
// are intentionally excluded because they may represent cancel/quarantine or
// ambiguous-publish compensation rather than an interrupted registration.
func (s *Store) ListPreparedSchedules(ctx context.Context, prefix string, limit int) ([]jobs.JobSchedule, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, errors.New("prepared schedule prefix is required")
	}
	if limit <= 0 || limit > 256 {
		limit = 256
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, job_id, recurrence, interval_seconds, timezone, next_due_at,
		       misfire_policy, overlap_policy, enabled, revision
		FROM job_schedules
		WHERE enabled = 0 AND revision = 1 AND id LIKE ?
		ORDER BY id
		LIMIT ?`, prefix+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("list prepared schedules: %w", err)
	}
	defer rows.Close()

	var schedules []jobs.JobSchedule
	for rows.Next() {
		var schedule jobs.JobSchedule
		var intervalSeconds int64
		var misfire, overlap string
		var enabled int
		if err := rows.Scan(
			&schedule.ID, &schedule.JobID, &schedule.Recurrence, &intervalSeconds, &schedule.Timezone,
			&schedule.NextDueAt, &misfire, &overlap, &enabled, &schedule.Revision,
		); err != nil {
			return nil, fmt.Errorf("scan prepared schedule: %w", err)
		}
		schedule.Interval = time.Duration(intervalSeconds) * time.Second
		schedule.MisfirePolicy = jobs.MisfirePolicy(misfire)
		schedule.OverlapPolicy = jobs.OverlapPolicy(overlap)
		schedule.Enabled = enabled == 1
		schedules = append(schedules, schedule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list prepared schedules: %w", err)
	}
	return schedules, nil
}

// DeletePreparedSchedule deletes only the disabled revision-1 registration
// form. A published, cancelled, quarantined, or ambiguously compensated row is
// outside this recovery path and cannot be removed by this method.
func (s *Store) DeletePreparedSchedule(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("prepared schedule id is required")
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM job_schedules
		WHERE id = ? AND enabled = 0 AND revision = 1`, id)
	if err != nil {
		return fmt.Errorf("delete prepared schedule %s: %w", id, err)
	}
	return nil
}
