package scheduler

import (
	"context"
	"errors"

	"github.com/inipew/goultroid/internal/jobs"
)

// publishScheduledRegistration prefers the production shared-SQLite
// transaction exposed by Jobs Manager. The returned bool tells compensation
// whether an owned wrapper is safe to remove after a failure. A publisher
// error can include an uncertain commit result, so its wrapper is retained;
// the compatibility fallback also retains it after an uncertain enable write.
func (e *Engine) publishScheduledRegistration(ctx context.Context, job *ScheduledJob, registration scheduledRegistration) (bool, error) {
	if e == nil || job == nil || e.jobsMgr == nil {
		return true, errors.New("scheduler registration publisher is not configured")
	}
	if supported, err := e.jobsMgr.PublishScheduleRegistration(ctx, redesignedScheduleID(job.ID), job.ID); supported {
		var beforeCommit *jobs.ScheduleRegistrationPublishError
		return err != nil && errors.As(err, &beforeCommit), err
	}
	if err := e.db.ActivateScheduledJob(ctx, job.ID); err != nil {
		return true, err
	}
	if err := e.saveRedesignedSchedule(ctx, job, registration.definitionID, true); err != nil {
		return false, err
	}
	return false, nil
}
