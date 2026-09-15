package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

func TestDrainWaitsForAcceptedAttempt(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	manager, _, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error {
			close(started)
			<-release
			return nil
		},
		jobs.JobRetryPolicy{MaxAttempts: 1},
	)

	if _, _, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:drain-active"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("accepted attempt never started")
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- manager.Drain(drainCtx) }()

	select {
	case err := <-drained:
		t.Fatalf("Drain returned before the accepted attempt settled: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("Drain after accepted attempt completion: %v", err)
		}
	case <-drainCtx.Done():
		t.Fatalf("Drain did not converge after accepted attempt completed: %v", drainCtx.Err())
	}
}

func TestDrainDoesNotWaitForFutureRetryBackoff(t *testing.T) {
	manager, store, _ := engineBackedManager(t,
		func(context.Context, jobs.JobDefinition) error { return errTestFailure },
		jobs.JobRetryPolicy{MaxAttempts: 2, InitialDelay: 30 * time.Second},
	)

	ticket, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:drain-retry")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticket.Wait(context.Background()); err != nil {
		t.Fatalf("first attempt ticket: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Drain(ctx); err != nil {
		t.Fatalf("Drain waited on future retry backoff: %v", err)
	}

	attempts, err := store.CountAttempts(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempts after drain=%d, want 1 retry-pending durable attempt", attempts)
	}

	// Once drained, the retry driver must remain quiesced. The unresolved
	// occurrence is intentionally left for startup recovery rather than opening
	// a second physical attempt during teardown.
	time.Sleep(100 * time.Millisecond)
	attempts, err = store.CountAttempts(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempts grew after drain=%d, want 1", attempts)
	}
}
