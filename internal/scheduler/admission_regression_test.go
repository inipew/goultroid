package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/queue"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
	"go.uber.org/zap"
)

func saturatedWorkers(t *testing.T, poolName string) *workers.Manager {
	t.Helper()
	m := workers.NewManager()
	tm := tasks.NewManager()
	tm.SetOwnerQuota("pressure", tasks.Quota{})
	m.SetTasksManager(tm)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := m.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	p, ok := m.Get(poolName)
	if !ok {
		t.Fatal("missing pool")
	}
	started := make(chan struct{}, p.Stats().Concurrency)
	for i := 0; i < p.Stats().Concurrency; i++ {
		if err := m.Submit(context.Background(), poolName, tasks.Task{
			ID: fmt.Sprintf("hold-%d", i), Owner: "pressure", Run: func(context.Context) error {
				started <- struct{}{}
				<-release
				return nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < p.Stats().Concurrency; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("worker did not start")
		}
	}
	capacity := p.Stats().QueueStats.Capacity
	for i := 0; i < capacity; i++ {
		if err := m.Submit(context.Background(), poolName, tasks.Task{
			ID: fmt.Sprintf("queue-%d", i), Owner: "pressure", Run: func(context.Context) error { return nil },
		}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for p.Stats().QueueStats.Depth < capacity {
		if time.Now().After(deadline) {
			t.Fatal("physical queue did not fill")
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < capacity; i++ {
		if err := m.TrySubmit(context.Background(), poolName, tasks.Task{
			ID: fmt.Sprintf("admission-%d", i), Owner: "pressure", Run: func(context.Context) error { return nil },
		}); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestManagedTriggerRejectsSaturatedSchedulerAdmission(t *testing.T) {
	m := saturatedWorkers(t, workers.PoolScheduler)
	jm := jobs.NewManager(m)
	if err := jm.Register(jobs.Job{ID: "managed", Owner: "managed", Pool: workers.PoolScheduler,
		Run: func(context.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(nil, nil, nil, nil, zap.NewNop())
	engine.SetJobsManager(jm)
	engine.ctx = context.Background()
	result := make(chan error, 1)
	go func() { result <- engine.executeManagedJob(context.Background(), ScheduledJob{Payload: "managed"}) }()
	select {
	case err := <-result:
		if !errors.Is(err, queue.ErrQueueFull) {
			t.Fatalf("trigger error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler trigger waited for saturated pool")
	}
	job, _ := jm.Get("managed")
	if job.State != jobs.StateRegistered {
		t.Fatalf("rejected job state = %s", job.State)
	}
}

func TestPeriodicTimerProgressesWhenGeneralAdmissionIsFull(t *testing.T) {
	m := saturatedWorkers(t, workers.PoolGeneral)
	c := newPeriodicCoordinator(zap.NewNop())
	c.SetSubmitter(m)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(context.Background()) }()
	for _, name := range []string{"first", "second"} {
		if err := c.Register(name, 5*time.Millisecond, PeriodicTaskOptions{Owner: name}, func(context.Context) error {
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s := c.Snapshots()
		if len(s) == 2 && s[0].Failures >= 2 && s[1].Failures >= 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timer stopped processing due registrations: %+v", c.Snapshots())
}

type capturedPeriodicSubmitter struct{ submitted chan tasks.Task }

func (s capturedPeriodicSubmitter) TrySubmit(_ context.Context, _ string, task tasks.Task) error {
	s.submitted <- task
	return nil
}

func TestPeriodicRetriesUseSeparateTasksAndTimerDelay(t *testing.T) {
	c := newPeriodicCoordinator(zap.NewNop())
	s := capturedPeriodicSubmitter{submitted: make(chan tasks.Task, 4)}
	c.SetSubmitter(s)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(context.Background()) }()
	calls := 0
	want := errors.New("retry me")
	if err := c.Register("retry", time.Hour, PeriodicTaskOptions{MaxAttempts: 2, RetryDelay: 100 * time.Millisecond}, func(context.Context) error {
		calls++
		if calls == 1 {
			return want
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.entries["retry"].NextRun = time.Now()
	c.mu.Unlock()
	c.notify()
	var first tasks.Task
	select {
	case first = <-s.submitted:
	case <-time.After(time.Second):
		t.Fatal("first task not submitted")
	}
	if err := first.Execute(context.Background()); !errors.Is(err, want) {
		t.Fatalf("first attempt = %v", err)
	}
	if calls != 1 {
		t.Fatalf("one Task ran %d attempts", calls)
	}
	select {
	case <-s.submitted:
		t.Fatal("retry did not respect timer delay")
	default:
	}
	var second tasks.Task
	select {
	case second = <-s.submitted:
	case <-time.After(time.Second):
		t.Fatal("retry not submitted")
	}
	if first.ID == second.ID {
		t.Fatal("retry reused a Task ID")
	}
	if err := second.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshots := c.Snapshots(); snapshots[0].Runs != 2 || snapshots[0].Failures != 1 {
		t.Fatalf("attempt metrics = %+v", snapshots)
	}
}
