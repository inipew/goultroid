package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

func TestRuntimeExecutionE1_RestartDrainsPreparedRegistrationsAcrossBatches(t *testing.T) {
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := &executionPreparedScheduleStore{executionLifecycleScheduleStore: newExecutionLifecycleScheduleStore()}
	repo := &executionRegistrationRecoveryRepository{rows: make(map[int64]*ScheduledJob)}
	engine, manager := newExecutionRegistrationRecoveryEngine(t, repo, definitions, schedules)
	if err := manager.RegisterHandler("target.handler", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(jobs.JobDefinition{
		ID:          "target-job",
		ScopeOwner:  "target",
		QuotaOwner:  "target",
		HandlerType: "target.handler",
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}

	const prepared = schedulerPreparedRecoveryLimit + 44
	for i := 1; i <= prepared; i++ {
		jobID := int64(i)
		repo.rows[jobID] = &ScheduledJob{ID: jobID, Status: JobStatusInitializing}
		if err := manager.SaveSchedule(context.Background(), jobs.JobSchedule{
			ID:            redesignedScheduleID(jobID),
			JobID:         "target-job",
			Recurrence:    "once",
			Timezone:      "UTC",
			NextDueAt:     time.Now().UTC().Add(time.Hour),
			MisfirePolicy: jobs.MisfireRunOnce,
			OverlapPolicy: jobs.OverlapForbid,
			Enabled:       false,
			Revision:      1,
		}); err != nil {
			t.Fatalf("save prepared schedule %d: %v", jobID, err)
		}
	}

	if err := engine.recoverPreparedScheduleRegistrations(context.Background()); err != nil {
		t.Fatalf("recover prepared registrations: %v", err)
	}
	if len(repo.rows) != 0 {
		t.Fatalf("compatibility rows remaining after multi-batch recovery = %d, want 0", len(repo.rows))
	}
	if _, ok := manager.Definition("target-job"); !ok {
		t.Fatal("multi-batch recovery deleted caller-owned ActionJob target")
	}
	for i := 1; i <= prepared; i++ {
		if _, ok := schedules.schedule(redesignedScheduleID(int64(i))); ok {
			t.Fatalf("prepared schedule %d survived multi-batch recovery", i)
		}
	}
}
