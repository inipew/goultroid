package scheduler

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/queue"
	"github.com/inipew/goultroid/internal/tasks"
)

type periodicTrySubmitter struct {
	submitCalls int
	tryCalls    int
}

func (s *periodicTrySubmitter) Submit(context.Context, string, tasks.Task) error {
	s.submitCalls++
	return queue.ErrQueueFull
}

func (s *periodicTrySubmitter) TrySubmit(context.Context, string, tasks.Task) error {
	s.tryCalls++
	return queue.ErrQueueFull
}

func TestPeriodicCoordinatorUsesNonBlockingAdmissionWhenAvailable(t *testing.T) {
	coordinator := newPeriodicCoordinator(nil)
	submitter := &periodicTrySubmitter{}
	coordinator.SetSubmitter(submitter)
	coordinator.entries["maintenance"] = &periodicRegistration{
		Owner:      "runtime",
		Name:       "maintenance",
		Generation: 1,
		Running:    true,
	}

	coordinator.startExecution(periodicDueRun{
		name:       "maintenance",
		generation: 1,
		options:    PeriodicTaskOptions{Owner: "runtime"},
		ctx:        context.Background(),
		task:       func(context.Context) error { return nil },
	})

	if submitter.submitCalls != 0 || submitter.tryCalls != 1 {
		t.Fatalf("periodic admission calls = Submit:%d TrySubmit:%d, want 0/1", submitter.submitCalls, submitter.tryCalls)
	}
	entry := coordinator.entries["maintenance"]
	if entry.Running {
		t.Fatal("failed non-blocking admission left periodic registration running")
	}
	if entry.Failures != 1 || entry.LastError != queue.ErrQueueFull.Error() {
		t.Fatalf("periodic admission failure not recorded: %+v", entry)
	}
}
