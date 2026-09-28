package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

func TestRuntimeExecutionE3_ActionJobTargetsDefinitionWithoutSchedulerWrapper(t *testing.T) {
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := newExecutionLifecycleScheduleStore()
	repo := &executionLifecycleRepository{}
	engine, manager := newExecutionLifecycleEngine(t, repo, definitions, schedules)
	if err := manager.RegisterHandler("target.handler", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(jobs.JobDefinition{
		ID:          "target-job",
		ScopeOwner:  "test",
		QuotaOwner:  "test",
		HandlerType: "target.handler",
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}

	created, err := engine.ScheduleOnce(context.Background(), 1, "chat", 0, time.Now().Add(time.Hour), ActionJob, "target-job")
	if err != nil {
		t.Fatalf("schedule ActionJob: %v", err)
	}
	schedule, ok := schedules.schedule(redesignedScheduleID(created.ID))
	if !ok {
		t.Fatal("redesigned ActionJob schedule was not persisted")
	}
	if !schedule.Enabled {
		t.Fatal("successful ActionJob schedule is not enabled")
	}
	if schedule.JobID != "target-job" {
		t.Fatalf("schedule target = %q, want caller-owned target-job", schedule.JobID)
	}
	if _, ok := manager.Definition(scheduledDefinitionID(created.ID)); ok {
		t.Fatal("ActionJob unexpectedly created a scheduler.action wrapper definition")
	}
}
