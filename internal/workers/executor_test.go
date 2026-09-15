package workers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestPermit_LifecycleAndDoubleUse(t *testing.T) {
	var released atomic.Bool
	permit := NewPermit("general", 1, 100, "task-1", 1, func() {
		released.Store(true)
	})

	if permit.IsUsed() {
		t.Errorf("permit should not be used initially")
	}

	if err := permit.Use(); err != nil {
		t.Fatalf("unexpected error using permit: %v", err)
	}

	if !permit.IsUsed() {
		t.Errorf("permit should be marked used")
	}

	// Double use must fail
	if err := permit.Use(); !errors.Is(err, ErrPermitAlreadyUsed) {
		t.Errorf("expected ErrPermitAlreadyUsed, got: %v", err)
	}

	permit.Release()
	if !released.Load() {
		t.Errorf("expected release callback to fire")
	}

	// Double release is idempotent
	permit.Release()
}

func TestExecuteAssignment_Success(t *testing.T) {
	var released atomic.Bool
	permit := NewPermit("general", 1, 100, "task-1", 1, func() {
		released.Store(true)
	})

	executed := false
	spec := tasks.WorkSpec{
		ID:         "task-1",
		QuotaOwner: "user-1",
		Pool:       "general",
		Handler: func(ctx context.Context) error {
			executed = true
			return nil
		},
	}

	res := ExecuteAssignment(Assignment{
		Spec:   spec,
		Permit: permit,
		Ctx:    context.Background(),
	})

	if !executed {
		t.Errorf("handler was not executed")
	}
	if !res.IsSuccess() {
		t.Errorf("expected IsSuccess() == true, got outcome %s", res.Outcome)
	}
	if res.Cause != tasks.CauseNone {
		t.Errorf("expected CauseNone, got %s", res.Cause)
	}
	if !released.Load() {
		t.Errorf("expected permit to be released after execution")
	}
}

func TestExecuteAssignment_PanicRecovery(t *testing.T) {
	var released atomic.Bool
	permit := NewPermit("general", 1, 100, "task-panic", 1, func() {
		released.Store(true)
	})

	spec := tasks.WorkSpec{
		ID:         "task-panic",
		QuotaOwner: "user-1",
		Pool:       "general",
		Handler: func(ctx context.Context) error {
			panic("something went horribly wrong")
		},
	}

	res := ExecuteAssignment(Assignment{
		Spec:   spec,
		Permit: permit,
		Ctx:    context.Background(),
	})

	if res.Outcome != tasks.OutcomePanic {
		t.Errorf("expected OutcomePanic, got %s", res.Outcome)
	}
	if res.Cause != tasks.CausePanic {
		t.Errorf("expected CausePanic, got %s", res.Cause)
	}
	if !released.Load() {
		t.Errorf("expected permit to be released even after panic")
	}
}

func TestExecuteAssignment_Timeout(t *testing.T) {
	var released atomic.Bool
	permit := NewPermit("general", 1, 100, "task-timeout", 1, func() {
		released.Store(true)
	})

	spec := tasks.WorkSpec{
		ID:               "task-timeout",
		QuotaOwner:       "user-1",
		Pool:             "general",
		ExecutionTimeout: 10 * time.Millisecond,
		Handler: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}

	res := ExecuteAssignment(Assignment{
		Spec:   spec,
		Permit: permit,
		Ctx:    context.Background(),
	})

	if res.Outcome != tasks.OutcomeTimedOut {
		t.Errorf("expected OutcomeTimedOut, got %s", res.Outcome)
	}
	if res.Cause != tasks.CauseTimeout {
		t.Errorf("expected CauseTimeout, got %s", res.Cause)
	}
	if !released.Load() {
		t.Errorf("expected permit to be released after timeout")
	}
}

func TestExecuteAssignmentRejectsInvalidPermitsAndUnresolvedHandler(t *testing.T) {
	for _, scenario := range []string{"released", "wrong-pool", "wrong-task", "nil-handler", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			p := NewPermit("general", 0, 1, "task", 1, nil)
			spec := tasks.WorkSpec{ID: "task", Pool: "general", Handler: func(context.Context) error { t.Fatal("invalid assignment executed"); return nil }}
			switch scenario {
			case "released":
				p.Release()
			case "wrong-pool":
				spec.Pool = "other"
			case "wrong-task":
				spec.ID = "other"
			case "nil-handler":
				spec.Handler = nil
				spec.HandlerRef = "unresolved"
			case "expired":
				spec.QueueDeadline = time.Now().Add(-time.Second)
			}
			result := ExecuteAssignment(Assignment{Spec: spec, Permit: p})
			if result.IsSuccess() || !result.StartedAt.IsZero() {
				t.Fatalf("invalid pre-start result: %+v", result)
			}
		})
	}
}
