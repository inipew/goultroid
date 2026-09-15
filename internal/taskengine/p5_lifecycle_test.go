package taskengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestStopDeadlineNotBlockedByCompletionCallback(t *testing.T) {
	e := NewEngine(Config{
		Pools:          map[tasks.PoolID]PoolEngineConfig{"p": {Concurrency: 1, BacklogLimit: 4}},
		ResultCapacity: 4, DeliveryConcurrency: 1, DeliveryQueueCap: 2,
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	callbackStarted := make(chan struct{})
	release := make(chan struct{})
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID: "blocked-callback", Pool: "p", QuotaOwner: "owner",
		Handler: func(context.Context) error { return nil },
		OnComplete: func(tasks.TaskResult) {
			close(callbackStarted)
			<-release
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticket.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-callbackStarted

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = e.Stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("Stop exceeded bounded deadline grace: %v", elapsed)
	}
	close(release)
}

func TestForceStopFencesCommitPending(t *testing.T) {
	e := NewEngine(Config{Pools: map[tasks.PoolID]PoolEngineConfig{"p": {Concurrency: 1, BacklogLimit: 4}}, ResultCapacity: 4})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	commitStarted := make(chan struct{})
	release := make(chan struct{})
	_, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID: "commit-pending", Pool: "p", QuotaOwner: "owner",
		Handler: func(context.Context) error { return nil },
		Commit: func(context.Context, tasks.TaskResult) error {
			close(commitStarted)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-commitStarted
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_ = e.ForceStop(ctx)
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("ForceStop was not bounded: %v", elapsed)
	}
	close(release)
}

func TestForceStopMarksDurableInFlightRecoveryRequired(t *testing.T) {
	e := NewEngine(Config{
		Pools:          map[tasks.PoolID]PoolEngineConfig{"p": {Concurrency: 1, BacklogLimit: 4}},
		ResultCapacity: 4,
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	handlerStarted := make(chan struct{})
	release := make(chan struct{})
	_, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID: "durable-inflight", Pool: "p", QuotaOwner: "owner",
		Handler: func(context.Context) error {
			close(handlerStarted)
			<-release // Deliberately ignore cancellation to model an uncooperative side effect.
			return nil
		},
		Commit: func(context.Context, tasks.TaskResult) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	<-handlerStarted

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = e.ForceStop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected hard deadline while handler is still running, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("ForceStop exceeded bounded deadline grace: %v", elapsed)
	}

	snapshot, found := e.applySnapshot("durable-inflight")
	if !found {
		t.Fatal("durable task disappeared during forced stop")
	}
	if snapshot.State != tasks.StateRecoveryRequired {
		t.Fatalf("durable in-flight forced stop state=%s, want %s", snapshot.State, tasks.StateRecoveryRequired)
	}
	close(release)
}
