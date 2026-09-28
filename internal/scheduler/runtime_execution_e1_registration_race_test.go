package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

type executionRegistrationRaceRepository struct {
	*executionLifecycleRepository
	mu              sync.Mutex
	rows            map[int64]*ScheduledJob
	activateEntered chan struct{}
	activateRelease chan struct{}
}

func (r *executionRegistrationRaceRepository) CreateScheduledJob(ctx context.Context, job *ScheduledJob) (*ScheduledJob, error) {
	created, err := r.executionLifecycleRepository.CreateScheduledJob(ctx, job)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.rows[created.ID] = created
	r.mu.Unlock()
	return created, nil
}

func (r *executionRegistrationRaceRepository) ActivateScheduledJob(ctx context.Context, id int64) error {
	close(r.activateEntered)
	select {
	case <-r.activateRelease:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.rows[id]
	if row == nil {
		return errors.New("scheduled row was removed during activation")
	}
	row.Status = JobStatusPending
	return nil
}

func (r *executionRegistrationRaceRepository) GetScheduledJob(_ context.Context, id int64) (*ScheduledJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.rows[id]
	if row == nil {
		return nil, nil
	}
	copy := *row
	return &copy, nil
}

func (r *executionRegistrationRaceRepository) DeleteScheduledJob(_ context.Context, id int64) error {
	r.mu.Lock()
	delete(r.rows, id)
	r.mu.Unlock()
	return nil
}

func TestRuntimeExecutionE1_RecoveryDoesNotDeleteActiveRegistration(t *testing.T) {
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := &executionPreparedScheduleStore{executionLifecycleScheduleStore: newExecutionLifecycleScheduleStore()}
	repo := &executionRegistrationRaceRepository{
		executionLifecycleRepository: &executionLifecycleRepository{},
		rows:                         make(map[int64]*ScheduledJob),
		activateEntered:              make(chan struct{}),
		activateRelease:              make(chan struct{}),
	}
	manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{Definitions: definitions, Schedules: schedules}, nil)
	if err := manager.RegisterHandler("scheduler.action", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(repo, nil)
	engine.SetJobsManager(manager)

	scheduleDone := make(chan error, 1)
	go func() {
		_, err := engine.ScheduleOnce(context.Background(), 8, "chat", 0, time.Now().Add(time.Hour), ActionMessage, "hello")
		scheduleDone <- err
	}()
	select {
	case <-repo.activateEntered:
	case <-time.After(time.Second):
		t.Fatal("registration did not reach activation")
	}

	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- engine.recoverPreparedScheduleRegistrations(context.Background()) }()
	select {
	case err := <-recoveryDone:
		t.Fatalf("recovery completed while registration was active: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(repo.activateRelease)
	if err := <-scheduleDone; err != nil {
		t.Fatalf("registration failed after recovery overlap: %v", err)
	}
	if err := <-recoveryDone; err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	if row, _ := repo.GetScheduledJob(context.Background(), 1); row == nil || row.Status != JobStatusPending {
		t.Fatalf("published compatibility row = %+v, want pending", row)
	}
	if schedule, ok := schedules.schedule(redesignedScheduleID(1)); !ok || !schedule.Enabled {
		t.Fatalf("published schedule = %+v, exists=%t", schedule, ok)
	}
}
