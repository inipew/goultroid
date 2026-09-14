package jobs

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// TimerEntry is reference-only scheduler input. It never contains a handler.
type TimerEntry struct {
	ScheduleID tasks.ScheduleID
	JobID      tasks.JobID
	Deadline   time.Time
	Generation uint64
	Sequence   uint64
}

// DeadlineSink receives due references from Scheduler. Scheduler does not own
// occurrence creation, retry policy, or feature execution.
type DeadlineSink interface {
	Due(context.Context, TimerEntry) error
}

// StoreTx is the narrow transaction surface consumed by JobManager. Concrete
// SQLite details and SQL types stay in internal/jobs/sqlite in later phases.
type StoreTx interface {
	PutDefinition(context.Context, JobDefinition) error
	PutSchedule(context.Context, JobSchedule) error
	PutOccurrence(context.Context, JobOccurrence) error
	PutAttempt(context.Context, JobAttempt) error
	Commit() error
	Rollback() error
}

// Repository owns transaction creation; application services own transaction
// boundaries rather than leaking database handles into the domain model.
type Repository interface {
	Begin(context.Context) (StoreTx, error)
}
