package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

type executionPreparedScheduleStore struct {
	*executionLifecycleScheduleStore
}

func (s *executionPreparedScheduleStore) ListPreparedSchedules(_ context.Context, prefix string, limit int) ([]jobs.JobSchedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		return nil, nil
	}
	out := make([]jobs.JobSchedule, 0, limit)
	for _, schedule := range s.schedules {
		if schedule.Enabled || schedule.Revision != 1 || !strings.HasPrefix(schedule.ID, prefix) {
			continue
		}
		out = append(out, schedule)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *executionPreparedScheduleStore) DeletePreparedSchedule(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if schedule, ok := s.schedules[id]; ok && !schedule.Enabled && schedule.Revision == 1 {
		delete(s.schedules, id)
	}
	return nil
}

type executionRegistrationRecoveryRepository struct {
	Repository
	rows    map[int64]*ScheduledJob
	deleted []int64
}

func (r *executionRegistrationRecoveryRepository) GetScheduledJob(_ context.Context, id int64) (*ScheduledJob, error) {
	row := r.rows[id]
	if row == nil {
		return nil, nil
	}
	copy := *row
	return &copy, nil
}

func (r *executionRegistrationRecoveryRepository) DeleteScheduledJob(_ context.Context, id int64) error {
	delete(r.rows, id)
	r.deleted = append(r.deleted, id)
	return nil
}

func newExecutionRegistrationRecoveryEngine(t *testing.T, repo Repository, definitions *executionLifecycleDefinitionStore, schedules *executionPreparedScheduleStore) (*Engine, *jobs.Manager) {
	t.Helper()
	manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{
		Definitions: definitions,
		Schedules:   schedules,
	}, nil)
	if err := manager.RegisterHandler("scheduler.action", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatalf("register scheduler action handler: %v", err)
	}
	engine := NewEngine(repo, nil)
	engine.SetJobsManager(manager)
	return engine, manager
}

func TestRuntimeExecutionE1_RestartCleansPreparedWrapperRegistration(t *testing.T) {
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := &executionPreparedScheduleStore{executionLifecycleScheduleStore: newExecutionLifecycleScheduleStore()}
	repo := &executionRegistrationRecoveryRepository{rows: map[int64]*ScheduledJob{
		1: {ID: 1, Status: JobStatusPending},
	}}
	engine, manager := newExecutionRegistrationRecoveryEngine(t, repo, definitions, schedules)
	if err := manager.Register(jobs.JobDefinition{
		ID:          scheduledDefinitionID(1),
		ScopeOwner:  scheduledTaskScope(1),
		QuotaOwner:  "scheduler",
		HandlerType: "scheduler.action",
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveSchedule(context.Background(), jobs.JobSchedule{
		ID:            redesignedScheduleID(1),
		JobID:         scheduledDefinitionID(1),
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

	if err := engine.recoverPreparedScheduleRegistrations(context.Background()); err != nil {
		t.Fatalf("recover prepared registration: %v", err)
	}
	if _, ok := repo.rows[1]; ok {
		t.Fatal("prepared compatibility row survived restart cleanup")
	}
	if _, ok := manager.Definition(scheduledDefinitionID(1)); ok {
		t.Fatal("prepared scheduler wrapper survived restart cleanup")
	}
	if _, ok := schedules.schedule(redesignedScheduleID(1)); ok {
		t.Fatal("prepared redesigned schedule survived restart cleanup")
	}
}

func TestRuntimeExecutionE1_RestartCleansPreparedActionJobWithoutDeletingTarget(t *testing.T) {
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := &executionPreparedScheduleStore{executionLifecycleScheduleStore: newExecutionLifecycleScheduleStore()}
	repo := &executionRegistrationRecoveryRepository{rows: map[int64]*ScheduledJob{
		2: {ID: 2, Status: JobStatusInitializing},
	}}
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
	if err := manager.SaveSchedule(context.Background(), jobs.JobSchedule{
		ID:            redesignedScheduleID(2),
		JobID:         "target-job",
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

	if err := engine.recoverPreparedScheduleRegistrations(context.Background()); err != nil {
		t.Fatalf("recover prepared ActionJob: %v", err)
	}
	if _, ok := manager.Definition("target-job"); !ok {
		t.Fatal("restart cleanup deleted caller-owned ActionJob target")
	}
	if _, ok := schedules.schedule(redesignedScheduleID(2)); ok {
		t.Fatal("prepared ActionJob schedule survived restart cleanup")
	}
}

func TestRuntimeExecutionE1_RestartIgnoresHigherRevisionDisabledSchedule(t *testing.T) {
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := &executionPreparedScheduleStore{executionLifecycleScheduleStore: newExecutionLifecycleScheduleStore()}
	repo := &executionRegistrationRecoveryRepository{rows: map[int64]*ScheduledJob{
		3: {ID: 3, Status: JobStatusPending},
	}}
	engine, _ := newExecutionRegistrationRecoveryEngine(t, repo, definitions, schedules)
	schedules.schedules[redesignedScheduleID(3)] = jobs.JobSchedule{
		ID:       redesignedScheduleID(3),
		JobID:    scheduledDefinitionID(3),
		Enabled:  false,
		Revision: 3,
	}

	if err := engine.recoverPreparedScheduleRegistrations(context.Background()); err != nil {
		t.Fatalf("recover prepared registrations: %v", err)
	}
	if _, ok := repo.rows[3]; !ok {
		t.Fatal("higher-revision disabled schedule was treated as never-published registration")
	}
	if _, ok := schedules.schedule(redesignedScheduleID(3)); !ok {
		t.Fatal("higher-revision disabled schedule was deleted by startup recovery")
	}
}
