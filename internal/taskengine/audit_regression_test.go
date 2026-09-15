package taskengine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/tasks"
)

func auditEngine(t *testing.T) *Engine {
	t.Helper()
	e := NewEngine(Config{Pools: map[tasks.PoolID]PoolEngineConfig{"a": {Concurrency: 1, BacklogLimit: 10}, "b": {Concurrency: 1, BacklogLimit: 10}}, ResultCapacity: 20})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = e.Stop(ctx)
	})
	return e
}

func waitAuditTicket(t *testing.T, ticket tasks.Ticket) tasks.TaskResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := ticket.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestEngineCompletionUnblocksGlobalQuota(t *testing.T) {
	for _, pool := range []tasks.PoolID{"a", "b"} {
		t.Run(string(pool), func(t *testing.T) {
			e := auditEngine(t)
			e.SetOwnerLimits("owner", admission.OwnerLimits{MaxActive: 1, MaxWaiting: 10})
			release := make(chan struct{})
			var once atomic.Bool
			defer func() {
				if once.CompareAndSwap(false, true) {
					close(release)
				}
			}()
			first, err := e.Submit(context.Background(), tasks.WorkSpec{ID: "first", Pool: "a", QuotaOwner: "owner", Handler: func(context.Context) error { <-release; return nil }})
			if err != nil {
				t.Fatal(err)
			}
			second, err := e.Submit(context.Background(), tasks.WorkSpec{ID: "second", Pool: pool, QuotaOwner: "owner", Handler: func(context.Context) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			if second.State() != tasks.StateQueued {
				t.Fatal("owner quota did not queue second task")
			}
			once.Store(true)
			close(release)
			waitAuditTicket(t, first)
			if res := waitAuditTicket(t, second); !res.IsSuccess() {
				t.Fatal(res)
			}
		})
	}
}

func TestEngineRejectsDuplicateAndInvalidAdmission(t *testing.T) {
	e := auditEngine(t)
	spec := tasks.WorkSpec{ID: "id", Pool: "a", QuotaOwner: "owner", Handler: func(context.Context) error { return nil }}
	ticket, err := e.Submit(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	waitAuditTicket(t, ticket)
	if _, err := e.Submit(context.Background(), spec); err == nil {
		t.Fatal("duplicate task accepted")
	}
	spec.ID = "expired"
	spec.QueueDeadline = time.Now().Add(-time.Second)
	if _, err := e.Submit(context.Background(), spec); !errors.Is(err, tasks.ErrDeadlineExpired) {
		t.Fatal(err)
	}
	if _, found := e.Snapshot(spec.ID); found {
		t.Fatal("rejected task registered")
	}
	spec.ID = "unresolved"
	spec.QueueDeadline = time.Time{}
	spec.Handler = nil
	spec.HandlerRef = "unknown"
	if _, err := e.Submit(context.Background(), spec); !errors.Is(err, tasks.ErrUnknownHandler) {
		t.Fatal(err)
	}
}

func TestEngineCompletionRunsOnceOutsideWorker(t *testing.T) {
	e := auditEngine(t)
	callbackStarted, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	spec := tasks.WorkSpec{ID: "callback", Pool: "a", QuotaOwner: "owner", Handler: func(context.Context) error { return nil }, OnComplete: func(tasks.TaskResult) {
		if calls.Add(1) == 1 {
			close(callbackStarted)
		}
		<-release
		panic("isolated callback")
	}}
	ticket, err := e.Submit(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	waitAuditTicket(t, ticket) // A blocked callback must not block the physical result.
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("missing callback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("completion called %d times", calls.Load())
	}
}

func TestEngineStopFinalizesQueuedBehindUncooperativeHandler(t *testing.T) {
	e := auditEngine(t)
	release := make(chan struct{})
	defer close(release)
	_, err := e.Submit(context.Background(), tasks.WorkSpec{ID: "blocked", Pool: "a", QuotaOwner: "owner", Handler: func(context.Context) error { <-release; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{ID: "queued", Pool: "a", QuotaOwner: "other", Handler: func(context.Context) error { t.Error("queued handler executed after stop"); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if res := waitAuditTicket(t, ticket); res.Outcome != tasks.OutcomeCancelled || res.Cause != tasks.CauseShutdown {
		t.Fatal(res)
	}
}

func TestEngineCopiesAdmittedPayloadAndOccurrence(t *testing.T) {
	e := auditEngine(t)
	payload := []byte("original")
	ref := &tasks.OccurrenceRef{AttemptID: "original"}
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{ID: "copy", Pool: "a", QuotaOwner: "owner", Input: payload, Job: ref, Handler: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	payload[0] = 'X'
	ref.AttemptID = "changed"
	result := waitAuditTicket(t, ticket)
	if result.AttemptID != "original" {
		t.Fatal("caller changed admitted attempt identity")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if string(e.registry["copy"].spec.Input.([]byte)) != "original" {
		t.Fatal("caller changed admitted payload")
	}
}
