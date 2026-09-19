package telegram

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

type fakeLimiter struct {
	mu          sync.Mutex
	deny        bool
	reservation Reservation
	penalized   []time.Duration
}

func (f *fakeLimiter) Reserve(time.Time, []LimitKey, int) Reservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deny {
		return f.reservation
	}
	if f.reservation != (Reservation{}) {
		return f.reservation
	}
	return Reservation{Allowed: true}
}

func (f *fakeLimiter) Penalize(_ time.Time, _ []LimitKey, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.penalized = append(f.penalized, d)
}

func newTestExecutor(limiter RPCRequestLimiter, clock Clock, sleeper Sleeper, metrics RPCMetrics) *RPCExecutor {
	exec, err := NewRPCExecutor(RPCExecutorConfig{
		Limiter:    limiter,
		Clock:      clock,
		Sleeper:    sleeper,
		Metrics:    metrics,
		RandSource: rand.New(rand.NewSource(42)),
		DefaultPolicy: RetryPolicy{
			MaxAttempts:        3,
			BaseDelay:          10 * time.Millisecond,
			MaxDelay:           100 * time.Millisecond,
			MaxElapsed:         1 * time.Second,
			InlineFloodWaitMax: 2 * time.Second,
			JitterFraction:     0.2,
		},
	})
	if err != nil {
		panic(err)
	}
	return exec
}

// 1. Success initial attempt
func TestRPCExecutor_Case1_SuccessInitial(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCReadOnly,
	}, func(ctx context.Context) error {
		calls++
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
	if sleeper.Calls() != 0 {
		t.Fatalf("expected 0 sleeps, got %d", sleeper.Calls())
	}
}

// 2. Transient then success
func TestRPCExecutor_Case2_TransientThenSuccess(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "contacts.resolveUsername",
		Kind:   RPCReadOnly,
	}, func(ctx context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("temporary failure connection reset")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
	if sleeper.Calls() != 1 {
		t.Fatalf("expected 1 sleep, got %d", sleeper.Calls())
	}
}

// 3. Transient exhaust
func TestRPCExecutor_Case3_TransientExhaust(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "contacts.resolveUsername",
		Kind:   RPCReadOnly,
	}, func(ctx context.Context) error {
		calls++
		return errors.New("temporary failure timeout")
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var failure *RPCFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected RPCFailure, got %T", err)
	}
	if failure.Attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", failure.Attempts)
	}
	if failure.Class != RPCTransient {
		t.Fatalf("expected RPCTransient class, got %v", failure.Class)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

// 4. Permanent no retry
func TestRPCExecutor_Case4_PermanentNoRetry(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCReadOnly,
	}, func(ctx context.Context) error {
		calls++
		return tgerr.New(403, "CHAT_WRITE_FORBIDDEN")
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var failure *RPCFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected RPCFailure, got %T", err)
	}
	if failure.Attempts != 1 {
		t.Fatalf("expected exactly 1 attempt for permanent error, got %d", failure.Attempts)
	}
	if failure.Class != RPCPermission {
		t.Fatalf("expected RPCPermission, got %v", failure.Class)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

// 5. Canceled before first attempt
func TestRPCExecutor_Case5_CanceledBeforeFirstAttempt(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls int
	err := exec.Do(ctx, RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCReadOnly,
	}, func(ctx context.Context) error {
		calls++
		return nil
	})

	if err == nil {
		t.Fatal("expected error on canceled context")
	}
	var failure *RPCFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected RPCFailure, got %T", err)
	}
	if failure.Attempts != 0 {
		t.Fatalf("expected 0 attempts before cancel, got %d", failure.Attempts)
	}
	if calls != 0 {
		t.Fatalf("expected 0 calls, got %d", calls)
	}
}

// 6. Canceled during limiter wait
func TestRPCExecutor_Case6_CanceledDuringLimiterWait(t *testing.T) {
	clock := NewFakeClock(time.Now())
	// Sleeper returns context cancellation
	sleeper := &FakeSleeper{err: context.Canceled}
	limiter := &fakeLimiter{
		reservation: Reservation{Allowed: false, RetryAfter: 50 * time.Millisecond},
	}
	exec := newTestExecutor(limiter, clock, sleeper, nil)

	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCReadOnly,
	}, func(ctx context.Context) error {
		calls++
		return nil
	})

	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected 0 calls, got %d", calls)
	}
}

type sequenceLimiter struct {
	mu           sync.Mutex
	reservations []Reservation
	calls        int
}

func (s *sequenceLimiter) Reserve(time.Time, []LimitKey, int) Reservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(s.reservations) == 0 {
		return Reservation{Allowed: true}
	}
	res := s.reservations[0]
	s.reservations = s.reservations[1:]
	return res
}

func (s *sequenceLimiter) Penalize(time.Time, []LimitKey, time.Duration) {}

func TestRPCExecutor_LimiterWaitReReservesBeforeRPC(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	limiter := &sequenceLimiter{reservations: []Reservation{
		{Allowed: false, RetryAfter: 10 * time.Millisecond},
		{Allowed: true},
	}}
	exec := newTestExecutor(limiter, clock, sleeper, nil)

	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCReadOnly,
	}, func(context.Context) error {
		calls++
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limiter.calls != 2 {
		t.Fatalf("expected limiter to be reserved twice, got %d", limiter.calls)
	}
	if sleeper.Calls() != 1 {
		t.Fatalf("expected exactly one limiter wait, got %d", sleeper.Calls())
	}
	if calls != 1 {
		t.Fatalf("expected exactly one physical RPC call, got %d", calls)
	}
}

func TestRPCExecutor_LimiterDeniedZeroRetryAfter_FailClosed(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	limiter := &fakeLimiter{
		deny:        true,
		reservation: Reservation{Allowed: false, RetryAfter: 0},
	}
	exec := newTestExecutor(limiter, clock, sleeper, nil)

	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCReadOnly,
	}, func(ctx context.Context) error {
		calls++
		return nil
	})

	if err == nil {
		t.Fatal("expected rate limit denial error, got nil")
	}
	if !errors.Is(err, core.ErrRateLimit) {
		t.Fatalf("expected errors.Is core.ErrRateLimit, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected 0 calls due to fail-closed denial, got %d", calls)
	}
}

// 7. Canceled during backoff
func TestRPCExecutor_Case7_CanceledDuringBackoff(t *testing.T) {
	clock := NewFakeClock(time.Now())
	// Let first call succeed sleep then fail, or return context.Canceled on sleep
	sleeper := &FakeSleeper{err: context.Canceled}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCReadOnly,
	}, func(ctx context.Context) error {
		calls++
		return errors.New("transient timeout")
	})

	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call before backoff cancel, got %d", calls)
	}
}

// 8. Parent deadline shorter than default
func TestRPCExecutor_Case8_ParentDeadlineShorterThanDefault(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	parentCtx, pCancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer pCancel()

	var opDeadline time.Time
	err := exec.Do(parentCtx, RPCMeta{
		Method:  "messages.sendMessage",
		Kind:    RPCReadOnly,
		Timeout: 5 * time.Second, // longer than parent
	}, func(ctx context.Context) error {
		d, ok := ctx.Deadline()
		if ok {
			opDeadline = d
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parentDeadline, _ := parentCtx.Deadline()
	if !opDeadline.Equal(parentDeadline) {
		t.Fatalf("expected opDeadline %v to equal parentDeadline %v", opDeadline, parentDeadline)
	}
}

// 9. FloodWait below threshold (wait + retry)
func TestRPCExecutor_Case9_FloodWaitBelowThreshold(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	limiter := &fakeLimiter{}
	exec := newTestExecutor(limiter, clock, sleeper, nil)

	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCReadOnly,
	}, func(ctx context.Context) error {
		calls++
		if calls == 1 {
			return tgerr.New(420, "FLOOD_WAIT_1")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
	if sleeper.Calls() != 1 {
		t.Fatalf("expected 1 sleep, got %d", sleeper.Calls())
	}
	if len(limiter.penalized) != 1 || limiter.penalized[0] != time.Second {
		t.Fatalf("expected limiter penalized by 1s, got %+v", limiter.penalized)
	}
}

// 10. FloodWait above threshold (return RateLimitError)
func TestRPCExecutor_Case10_FloodWaitAboveThreshold(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	limiter := &fakeLimiter{}
	exec := newTestExecutor(limiter, clock, sleeper, nil)

	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCReadOnly,
	}, func(ctx context.Context) error {
		calls++
		return tgerr.New(420, "FLOOD_WAIT_10") // 10s > 2s InlineFloodWaitMax
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, core.ErrRateLimit) {
		t.Fatalf("expected core.ErrRateLimit, got %v", err)
	}
	var failure *RPCFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected RPCFailure, got %T", err)
	}
	if failure.RetryAfter != 10*time.Second {
		t.Fatalf("expected 10s retry after, got %v", failure.RetryAfter)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
	if sleeper.Calls() != 0 {
		t.Fatalf("expected 0 sleeps, got %d", sleeper.Calls())
	}
}

// 11. Stale peer refresh once
func TestRPCExecutor_Case11_StalePeerRefreshOnce(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	var refreshed int
	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCReadOnly,
		RefreshPeer: func(ctx context.Context) error {
			refreshed++
			return nil
		},
	}, func(ctx context.Context) error {
		calls++
		if calls == 1 {
			return tgerr.New(400, "PEER_ID_INVALID")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refreshed != 1 {
		t.Fatalf("expected 1 refresh, got %d", refreshed)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
}

// 12. Stale peer twice stops
func TestRPCExecutor_Case12_StalePeerTwiceStops(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	var refreshed int
	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCReadOnly,
		RefreshPeer: func(ctx context.Context) error {
			refreshed++
			return nil
		},
	}, func(ctx context.Context) error {
		calls++
		return tgerr.New(400, "PEER_ID_INVALID")
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if refreshed != 1 {
		t.Fatalf("expected refresh exactly once, got %d", refreshed)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
}

// 13. Non-idempotent ambiguous no retry
func TestRPCExecutor_Case13_NonIdempotentAmbiguousNoRetry(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCNonIdempotentMutation,
	}, func(ctx context.Context) error {
		calls++
		return errors.New("read: connection reset by peer")
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var failure *RPCFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected RPCFailure, got %T", err)
	}
	if !failure.Ambiguous {
		t.Fatal("expected ambiguous flag to be true for non-idempotent mutation transient error")
	}
	if calls != 1 {
		t.Fatalf("expected no retry for non-idempotent mutation, calls=%d", calls)
	}
}

// 14. Attempts count includes initial request
func TestRPCExecutor_Case14_AttemptsCountIncludesInitial(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	var attempts atomic.Int32
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCReadOnly,
		RetryPolicy: RetryPolicy{
			MaxAttempts: 1, // MaxAttempts: 1 means initial only
		},
	}, func(ctx context.Context) error {
		attempts.Add(1)
		return errors.New("transient error timeout")
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var failure *RPCFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected RPCFailure, got %T", err)
	}
	if failure.Attempts != 1 {
		t.Fatalf("expected Attempts=1, got %d", failure.Attempts)
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", attempts.Load())
	}
}

// 15. Jitter bounded
func TestRPCExecutor_Case15_JitterBounded(t *testing.T) {
	exec, err := NewRPCExecutor(RPCExecutorConfig{
		DefaultPolicy: RetryPolicy{
			MaxAttempts:    5,
			BaseDelay:      100 * time.Millisecond,
			MaxDelay:       1 * time.Second,
			JitterFraction: 0.5,
		},
	})
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}

	for attempt := 1; attempt <= 10; attempt++ {
		for iter := 0; iter < 50; iter++ {
			d := exec.calculateBackoff(exec.defaultPolicy, attempt)
			if d < 0 {
				t.Fatalf("delay cannot be negative: %v", d)
			}
			if d > exec.defaultPolicy.MaxDelay {
				t.Fatalf("delay %v exceeded MaxDelay %v", d, exec.defaultPolicy.MaxDelay)
			}
		}
	}
}

// 16. No metrics high-cardinality value
func TestRPCExecutor_Case16_NoMetricsHighCardinalityValue(t *testing.T) {
	metrics := NewInMemoryRPCMetrics()
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, metrics)

	err := exec.Do(context.Background(), RPCMeta{
		Method:  "contacts.resolveUsername",
		PeerKey: "sensitive_user_123456",
		Kind:    RPCReadOnly,
	}, func(ctx context.Context) error {
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	snap := metrics.Snapshot()
	if snap.TotalRequests != 1 {
		t.Fatalf("expected 1 request, got %d", snap.TotalRequests)
	}
}

// ExecuteRPC generic test
func TestExecuteRPC(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	res, err := ExecuteRPC(context.Background(), exec, RPCMeta{
		Method: "messages.getHistory",
		Kind:   RPCReadOnly,
	}, func(ctx context.Context) (string, error) {
		return "hello result", nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "hello result" {
		t.Fatalf("expected 'hello result', got %q", res)
	}
}

func TestRPCExecutor_HardMaxElapsedBudget(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)

	policy := RetryPolicy{
		MaxAttempts:        5,
		BaseDelay:          100 * time.Millisecond,
		MaxDelay:           500 * time.Millisecond,
		MaxElapsed:         200 * time.Millisecond,
		InlineFloodWaitMax: 5 * time.Second,
	}

	calls := 0
	err := exec.Do(context.Background(), RPCMeta{
		Method:      "messages.sendMessage",
		Kind:        RPCReadOnly,
		RetryPolicy: policy,
	}, func(ctx context.Context) error {
		calls++
		// Advance clock past MaxElapsed
		clock.Advance(250 * time.Millisecond)
		return tgerr.New(500, "RPC_CALL_FAIL")
	})

	if err == nil {
		t.Fatal("expected error due to exceeded MaxElapsed budget")
	}
	var failure *RPCFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected RPCFailure, got %T", err)
	}
	if calls >= 5 {
		t.Fatalf("expected attempts to be cut short by MaxElapsed budget, got %d calls", calls)
	}
}


func TestRPCExecutor_DurableContextYieldsShortFloodWait(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	limiter := &fakeLimiter{}
	exec := newTestExecutor(limiter, clock, sleeper, nil)

	ctx := execution.WithMetadata(context.Background(), execution.Metadata{CanDurablyYield: true})
	var calls int
	err := exec.Do(ctx, RPCMeta{
		Method: "messages.sendMessage",
		Kind:   RPCNonIdempotentMutation,
	}, func(context.Context) error {
		calls++
		return tgerr.New(420, "FLOOD_WAIT_1")
	})
	if err == nil {
		t.Fatal("expected durable FloodWait yield")
	}
	if !errors.Is(err, core.ErrRateLimit) {
		t.Fatalf("expected rate-limit signal, got %v", err)
	}
	var failure *RPCFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected RPCFailure, got %T", err)
	}
	if failure.RetryAfter != time.Second {
		t.Fatalf("retry_after=%s, want 1s", failure.RetryAfter)
	}
	var rateLimit *core.RateLimitError
	if !errors.As(err, &rateLimit) || rateLimit.RateLimitWait() != time.Second {
		t.Fatalf("structured rate-limit wait missing: %v", err)
	}
	if calls != 1 {
		t.Fatalf("durable FloodWait retried inline: calls=%d", calls)
	}
	if sleeper.Calls() != 0 {
		t.Fatalf("durable FloodWait occupied sleeper: calls=%d", sleeper.Calls())
	}
	if len(limiter.penalized) != 1 || limiter.penalized[0] != time.Second {
		t.Fatalf("server FloodWait must still penalize shared limiter: %+v", limiter.penalized)
	}
}


type oneWaitLimiter struct {
	mu      sync.Mutex
	wait    time.Duration
	reserves int
}

func (l *oneWaitLimiter) Reserve(time.Time, []LimitKey, int) Reservation {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reserves++
	if l.reserves == 1 {
		return Reservation{Allowed: false, RetryAfter: l.wait}
	}
	return Reservation{Allowed: true}
}

func (*oneWaitLimiter) Penalize(time.Time, []LimitKey, time.Duration) {}

func TestRPCExecutor_InteractiveShortLimiterWaitRemainsInline(t *testing.T) {
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	limiter := &oneWaitLimiter{wait: 25 * time.Millisecond}
	exec := newTestExecutor(limiter, clock, sleeper, nil)

	var calls int
	err := exec.Do(context.Background(), RPCMeta{
		Method: "messages.getHistory",
		Kind:   RPCReadOnly,
	}, func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("interactive limiter wait failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("physical RPC calls=%d, want 1", calls)
	}
	if sleeper.Calls() != 1 || sleeper.TotalSleep() != 25*time.Millisecond {
		t.Fatalf("interactive limiter wait was not kept inline: calls=%d total=%s", sleeper.Calls(), sleeper.TotalSleep())
	}
}
