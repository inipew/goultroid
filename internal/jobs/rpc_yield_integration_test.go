package jobs_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/telegram"
)

type durableYieldSleeper struct {
	calls atomic.Int32
}

func (s *durableYieldSleeper) Sleep(context.Context, time.Duration) error {
	s.calls.Add(1)
	return nil
}

func TestDurableJobYieldsShortServerFloodWaitWithoutRPCSleep(t *testing.T) {
	sleeper := &durableYieldSleeper{}
	rpcExecutor, err := telegram.NewRPCExecutor(telegram.RPCExecutorConfig{
		Limiter: telegram.NoopRPCLimiter{},
		Sleeper: sleeper,
		DefaultPolicy: telegram.RetryPolicy{
			MaxAttempts:        3,
			BaseDelay:          10 * time.Millisecond,
			MaxDelay:           100 * time.Millisecond,
			MaxElapsed:         5 * time.Second,
			InlineFloodWaitMax: 5 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var rpcCalls atomic.Int32
	manager, store, _ := engineBackedManager(t,
		func(ctx context.Context, _ jobs.JobDefinition) error {
			return rpcExecutor.Do(ctx, telegram.RPCMeta{
				Method: "messages.sendMessage",
				Kind:   telegram.RPCNonIdempotentMutation,
			}, func(context.Context) error {
				rpcCalls.Add(1)
				return tgerr.New(420, "FLOOD_WAIT_1")
			})
		},
		jobs.JobRetryPolicy{MaxAttempts: 1, MaxDeferrals: 2},
	)

	ticket, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:durable-rpc-yield")
	if err != nil {
		t.Fatal(err)
	}
	res, err := ticket.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != tasks.OutcomeFailed || res.Cause != tasks.CauseRateLimited || res.RetryAfter != time.Second {
		t.Fatalf("unexpected durable RPC result: %+v", res)
	}
	if sleeper.calls.Load() != 0 {
		t.Fatalf("RPC sleeper called %d times; durable FloodWait must yield", sleeper.calls.Load())
	}
	if rpcCalls.Load() != 1 {
		t.Fatalf("physical RPC calls=%d, want 1 before durable yield", rpcCalls.Load())
	}
	if res.Failure.Message == "" {
		t.Fatal("diagnostic failure message missing")
	}

	latest, err := store.LatestAttempt(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.State != jobs.AttemptDeferred {
		t.Fatalf("attempt state=%s, want deferred", latest.State)
	}
	occurrence, err := store.GetOccurrence(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	wantReadyAt := res.FinishedAt.Add(time.Second)
	if !occurrence.ReadyAt.Equal(wantReadyAt) {
		t.Fatalf("ready_at=%v, want deterministic %v", occurrence.ReadyAt, wantReadyAt)
	}
}

type fixedDenyLimiter struct {
	wait     time.Duration
	reserves atomic.Int32
}

func (l *fixedDenyLimiter) Reserve(time.Time, []telegram.LimitKey, int) telegram.Reservation {
	l.reserves.Add(1)
	return telegram.Reservation{Allowed: false, RetryAfter: l.wait}
}

func (*fixedDenyLimiter) Penalize(time.Time, []telegram.LimitKey, time.Duration) {}

func TestDurableJobPersistsLimiterWaitThatCannotFitRPCBudget(t *testing.T) {
	const wait = 500 * time.Millisecond
	sleeper := &durableYieldSleeper{}
	limiter := &fixedDenyLimiter{wait: wait}
	rpcExecutor, err := telegram.NewRPCExecutor(telegram.RPCExecutorConfig{
		Limiter: limiter,
		Sleeper: sleeper,
		DefaultPolicy: telegram.RetryPolicy{
			MaxAttempts:        3,
			BaseDelay:          10 * time.Millisecond,
			MaxDelay:           100 * time.Millisecond,
			MaxElapsed:         100 * time.Millisecond,
			InlineFloodWaitMax: 5 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var rpcCalls atomic.Int32
	manager, store, _ := engineBackedManager(t,
		func(ctx context.Context, _ jobs.JobDefinition) error {
			return rpcExecutor.Do(ctx, telegram.RPCMeta{
				Method: "messages.getHistory",
				Kind:   telegram.RPCReadOnly,
			}, func(context.Context) error {
				rpcCalls.Add(1)
				return nil
			})
		},
		jobs.JobRetryPolicy{MaxAttempts: 1, MaxDeferrals: 2},
	)

	ticket, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "manual:durable-limiter-yield")
	if err != nil {
		t.Fatal(err)
	}
	res, err := ticket.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != tasks.OutcomeFailed || res.Cause != tasks.CauseRateLimited || res.RetryAfter != wait {
		t.Fatalf("unexpected limiter deferral result: %+v", res)
	}
	if sleeper.calls.Load() != 0 {
		t.Fatalf("limiter wait occupied RPC sleeper: calls=%d", sleeper.calls.Load())
	}
	if rpcCalls.Load() != 0 {
		t.Fatalf("physical RPC executed before limiter admission: calls=%d", rpcCalls.Load())
	}
	if limiter.reserves.Load() != 1 {
		t.Fatalf("limiter reserve calls=%d, want 1 before durable deferral", limiter.reserves.Load())
	}
	latest, err := store.LatestAttempt(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.State != jobs.AttemptDeferred {
		t.Fatalf("attempt state=%s, want deferred", latest.State)
	}
	occurrence, err := store.GetOccurrence(context.Background(), occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if want := res.FinishedAt.Add(wait); !occurrence.ReadyAt.Equal(want) {
		t.Fatalf("ready_at=%v, want %v", occurrence.ReadyAt, want)
	}
}
