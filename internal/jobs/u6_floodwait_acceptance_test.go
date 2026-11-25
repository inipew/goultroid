package jobs_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/telegram"
)

func TestU6_S06_TelegramRPCFloodWaitHandoffCancellationAndAmbiguous(t *testing.T) {
	t.Run("long FloodWait yields to durable AttemptDeferred without sleeper", func(t *testing.T) {
		sleeper := &durableYieldSleeper{}
		rpcExec, err := telegram.NewRPCExecutor(telegram.RPCExecutorConfig{
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
				return rpcExec.Do(ctx, telegram.RPCMeta{
					Method: "messages.sendMessage",
					Kind:   telegram.RPCNonIdempotentMutation,
				}, func(context.Context) error {
					rpcCalls.Add(1)
					return tgerr.New(420, "FLOOD_WAIT_30")
				})
			},
			jobs.JobRetryPolicy{MaxAttempts: 1, MaxDeferrals: 2},
		)

		ticket, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "u6:s06:floodwait30")
		if err != nil {
			t.Fatal(err)
		}
		res, err := ticket.Wait(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != tasks.OutcomeFailed || res.Cause != tasks.CauseRateLimited || res.RetryAfter != 30*time.Second {
			t.Fatalf("unexpected result: %+v", res)
		}
		if sleeper.calls.Load() != 0 {
			t.Fatalf("RPC sleeper called %d times; durable FloodWait must yield", sleeper.calls.Load())
		}
		if rpcCalls.Load() != 1 {
			t.Fatalf("rpcCalls = %d, want 1", rpcCalls.Load())
		}
		latest, err := store.LatestAttempt(context.Background(), occurrenceID)
		if err != nil {
			t.Fatal(err)
		}
		if latest.State != jobs.AttemptDeferred {
			t.Fatalf("attempt state = %s, want AttemptDeferred", latest.State)
		}
		occ, err := store.GetOccurrence(context.Background(), occurrenceID)
		if err != nil {
			t.Fatal(err)
		}
		if want := res.FinishedAt.Add(30 * time.Second); !occ.ReadyAt.Equal(want) {
			t.Fatalf("ReadyAt = %v, want %v", occ.ReadyAt, want)
		}
	})

	t.Run("cancellation precedence overrides retryable RPC", func(t *testing.T) {
		sleeper := &durableYieldSleeper{}
		rpcExec, err := telegram.NewRPCExecutor(telegram.RPCExecutorConfig{
			Limiter: telegram.NoopRPCLimiter{},
			Sleeper: sleeper,
		})
		if err != nil {
			t.Fatal(err)
		}

		manager, store, _ := engineBackedManager(t,
			func(ctx context.Context, _ jobs.JobDefinition) error {
				callCtx, cancel := context.WithCancel(ctx)
				cancel()
				return rpcExec.Do(callCtx, telegram.RPCMeta{
					Method: "messages.getHistory",
					Kind:   telegram.RPCReadOnly,
				}, func(c context.Context) error {
					return c.Err()
				})
			},
			jobs.JobRetryPolicy{MaxAttempts: 3},
		)

		ticket, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "u6:s06:cancel")
		if err != nil {
			t.Fatal(err)
		}
		res, err := ticket.Wait(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != tasks.OutcomeCancelled {
			t.Fatalf("expected OutcomeCancelled, got %+v", res)
		}
		occ, err := store.GetOccurrence(context.Background(), occurrenceID)
		if err != nil {
			t.Fatal(err)
		}
		if occ.State != jobs.OccurrenceCancelled {
			t.Fatalf("occurrence state = %s, want OccurrenceCancelled", occ.State)
		}
	})

	t.Run("ambiguous non-idempotent RPC is permanent and never retried", func(t *testing.T) {
		sleeper := &durableYieldSleeper{}
		rpcExec, err := telegram.NewRPCExecutor(telegram.RPCExecutorConfig{
			Limiter: telegram.NoopRPCLimiter{},
			Sleeper: sleeper,
			DefaultPolicy: telegram.RetryPolicy{
				MaxAttempts:        3,
				BaseDelay:          5 * time.Millisecond,
				MaxDelay:           20 * time.Millisecond,
				MaxElapsed:         time.Second,
				InlineFloodWaitMax: 5 * time.Second,
			},
		})
		if err != nil {
			t.Fatal(err)
		}

		var rpcCalls atomic.Int32
		manager, store, _ := engineBackedManager(t,
			func(ctx context.Context, _ jobs.JobDefinition) error {
				return rpcExec.Do(ctx, telegram.RPCMeta{
					Method: "messages.sendMessage",
					Kind:   telegram.RPCNonIdempotentMutation,
				}, func(context.Context) error {
					rpcCalls.Add(1)
					return errors.New("connection reset by peer")
				})
			},
			jobs.JobRetryPolicy{MaxAttempts: 3},
		)

		ticket, occurrenceID, err := manager.SubmitOccurrence(context.Background(), "job-retry", "u6:s06:ambiguous")
		if err != nil {
			t.Fatal(err)
		}
		res, err := ticket.Wait(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != tasks.OutcomeFailed {
			t.Fatalf("expected OutcomeFailed, got %+v", res)
		}
		if rpcCalls.Load() != 1 {
			t.Fatalf("ambiguous non-idempotent RPC was retried %d times, want 1", rpcCalls.Load())
		}
		// Wait deterministically for retry monitor to finalize occurrence as failed.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			occ, gerr := store.GetOccurrence(context.Background(), occurrenceID)
			if gerr == nil && occ.State == jobs.OccurrenceFailed {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		occ, _ := store.GetOccurrence(context.Background(), occurrenceID)
		t.Fatalf("expected OccurrenceFailed for ambiguous non-idempotent RPC, got %+v", occ)
	})
}
