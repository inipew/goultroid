package telegram

import (
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/execution"
)

func TestRPCFailureExecutionSemantics(t *testing.T) {
	tests := []struct {
		name        string
		failure     *RPCFailure
		disposition execution.Disposition
		retryAfter  time.Duration
	}{
		{
			name:        "transient",
			failure:     &RPCFailure{Method: "x", Class: RPCTransient, Err: errors.New("temporary")},
			disposition: execution.DispositionRetryable,
		},
		{
			name:        "flood wait",
			failure:     &RPCFailure{Method: "x", Class: RPCFloodWait, RetryAfter: 4 * time.Second, Err: errors.New("flood")},
			disposition: execution.DispositionRetryable,
			retryAfter:  4 * time.Second,
		},
		{
			name:        "permission",
			failure:     &RPCFailure{Method: "x", Class: RPCPermission, Err: errors.New("denied")},
			disposition: execution.DispositionPermanent,
		},
		{
			name:        "ambiguous non idempotent",
			failure:     &RPCFailure{Method: "x", Class: RPCTransient, Ambiguous: true, Err: errors.New("unknown effect")},
			disposition: execution.DispositionPermanent,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.failure.ExecutionSemantics()
			if got.Disposition != tc.disposition || got.RetryAfter != tc.retryAfter {
				t.Fatalf("semantics=%+v, want disposition=%q retry_after=%v", got, tc.disposition, tc.retryAfter)
			}
		})
	}
}
