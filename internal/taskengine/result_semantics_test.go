package taskengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestEnginePropagatesTypedExecutionSemantics(t *testing.T) {
	tests := []struct {
		name        string
		semantics   execution.Semantics
		disposition execution.Disposition
		retry       bool
	}{
		{
			name:        "rejected",
			semantics:   execution.Semantics{Disposition: execution.DispositionRejected, Code: "invalid_input"},
			disposition: execution.DispositionRejected,
			retry:       false,
		},
		{
			name:        "permanent",
			semantics:   execution.Semantics{Disposition: execution.DispositionPermanent, Code: "bad_payload"},
			disposition: execution.DispositionPermanent,
			retry:       false,
		},
		{
			name:        "retryable",
			semantics:   execution.Semantics{Disposition: execution.DispositionRetryable, Code: "temporary"},
			disposition: execution.DispositionRetryable,
			retry:       true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := auditEngine(t)
			ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
				ID:         tasks.TaskID("typed-" + tc.name),
				Pool:       "a",
				QuotaOwner: "typed-owner",
				Handler: func(context.Context) error {
					return execution.WithSemantics(errors.New("typed failure"), tc.semantics)
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			res := waitAuditTicket(t, ticket)
			if res.Outcome != tasks.OutcomeFailed {
				t.Fatalf("outcome=%s, want failed", res.Outcome)
			}
			if res.Disposition != tc.disposition {
				t.Fatalf("disposition=%q, want %q", res.Disposition, tc.disposition)
			}
			if res.Failure.Code != tc.semantics.Code {
				t.Fatalf("failure code=%q, want %q", res.Failure.Code, tc.semantics.Code)
			}
			if got := res.ShouldRetry(); got != tc.retry {
				t.Fatalf("ShouldRetry=%v, want %v", got, tc.retry)
			}
		})
	}
}

func TestEngineCancellationResultIsTypedCancelled(t *testing.T) {
	e := auditEngine(t)
	blockerStarted := make(chan struct{})
	blockerRelease := make(chan struct{})
	defer close(blockerRelease)

	if _, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID: "typed-cancel-blocker", Pool: "a", QuotaOwner: "blocker",
		Handler: func(context.Context) error {
			close(blockerStarted)
			<-blockerRelease
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	<-blockerStarted

	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID: "typed-cancelled", Pool: "a", QuotaOwner: "queued",
		Handler: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Cancel("typed-cancelled", tasks.CauseUserCancel); err != nil {
		t.Fatal(err)
	}
	res := waitAuditTicket(t, ticket)
	if res.Disposition != execution.DispositionCancelled || res.ShouldRetry() {
		t.Fatalf("cancel semantics=%+v", res)
	}
}

type mockRateLimitErr struct {
	wait time.Duration
	err  error
}

func (m mockRateLimitErr) Error() string                { return m.err.Error() }
func (m mockRateLimitErr) Unwrap() error                { return m.err }
func (m mockRateLimitErr) RateLimitWait() time.Duration { return m.wait }

func TestEngine_CancellationPrecedesRateLimit(t *testing.T) {
	e := auditEngine(t)
	rateAndCancelErr := mockRateLimitErr{
		wait: 10 * time.Second,
		err:  context.Canceled,
	}

	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "rate-and-cancel",
		Pool:       "a",
		QuotaOwner: "test-owner",
		Handler: func(context.Context) error {
			return rateAndCancelErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := waitAuditTicket(t, ticket)
	if res.Outcome != tasks.OutcomeCancelled {
		t.Fatalf("outcome=%s, want cancelled", res.Outcome)
	}
	if res.Cause != tasks.CauseUserCancel {
		t.Fatalf("cause=%s, want user_cancel (must not be rate_limited)", res.Cause)
	}
	if res.Disposition != execution.DispositionCancelled {
		t.Fatalf("disposition=%s, want cancelled", res.Disposition)
	}
}

func TestEngine_SemanticRetryAfterClassification(t *testing.T) {
	e := auditEngine(t)

	// 1. Retryable outcome with RetryAfter must enter CauseRateLimited deferral path
	retryWait := 5 * time.Second
	retryErr := execution.WithSemantics(errors.New("defer please"), execution.Semantics{
		Disposition: execution.DispositionRetryable,
		RetryAfter:  retryWait,
		Code:        "custom_rate_limit",
	})

	ticket1, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "semantic-retry-after",
		Pool:       "a",
		QuotaOwner: "test-owner",
		Handler: func(context.Context) error {
			return retryErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res1 := waitAuditTicket(t, ticket1)
	if res1.Outcome != tasks.OutcomeFailed {
		t.Fatalf("res1 outcome=%s, want failed", res1.Outcome)
	}
	if res1.Cause != tasks.CauseRateLimited {
		t.Fatalf("res1 cause=%s, want rate_limited", res1.Cause)
	}
	if res1.Disposition != execution.DispositionRetryable {
		t.Fatalf("res1 disposition=%s, want retryable", res1.Disposition)
	}
	if res1.RetryAfter != retryWait {
		t.Fatalf("res1 retryAfter=%v, want %v", res1.RetryAfter, retryWait)
	}

	// 2. Permanent outcome with RetryAfter metadata must NOT enter CauseRateLimited
	permWait := 24 * time.Hour
	permErr := execution.WithSemantics(errors.New("banned permanently"), execution.Semantics{
		Disposition: execution.DispositionPermanent,
		RetryAfter:  permWait,
		Code:        "perm_ban",
	})

	ticket2, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "semantic-perm-with-wait",
		Pool:       "a",
		QuotaOwner: "test-owner",
		Handler: func(context.Context) error {
			return permErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res2 := waitAuditTicket(t, ticket2)
	if res2.Outcome != tasks.OutcomeFailed {
		t.Fatalf("res2 outcome=%s, want failed", res2.Outcome)
	}
	if res2.Cause == tasks.CauseRateLimited {
		t.Fatalf("res2 cause=%s, permanent error must NOT become rate_limited", res2.Cause)
	}
	if res2.Disposition != execution.DispositionPermanent {
		t.Fatalf("res2 disposition=%s, want permanent", res2.Disposition)
	}
	if res2.RetryAfter != permWait {
		t.Fatalf("res2 retryAfter=%v, want %v", res2.RetryAfter, permWait)
	}
}

func TestEngine_ExecutionShutdownVsUserCancel(t *testing.T) {
	// 1. User/caller cancellation while engine is running -> CauseUserCancel
	e1 := auditEngine(t)
	handlerStarted := make(chan struct{})
	ticket1, err := e1.Submit(context.Background(), tasks.WorkSpec{
		ID:         "caller-cancel-task",
		Pool:       "a",
		QuotaOwner: "test-owner",
		Handler: func(ctx context.Context) error {
			close(handlerStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-handlerStarted
	if _, err := e1.Cancel("caller-cancel-task", tasks.CauseUserCancel); err != nil {
		t.Fatal(err)
	}
	res1 := waitAuditTicket(t, ticket1)
	if res1.Outcome != tasks.OutcomeCancelled {
		t.Fatalf("res1 outcome=%s, want cancelled", res1.Outcome)
	}
	if res1.Cause != tasks.CauseUserCancel {
		t.Fatalf("res1 cause=%s, want user_cancel", res1.Cause)
	}

	// 2. Engine shutdown (ForceStop) while handler is running -> CauseShutdown
	e2 := auditEngine(t)
	shutdownHandlerStarted := make(chan struct{})
	ticket2, err := e2.Submit(context.Background(), tasks.WorkSpec{
		ID:         "shutdown-cancel-task",
		Pool:       "a",
		QuotaOwner: "test-owner",
		Handler: func(ctx context.Context) error {
			close(shutdownHandlerStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-shutdownHandlerStarted
	if err := e2.ForceStop(context.Background()); err != nil {
		t.Fatal(err)
	}
	res2 := waitAuditTicket(t, ticket2)
	if res2.Outcome != tasks.OutcomeCancelled {
		t.Fatalf("res2 outcome=%s, want cancelled", res2.Outcome)
	}
	if res2.Cause != tasks.CauseShutdown {
		t.Fatalf("res2 cause=%s, want shutdown (must not be user_cancel)", res2.Cause)
	}
}
