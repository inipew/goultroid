package telegram

import (
	"context"
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
		{
			name:        "cancellation during transient",
			failure:     &RPCFailure{Method: "x", Class: RPCTransient, Err: context.Canceled},
			disposition: execution.DispositionCancelled,
		},
		{
			name:        "cancellation during flood wait",
			failure:     &RPCFailure{Method: "x", Class: RPCFloodWait, RetryAfter: 10 * time.Second, Err: context.Canceled},
			disposition: execution.DispositionCancelled,
		},
		{
			name:        "cancellation during ambiguous stays permanent",
			failure:     &RPCFailure{Method: "x", Class: RPCTransient, Ambiguous: true, Err: context.Canceled},
			disposition: execution.DispositionPermanent,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.failure.ExecutionSemantics()
			if got.Disposition != tc.disposition || got.RetryAfter != tc.retryAfter {
				t.Fatalf("semantics=%+v, want disposition=%q retry_after=%v", got, tc.disposition, tc.retryAfter)
			}
			// Verify that execution.ExplicitSemantics also extracts the correct disposition
			if explicit, ok := execution.ExplicitSemantics(tc.failure); !ok || explicit.Disposition != tc.disposition {
				t.Fatalf("ExplicitSemantics=%+v, want disposition=%q", explicit, tc.disposition)
			}
		})
	}
}
