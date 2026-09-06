package scheduler

import (
	"context"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

// authorizeJobAccess resolves the durable job before any mutation or history
// lookup. A job can only be operated on from its own chat, and the requester
// must either be its creator or a configured sudo/owner account.
func (e *Engine) authorizeJobAccess(ctx context.Context, requesterID, chatID, jobID int64) (*database.ScheduledJob, error) {
	if requesterID <= 0 || chatID == 0 || jobID <= 0 {
		return nil, core.ErrPermissionDenied
	}
	job, err := e.db.GetScheduledJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, core.ErrNotFound
	}
	if job.ChatID != chatID {
		return nil, core.ErrPermissionDenied
	}
	if job.CreatedBy == requesterID {
		return job, nil
	}
	if e.perms != nil && e.perms.IsSudo(requesterID) {
		return job, nil
	}
	return nil, core.ErrPermissionDenied
}

// CancelScoped is the only scheduler cancellation path exposed to plugins.
// It binds requester identity and chat scope to the durable job ID before the
// existing cancellation path removes the row and stops local workers.
func (e *Engine) CancelScoped(ctx context.Context, requesterID, chatID, jobID int64) error {
	if _, err := e.authorizeJobAccess(ctx, requesterID, chatID, jobID); err != nil {
		return err
	}
	return e.Cancel(ctx, jobID)
}

// JobHistoryScoped prevents a caller from reading execution history for a job
// belonging to another chat or creator unless the caller is configured sudo.
func (e *Engine) JobHistoryScoped(ctx context.Context, requesterID, chatID, jobID int64, limit int) ([]database.JobHistoryEntry, error) {
	if _, err := e.authorizeJobAccess(ctx, requesterID, chatID, jobID); err != nil {
		return nil, err
	}
	return e.JobHistory(ctx, jobID, limit)
}

var _ interface {
	CancelScoped(context.Context, int64, int64, int64) error
	JobHistoryScoped(context.Context, int64, int64, int64, int) ([]database.JobHistoryEntry, error)
} = (*Engine)(nil)
