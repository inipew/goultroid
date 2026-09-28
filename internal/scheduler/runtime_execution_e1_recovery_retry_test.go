package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

type executionRegistrationDeleteFailStore struct {
	*executionLifecycleDefinitionStore
	deleteErr error
}

func (s *executionRegistrationDeleteFailStore) DeleteDefinition(ctx context.Context, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.executionLifecycleDefinitionStore.DeleteDefinition(ctx, id)
}

func TestRuntimeExecutionE1_RestartRetainsPreparedMarkerWhenWrapperCleanupFails(t *testing.T) {
	cleanupErr := errors.New("definition cleanup unavailable")
	definitions := &executionRegistrationDeleteFailStore{
		executionLifecycleDefinitionStore: newExecutionLifecycleDefinitionStore(),
		deleteErr:                         cleanupErr,
	}
	schedules := &executionPreparedScheduleStore{executionLifecycleScheduleStore: newExecutionLifecycleScheduleStore()}
	repo := &executionRegistrationRecoveryRepository{rows: map[int64]*ScheduledJob{
		4: {ID: 4, Status: JobStatusPending},
	}}
	manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{
		Definitions: definitions,
		Schedules:   schedules,
	}, nil)
	if err := manager.RegisterHandler("scheduler.action", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(repo, nil)
	engine.SetJobsManager(manager)
	if err := manager.Register(jobs.JobDefinition{
		ID:          scheduledDefinitionID(4),
		ScopeOwner:  scheduledTaskScope(4),
		QuotaOwner:  "scheduler",
		HandlerType: "scheduler.action",
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveSchedule(context.Background(), jobs.JobSchedule{
		ID:            redesignedScheduleID(4),
		JobID:         scheduledDefinitionID(4),
		Recurrence:    "once",
		Timezone:      "UTC",
		NextDueAt:     time.Now().UTC().Add(time.Hour),
		MisfirePolicy: jobs.MisfireRunOnce,
		OverlapPolicy: jobs.OverlapForbid,
		Enabled:       false,
		Revision:      1,
	}); err != nil {
		t.Fatal(err)
	}

	err := engine.recoverPreparedScheduleRegistrations(context.Background())
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("recovery error = %v, want wrapper cleanup failure", err)
	}
	if _, ok := repo.rows[4]; ok {
		t.Fatal("compatibility row survived successful first cleanup step")
	}
	if _, ok := manager.Definition(scheduledDefinitionID(4)); !ok {
		t.Fatal("wrapper disappeared despite durable delete failure")
	}
	if _, ok := schedules.schedule(redesignedScheduleID(4)); !ok {
		t.Fatal("prepared schedule retry marker was removed after wrapper cleanup failure")
	}

	definitions.deleteErr = nil
	if err := engine.recoverPreparedScheduleRegistrations(context.Background()); err != nil {
		t.Fatalf("retry prepared registration cleanup: %v", err)
	}
	if _, ok := manager.Definition(scheduledDefinitionID(4)); ok {
		t.Fatal("wrapper survived successful retry cleanup")
	}
	if _, ok := schedules.schedule(redesignedScheduleID(4)); ok {
		t.Fatal("prepared schedule retry marker survived successful retry cleanup")
	}
}
