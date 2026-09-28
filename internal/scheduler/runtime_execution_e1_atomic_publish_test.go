package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

type executionAtomicScheduleStore struct {
	*executionLifecycleScheduleStore
	publishCalls int
	publishErr   error
}

func (s *executionAtomicScheduleStore) PublishScheduleRegistration(_ context.Context, scheduleID string, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishCalls++
	schedule, ok := s.schedules[scheduleID]
	if !ok {
		return errors.New("prepared schedule missing")
	}
	if schedule.Enabled || schedule.Revision != 1 {
		return errors.New("schedule is not in prepared state")
	}
	schedule.Enabled = true
	schedule.Revision++
	s.schedules[scheduleID] = schedule
	return s.publishErr
}

type executionAtomicPublishRepository struct {
	*executionLifecycleRepository
	activateCalls int
}

func (r *executionAtomicPublishRepository) ActivateScheduledJob(context.Context, int64) error {
	r.activateCalls++
	return errors.New("legacy activation must be owned by atomic publisher")
}

func TestRuntimeExecutionE1_AtomicPublisherOwnsActivationAndSchedulePublish(t *testing.T) {
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := &executionAtomicScheduleStore{executionLifecycleScheduleStore: newExecutionLifecycleScheduleStore()}
	repo := &executionAtomicPublishRepository{executionLifecycleRepository: &executionLifecycleRepository{}}
	manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{
		Definitions: definitions,
		Schedules:   schedules,
	}, nil)
	if err := manager.RegisterHandler("scheduler.action", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(repo, nil)
	engine.SetJobsManager(manager)

	job, err := engine.ScheduleOnce(context.Background(), 8, "chat", 0, time.Now().Add(time.Hour), ActionMessage, "hello")
	if err != nil {
		t.Fatalf("schedule once through atomic publisher: %v", err)
	}
	if repo.activateCalls != 0 {
		t.Fatalf("legacy ActivateScheduledJob calls = %d, want 0", repo.activateCalls)
	}
	if schedules.publishCalls != 1 {
		t.Fatalf("atomic publish calls = %d, want 1", schedules.publishCalls)
	}
	schedule, ok := schedules.schedule(redesignedScheduleID(job.ID))
	if !ok || !schedule.Enabled || schedule.Revision != 2 {
		t.Fatalf("published schedule = %+v exists=%t, want enabled revision 2", schedule, ok)
	}
}

func TestRuntimeExecutionE1_AmbiguousAtomicPublishRetainsWrapper(t *testing.T) {
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := &executionAtomicScheduleStore{
		executionLifecycleScheduleStore: newExecutionLifecycleScheduleStore(),
		publishErr:                      errors.New("commit result uncertain"),
	}
	repo := &executionLifecycleRepository{}
	manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{Definitions: definitions, Schedules: schedules}, nil)
	if err := manager.RegisterHandler("scheduler.action", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(repo, nil)
	engine.SetJobsManager(manager)
	_, err := engine.ScheduleOnce(context.Background(), 8, "chat", 0, time.Now().Add(time.Hour), ActionMessage, "hello")
	if !errors.Is(err, schedules.publishErr) {
		t.Fatalf("schedule error = %v, want ambiguous publish error", err)
	}
	if _, ok := manager.Definition(scheduledDefinitionID(1)); !ok {
		t.Fatal("owned wrapper was deleted after an ambiguous commit")
	}
	if schedule, ok := schedules.schedule(redesignedScheduleID(1)); !ok || schedule.Enabled {
		t.Fatalf("schedule after compensation = %+v, exists=%t, want disabled retained schedule", schedule, ok)
	}
}
