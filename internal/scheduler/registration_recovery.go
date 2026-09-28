package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	schedulerPreparedSchedulePrefix = "sched:scheduled:"
	schedulerPreparedRecoveryLimit  = 256
)

func scheduledJobIDFromScheduleID(scheduleID string) (int64, error) {
	raw := strings.TrimPrefix(scheduleID, schedulerPreparedSchedulePrefix)
	if raw == scheduleID || strings.TrimSpace(raw) == "" {
		return 0, fmt.Errorf("invalid scheduler schedule id %q", scheduleID)
	}
	jobID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || jobID <= 0 {
		return 0, fmt.Errorf("invalid scheduler schedule id %q", scheduleID)
	}
	return jobID, nil
}

// recoverPreparedScheduleRegistrations removes durable registration state that
// was prepared disabled but never published before a process stop/crash. The
// recovery path is intentionally cleanup-only: a scheduling call whose publish
// step never completed is not silently revived on restart. Work is read in
// bounded batches, while the enclosing timeout bounds total startup recovery.
func (e *Engine) recoverPreparedScheduleRegistrations(ctx context.Context) error {
	if e == nil || e.db == nil || e.jobsMgr == nil {
		return nil
	}
	recoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	for {
		schedules, err := e.jobsMgr.ListPreparedSchedules(recoveryCtx, schedulerPreparedSchedulePrefix, schedulerPreparedRecoveryLimit)
		if err != nil {
			return err
		}
		if len(schedules) == 0 {
			return nil
		}

		var recoveryErrs []error
		for _, schedule := range schedules {
			if recoveryCtx.Err() != nil {
				recoveryErrs = append(recoveryErrs, recoveryCtx.Err())
				break
			}
			jobID, parseErr := scheduledJobIDFromScheduleID(schedule.ID)
			if parseErr != nil {
				recoveryErrs = append(recoveryErrs, parseErr)
				continue
			}
			row, readErr := e.db.GetScheduledJob(recoveryCtx, jobID)
			if readErr != nil {
				recoveryErrs = append(recoveryErrs, fmt.Errorf("read prepared scheduled row %d: %w", jobID, readErr))
				continue
			}
			if row != nil && row.Status != JobStatusInitializing && row.Status != JobStatusPending {
				continue
			}
			if row != nil {
				if deleteErr := e.db.DeleteScheduledJob(recoveryCtx, jobID); deleteErr != nil {
					recoveryErrs = append(recoveryErrs, fmt.Errorf("delete prepared scheduled row %d: %w", jobID, deleteErr))
					continue
				}
			}

			// Scheduler-owned wrapper definitions are transient registration state.
			// ActionJob points at a caller-owned target definition and must never
			// delete it. If wrapper cleanup fails, retain the revision-1 prepared
			// schedule as the durable retry marker for the next restart.
			if schedule.JobID == scheduledDefinitionID(jobID) {
				if deleteErr := e.jobsMgr.DeleteDefinition(recoveryCtx, schedule.JobID); deleteErr != nil {
					recoveryErrs = append(recoveryErrs, fmt.Errorf("delete prepared wrapper %s: %w", schedule.JobID, deleteErr))
					continue
				}
			}
			// DeletePreparedSchedule is called explicitly even though deleting an
			// owned SQLite wrapper normally cascades the schedule. This keeps the
			// recovery contract correct for non-SQLite stores and ActionJob targets.
			if deleteErr := e.jobsMgr.DeletePreparedSchedule(recoveryCtx, schedule.ID); deleteErr != nil {
				recoveryErrs = append(recoveryErrs, fmt.Errorf("delete prepared schedule %s: %w", schedule.ID, deleteErr))
			}
		}
		if err := errors.Join(recoveryErrs...); err != nil {
			return err
		}
		if len(schedules) < schedulerPreparedRecoveryLimit {
			return nil
		}
	}
}
