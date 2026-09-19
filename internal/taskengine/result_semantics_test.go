package taskengine

import (
	"context"
	"errors"
	"testing"

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
