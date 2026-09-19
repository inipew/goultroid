package scheduler

import (
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestSchedulerSubmissionFailurePolicy(t *testing.T) {
	permanentErr := execution.WithSemantics(
		errors.New("missing definition"),
		execution.Semantics{Disposition: execution.DispositionPermanent, Code: "missing"},
	)
	if delay, permanent := schedulerSubmissionFailurePolicy(permanentErr); !permanent || delay != time.Second {
		t.Fatalf("permanent policy delay=%v permanent=%v", delay, permanent)
	}

	retryErr := execution.WithSemantics(
		errors.New("wait"),
		execution.Semantics{Disposition: execution.DispositionRetryable, Code: "busy", RetryAfter: 3 * time.Second},
	)
	if delay, permanent := schedulerSubmissionFailurePolicy(retryErr); permanent || delay != 3*time.Second {
		t.Fatalf("retry policy delay=%v permanent=%v", delay, permanent)
	}

	admissionErr := tasks.NewAdmissionError(tasks.ReasonOwnerQueueFull, tasks.ErrOwnerQueueFull)
	if delay, permanent := schedulerSubmissionFailurePolicy(admissionErr); permanent || delay != time.Second {
		t.Fatalf("admission policy delay=%v permanent=%v", delay, permanent)
	}

	cancelledErr := execution.WithSemantics(
		errors.New("shutdown"),
		execution.Semantics{Disposition: execution.DispositionCancelled, Code: "shutdown"},
	)
	if delay, permanent := schedulerSubmissionFailurePolicy(cancelledErr); permanent || delay != time.Second {
		t.Fatalf("cancelled policy delay=%v permanent=%v", delay, permanent)
	}
}
