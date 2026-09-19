package telegram

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

// RPCOperationKind specifies the safety and idempotency of an RPC method.
type RPCOperationKind uint8

const (
	RPCReadOnly RPCOperationKind = iota + 1
	RPCIdempotentMutation
	RPCNonIdempotentMutation
)

func (k RPCOperationKind) String() string {
	switch k {
	case RPCReadOnly:
		return "read_only"
	case RPCIdempotentMutation:
		return "idempotent_mutation"
	case RPCNonIdempotentMutation:
		return "non_idempotent_mutation"
	default:
		return "unknown"
	}
}

// LimitKey identifies a rate-limiting dimension (e.g. global, method family, or peer).
type LimitKey struct {
	Scope string
	Key   string
}

// Reservation indicates whether an RPC invocation is permitted or delayed.
type Reservation struct {
	Allowed    bool
	RetryAfter time.Duration
}

// RPCRequestLimiter coordinates rate-limiting across multiple dimensions.
type RPCRequestLimiter interface {
	Reserve(now time.Time, dimensions []LimitKey, cost int) Reservation
	Penalize(now time.Time, dimensions []LimitKey, retryAfter time.Duration)
}

// NoopRPCLimiter allows all requests immediately.
type NoopRPCLimiter struct{}

func (NoopRPCLimiter) Reserve(time.Time, []LimitKey, int) Reservation {
	return Reservation{Allowed: true}
}
func (NoopRPCLimiter) Penalize(time.Time, []LimitKey, time.Duration) {}

// RetryPolicy defines backoff, attempt limits, and FloodWait thresholds.
type RetryPolicy struct {
	MaxAttempts        int
	BaseDelay          time.Duration
	MaxDelay           time.Duration
	MaxElapsed         time.Duration
	InlineFloodWaitMax time.Duration
	JitterFraction     float64
}

// Validate ensures the retry policy conforms to invariants.
func (p RetryPolicy) Validate() error {
	if p.MaxAttempts < 1 {
		return errors.New("max attempts must be at least 1")
	}
	if p.BaseDelay < 0 || p.MaxDelay < 0 || p.MaxElapsed < 0 || p.InlineFloodWaitMax < 0 {
		return errors.New("delays and thresholds cannot be negative")
	}
	if p.MaxDelay > 0 && p.BaseDelay > p.MaxDelay {
		return errors.New("base delay cannot exceed max delay")
	}
	if p.JitterFraction < 0 || p.JitterFraction > 1 {
		return errors.New("jitter fraction must be between 0.0 and 1.0")
	}
	return nil
}

// RPCMeta provides operational metadata for an RPC execution.
type RPCMeta struct {
	Method      string
	Family      string
	PeerKey     string
	Kind        RPCOperationKind
	Timeout     time.Duration
	RetryPolicy RetryPolicy
	RefreshPeer func(context.Context) error
}

// RPCFailure holds detailed diagnostic information about a failed RPC call.
type RPCFailure struct {
	Method     string
	Class      RPCErrorClass
	Attempts   int
	RetryAfter time.Duration
	Ambiguous  bool
	Err        error
}

func (f *RPCFailure) Error() string {
	if f == nil {
		return "<nil>"
	}
	amb := ""
	if f.Ambiguous {
		amb = " [ambiguous]"
	}
	if f.RetryAfter > 0 {
		return fmt.Sprintf("rpc %s failed after %d attempt(s) (class=%d%s, retry_after=%v): %v",
			f.Method, f.Attempts, f.Class, amb, f.RetryAfter, f.Err)
	}
	return fmt.Sprintf("rpc %s failed after %d attempt(s) (class=%d%s): %v",
		f.Method, f.Attempts, f.Class, amb, f.Err)
}

func (f *RPCFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Err
}

func defaultExecutorPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:        3,
		BaseDelay:          250 * time.Millisecond,
		MaxDelay:           30 * time.Second,
		MaxElapsed:         45 * time.Second,
		InlineFloodWaitMax: 5 * time.Second,
		JitterFraction:     0.5,
	}
}

// DefaultExecutorPolicy is retained for compatibility. Internal runtime
// construction uses defaultExecutorPolicy so mutations cannot alter later
// production executors.
var DefaultExecutorPolicy = defaultExecutorPolicy()

// RPCExecutor coordinates timeout, rate limiting, error classification, retry, and FloodWait.
type RPCExecutor struct {
	limiter       RPCRequestLimiter
	clock         Clock
	sleeper       Sleeper
	metrics       RPCMetrics
	defaultPolicy RetryPolicy
	randMu        sync.Mutex
	randSource    *rand.Rand
}

// RPCExecutorConfig options for constructing an RPCExecutor.
type RPCExecutorConfig struct {
	Limiter       RPCRequestLimiter
	Clock         Clock
	Sleeper       Sleeper
	Metrics       RPCMetrics
	DefaultPolicy RetryPolicy
	RandSource    *rand.Rand
}

// NewRPCExecutor initializes a verified RPCExecutor instance.
func NewRPCExecutor(cfg RPCExecutorConfig) (*RPCExecutor, error) {
	policy := cfg.DefaultPolicy
	if policy.MaxAttempts == 0 {
		policy = defaultExecutorPolicy()
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("invalid default policy: %w", err)
	}

	clock := cfg.Clock
	if clock == nil {
		clock = DefaultClock
	}
	sleeper := cfg.Sleeper
	if sleeper == nil {
		sleeper = DefaultSleeper
	}
	limiter := cfg.Limiter
	if limiter == nil {
		limiter = NoopRPCLimiter{}
	}
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = NoopRPCMetrics{}
	}
	rSource := cfg.RandSource
	if rSource == nil {
		rSource = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	return &RPCExecutor{
		limiter:       limiter,
		clock:         clock,
		sleeper:       sleeper,
		metrics:       metrics,
		defaultPolicy: policy,
		randSource:    rSource,
	}, nil
}

// Do executes operation under the executor's policies.
func (e *RPCExecutor) Do(ctx context.Context, meta RPCMeta, operation func(context.Context) error) error {
	if meta.Kind == 0 {
		return errors.New("rpc operation kind is required")
	}
	if operation == nil {
		return errors.New("rpc operation cannot be nil")
	}

	policy := meta.RetryPolicy
	if policy.MaxAttempts == 0 {
		policy = e.defaultPolicy
	} else if err := policy.Validate(); err != nil {
		return fmt.Errorf("invalid rpc retry policy: %w", err)
	}

	// Determine operation timeout bounded by MaxElapsed
	timeout := meta.Timeout
	if timeout <= 0 {
		switch meta.Kind {
		case RPCReadOnly:
			timeout = 10 * time.Second
		default:
			timeout = 15 * time.Second
		}
	}
	if policy.MaxElapsed > 0 && policy.MaxElapsed < timeout {
		timeout = policy.MaxElapsed
	}

	opCtx, cancel := core.WithDefaultTimeout(ctx, timeout)
	defer cancel()

	dimensions := []LimitKey{
		{Scope: "global", Key: "account"},
	}
	if meta.Family != "" {
		dimensions = append(dimensions, LimitKey{Scope: "family", Key: meta.Family})
	}
	if meta.Method != "" {
		dimensions = append(dimensions, LimitKey{Scope: "method", Key: meta.Method})
	}
	if meta.PeerKey != "" {
		dimensions = append(dimensions, LimitKey{Scope: "peer", Key: meta.PeerKey})
	}

	startTime := e.clock.Now()
	var refreshedPeer bool

	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if policy.MaxElapsed > 0 && e.clock.Now().Sub(startTime) >= policy.MaxElapsed {
			return &RPCFailure{
				Method:   meta.Method,
				Class:    RPCUnknown,
				Attempts: attempt - 1,
				Err:      fmt.Errorf("rpc max elapsed budget %v exceeded: %w", policy.MaxElapsed, context.DeadlineExceeded),
			}
		}
		if err := opCtx.Err(); err != nil {
			return &RPCFailure{
				Method:   meta.Method,
				Class:    RPCUnknown,
				Attempts: attempt - 1,
				Err:      err,
			}
		}

		// Acquire limiter capacity before every physical RPC attempt. A denied
		// reservation does not consume tokens, so a successful wait must be
		// followed by a fresh reservation instead of falling through to the RPC.
		for {
			reservation := e.limiter.Reserve(e.clock.Now(), dimensions, 1)
			if reservation.Allowed {
				break
			}
			if reservation.RetryAfter <= 0 {
				e.metrics.ObserveFloodWait(meta.Method, 0, true)
				return &RPCFailure{
					Method:   meta.Method,
					Class:    RPCFloodWait,
					Attempts: attempt - 1,
					Err:      core.NewRateLimitError(0, errors.New("rate limit reservation denied")),
				}
			}
			// A durable caller already has an atomic defer-and-redrive protocol.
			// Yield every positive limiter reservation wait before sleeping so a
			// TaskEngine worker is never occupied only waiting for local admission.
			// Interactive callers intentionally retain the bounded inline wait path.
			if execution.CanDurablyYield(opCtx) {
				e.metrics.ObserveFloodWait(meta.Method, reservation.RetryAfter, true)
				return &RPCFailure{
					Method:     meta.Method,
					Class:      RPCFloodWait,
					Attempts:   attempt - 1,
					RetryAfter: reservation.RetryAfter,
					Err:        core.NewRateLimitError(reservation.RetryAfter, errors.New("rate limiter reservation deferred")),
				}
			}
			if policy.MaxElapsed > 0 && e.clock.Now().Add(reservation.RetryAfter).Sub(startTime) > policy.MaxElapsed {
				e.metrics.ObserveFloodWait(meta.Method, reservation.RetryAfter, true)
				return &RPCFailure{
					Method:     meta.Method,
					Class:      RPCFloodWait,
					Attempts:   attempt - 1,
					RetryAfter: reservation.RetryAfter,
					Err:        core.NewRateLimitError(reservation.RetryAfter, fmt.Errorf("rate limiter wait %v exceeds max elapsed %v: %w", reservation.RetryAfter, policy.MaxElapsed, context.DeadlineExceeded)),
				}
			}
			if deadline, ok := opCtx.Deadline(); ok && e.clock.Now().Add(reservation.RetryAfter).After(deadline) {
				e.metrics.ObserveFloodWait(meta.Method, reservation.RetryAfter, true)
				return &RPCFailure{
					Method:     meta.Method,
					Class:      RPCFloodWait,
					Attempts:   attempt - 1,
					RetryAfter: reservation.RetryAfter,
					Err:        core.NewRateLimitError(reservation.RetryAfter, errors.New("rate limit reservation exceeded context deadline")),
				}
			}
			e.metrics.ObserveWait("limiter", reservation.RetryAfter)
			if err := e.sleeper.Sleep(opCtx, reservation.RetryAfter); err != nil {
				return &RPCFailure{
					Method:     meta.Method,
					Class:      RPCUnknown,
					Attempts:   attempt - 1,
					RetryAfter: reservation.RetryAfter,
					Err:        err,
				}
			}
			if err := opCtx.Err(); err != nil {
				return &RPCFailure{
					Method:     meta.Method,
					Class:      RPCUnknown,
					Attempts:   attempt - 1,
					RetryAfter: reservation.RetryAfter,
					Err:        err,
				}
			}
		}

		callStart := e.clock.Now()
		err := operation(opCtx)
		callElapsed := e.clock.Now().Sub(callStart)

		if err == nil {
			e.metrics.ObserveRequest(meta.Method, RPCSuccess, attempt, callElapsed)
			return nil
		}

		class := ClassifyRPCError(err)
		e.metrics.ObserveRequest(meta.Method, class, attempt, callElapsed)

		// Cancellation / deadline check
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || opCtx.Err() != nil {
			return &RPCFailure{
				Method:   meta.Method,
				Class:    class,
				Attempts: attempt,
				Err:      err,
			}
		}

		// Stale peer handling
		if class == RPCStalePeer {
			if meta.RefreshPeer != nil && !refreshedPeer {
				refreshedPeer = true
				if refErr := meta.RefreshPeer(opCtx); refErr == nil {
					if meta.Kind == RPCReadOnly || meta.Kind == RPCIdempotentMutation {
						continue
					}
					return &RPCFailure{
						Method:    meta.Method,
						Class:     class,
						Attempts:  attempt,
						Ambiguous: true,
						Err:       err,
					}
				}
			}
			return &RPCFailure{
				Method:   meta.Method,
				Class:    class,
				Attempts: attempt,
				Err:      err,
			}
		}

		// FloodWait handling
		if class == RPCFloodWait {
			wait, _ := tgerr.AsFloodWait(err)
			if wait <= 0 {
				wait = time.Second
			}
			e.limiter.Penalize(e.clock.Now(), dimensions, wait)
			// Explicit server FloodWait is a safe yield point when the caller has
			// a durable continuation owner. Interactive work retains the existing
			// bounded inline-wait behavior for short waits.
			deferred := execution.CanDurablyYield(opCtx) || wait > policy.InlineFloodWaitMax
			e.metrics.ObserveFloodWait(meta.Method, wait, deferred)

			if deferred {
				return &RPCFailure{
					Method:     meta.Method,
					Class:      class,
					Attempts:   attempt,
					RetryAfter: wait,
					Err:        core.NewRateLimitError(wait, err),
				}
			}

			if policy.MaxElapsed > 0 && e.clock.Now().Add(wait).Sub(startTime) > policy.MaxElapsed {
				return &RPCFailure{
					Method:     meta.Method,
					Class:      class,
					Attempts:   attempt,
					RetryAfter: wait,
					Err:        fmt.Errorf("flood wait %v exceeds max elapsed budget %v: %w", wait, policy.MaxElapsed, core.NewRateLimitError(wait, err)),
				}
			}
			if deadline, ok := opCtx.Deadline(); ok && e.clock.Now().Add(wait).After(deadline) {
				return &RPCFailure{
					Method:     meta.Method,
					Class:      class,
					Attempts:   attempt,
					RetryAfter: wait,
					Err:        core.NewRateLimitError(wait, err),
				}
			}

			if attempt >= policy.MaxAttempts {
				return &RPCFailure{
					Method:     meta.Method,
					Class:      class,
					Attempts:   attempt,
					RetryAfter: wait,
					Err:        core.NewRateLimitError(wait, err),
				}
			}

			e.metrics.ObserveWait("floodwait", wait)
			if sleepErr := e.sleeper.Sleep(opCtx, wait); sleepErr != nil {
				return &RPCFailure{
					Method:     meta.Method,
					Class:      class,
					Attempts:   attempt,
					RetryAfter: wait,
					Err:        sleepErr,
				}
			}
			continue
		}

		// Transient error handling
		if class == RPCTransient {
			if meta.Kind == RPCNonIdempotentMutation {
				return &RPCFailure{
					Method:    meta.Method,
					Class:     class,
					Attempts:  attempt,
					Ambiguous: true,
					Err:       fmt.Errorf("%w (non-idempotent mutation outcome ambiguous)", err),
				}
			}

			if attempt >= policy.MaxAttempts {
				return &RPCFailure{
					Method:   meta.Method,
					Class:    class,
					Attempts: attempt,
					Err:      err,
				}
			}

			delay := e.calculateBackoff(policy, attempt)
			if policy.MaxElapsed > 0 && e.clock.Now().Add(delay).Sub(startTime) > policy.MaxElapsed {
				return &RPCFailure{
					Method:   meta.Method,
					Class:    class,
					Attempts: attempt,
					Err:      err,
				}
			}
			if deadline, ok := opCtx.Deadline(); ok && e.clock.Now().Add(delay).After(deadline) {
				return &RPCFailure{
					Method:   meta.Method,
					Class:    class,
					Attempts: attempt,
					Err:      err,
				}
			}

			e.metrics.ObserveWait("transient", delay)
			if sleepErr := e.sleeper.Sleep(opCtx, delay); sleepErr != nil {
				return &RPCFailure{
					Method:   meta.Method,
					Class:    class,
					Attempts: attempt,
					Err:      sleepErr,
				}
			}
			continue
		}

		// Permanent failure: no retry
		return &RPCFailure{
			Method:   meta.Method,
			Class:    class,
			Attempts: attempt,
			Err:      err,
		}
	}

	return &RPCFailure{
		Method:   meta.Method,
		Class:    RPCUnknown,
		Attempts: policy.MaxAttempts,
		Err:      errors.New("retry attempts exhausted"),
	}
}

func (e *RPCExecutor) calculateBackoff(policy RetryPolicy, attempt int) time.Duration {
	shift := attempt - 1
	if shift < 0 {
		shift = 0
	} else if shift > 30 {
		shift = 30
	}

	base := policy.BaseDelay
	if base <= 0 {
		base = 100 * time.Millisecond
	}

	capDelay := policy.MaxDelay
	if capDelay <= 0 {
		capDelay = 30 * time.Second
	}

	// Calculate exponential backoff safely
	multiplier := time.Duration(1 << shift)
	exponential := base * multiplier
	if exponential > capDelay || exponential <= 0 {
		exponential = capDelay
	}

	if policy.JitterFraction <= 0 {
		return exponential
	}

	e.randMu.Lock()
	delay := time.Duration(e.randSource.Float64() * float64(exponential))
	e.randMu.Unlock()
	return delay
}

// ExecuteRPC bridges non-generic RPCExecutor to generic caller functions.
func ExecuteRPC[T any](ctx context.Context, executor *RPCExecutor, meta RPCMeta, operation func(context.Context) (T, error)) (T, error) {
	var result T
	if executor == nil {
		var zero T
		return zero, errors.New("rpc executor cannot be nil")
	}
	err := executor.Do(ctx, meta, func(opCtx context.Context) error {
		var opErr error
		result, opErr = operation(opCtx)
		return opErr
	})
	return result, err
}

// Metrics returns the metrics collector attached to the executor.
func (e *RPCExecutor) Metrics() RPCMetrics {
	if e != nil && e.metrics != nil {
		return e.metrics
	}
	return NoopRPCMetrics{}
}
