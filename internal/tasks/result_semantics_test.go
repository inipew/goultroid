package tasks

import (
	"testing"

	"github.com/inipew/goultroid/internal/execution"
)

func TestTaskResultSemanticRetryPolicy(t *testing.T) {
	tests := []struct {
		name        string
		result      TaskResult
		disposition execution.Disposition
		retry       bool
	}{
		{"success", TaskResult{Outcome: OutcomeCompleted, Disposition: execution.DispositionSuccess}, execution.DispositionSuccess, false},
		{"handled", TaskResult{Outcome: OutcomeCompleted, Disposition: execution.DispositionHandled}, execution.DispositionHandled, false},
		{"rejected", TaskResult{Outcome: OutcomeFailed, Disposition: execution.DispositionRejected}, execution.DispositionRejected, false},
		{"permanent", TaskResult{Outcome: OutcomeFailed, Disposition: execution.DispositionPermanent}, execution.DispositionPermanent, false},
		{"cancelled", TaskResult{Outcome: OutcomeCancelled, Disposition: execution.DispositionCancelled}, execution.DispositionCancelled, false},
		{"retryable", TaskResult{Outcome: OutcomeFailed, Disposition: execution.DispositionRetryable}, execution.DispositionRetryable, true},
		{"internal", TaskResult{Outcome: OutcomeFailed, Disposition: execution.DispositionInternal}, execution.DispositionInternal, true},
		{"legacy failed", TaskResult{Outcome: OutcomeFailed}, execution.DispositionInternal, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			semantics := tc.result.Semantics()
			if semantics.Disposition != tc.disposition {
				t.Fatalf("disposition=%q, want %q", semantics.Disposition, tc.disposition)
			}
			if got := tc.result.ShouldRetry(); got != tc.retry {
				t.Fatalf("ShouldRetry=%v, want %v", got, tc.retry)
			}
		})
	}
}

func TestTaskResultSemanticsCarriesCodeAndRetryAfter(t *testing.T) {
	result := TaskResult{
		Outcome:     OutcomeFailed,
		Disposition: execution.DispositionRetryable,
		RetryAfter:  42,
		Failure:     FailureInfo{Code: "rpc_flood_wait"},
	}
	semantics := result.Semantics()
	if semantics.Code != "rpc_flood_wait" || semantics.RetryAfter != 42 {
		t.Fatalf("semantics=%+v", semantics)
	}
}
