package execution

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type retryWaitErr struct{ wait time.Duration }

func (e retryWaitErr) Error() string                { return "wait" }
func (e retryWaitErr) RateLimitWait() time.Duration { return e.wait }

func TestSemanticErrorPreservesUnwrap(t *testing.T) {
	base := errors.New("denied")
	err := WithSemantics(base, Semantics{Disposition: DispositionRejected, Code: "permission_denied"})
	if !errors.Is(err, base) {
		t.Fatal("semantic wrapper broke errors.Is")
	}
	got := SemanticsOf(fmt.Errorf("outer: %w", err))
	if got.Disposition != DispositionRejected || got.Code != "permission_denied" {
		t.Fatalf("semantics=%+v", got)
	}
	if got.ShouldRetry() {
		t.Fatal("rejected result must not retry")
	}
}

func TestSemanticsOfContextAndRateLimit(t *testing.T) {
	if got := SemanticsOf(context.Canceled); got.Disposition != DispositionCancelled {
		t.Fatalf("cancel semantics=%+v", got)
	}
	if got := SemanticsOf(context.DeadlineExceeded); got.Disposition != DispositionRetryable {
		t.Fatalf("deadline semantics=%+v", got)
	}
	if got := SemanticsOf(retryWaitErr{wait: 3 * time.Second}); got.Disposition != DispositionRetryable || got.RetryAfter != 3*time.Second {
		t.Fatalf("rate semantics=%+v", got)
	}
}

func TestInternalRetainsBoundedRetryCompatibility(t *testing.T) {
	got := SemanticsOf(errors.New("legacy failure"))
	if got.Disposition != DispositionInternal || !got.ShouldRetry() {
		t.Fatalf("legacy semantics=%+v", got)
	}
}

func TestWithSemanticsOverridesImplicitContextClassification(t *testing.T) {
	err := WithSemantics(context.DeadlineExceeded, Semantics{
		Disposition: DispositionPermanent,
		Code:        "deadline_is_terminal_here",
	})
	got := SemanticsOf(err)
	if got.Disposition != DispositionPermanent || got.Code != "deadline_is_terminal_here" {
		t.Fatalf("semantics=%+v", got)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("override broke error unwrap identity")
	}
}
