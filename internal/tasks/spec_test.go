package tasks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkSpec_Validate(t *testing.T) {
	tests := []struct {
		name    string
		spec    WorkSpec
		wantErr bool
	}{
		{
			name: "valid minimal spec",
			spec: WorkSpec{
				ID:         "task-1",
				QuotaOwner: "user-1",
				Pool:       "general",
				Handler:    func(ctx context.Context) error { return nil },
			},
			wantErr: false,
		},
		{
			name: "missing id",
			spec: WorkSpec{
				QuotaOwner: "user-1",
				Pool:       "general",
				Handler:    func(ctx context.Context) error { return nil },
			},
			wantErr: true,
		},
		{
			name: "missing quota owner",
			spec: WorkSpec{
				ID:      "task-1",
				Pool:    "general",
				Handler: func(ctx context.Context) error { return nil },
			},
			wantErr: true,
		},
		{
			name: "missing pool",
			spec: WorkSpec{
				ID:         "task-1",
				QuotaOwner: "user-1",
				Handler:    func(ctx context.Context) error { return nil },
			},
			wantErr: true,
		},
		{
			name: "valid with handler ref",
			spec: WorkSpec{
				ID:         "task-1",
				QuotaOwner: "user-1",
				Pool:       "general",
				HandlerRef: "cmd.echo",
			},
			wantErr: false,
		},
		{
			name: "missing both handler and handler ref",
			spec: WorkSpec{
				ID:         "task-1",
				QuotaOwner: "user-1",
				Pool:       "general",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("WorkSpec.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTaskResult_DurationAndSuccess(t *testing.T) {
	start := time.Now()
	finish := start.Add(50 * time.Millisecond)

	res := TaskResult{
		TaskID:     "task-1",
		Outcome:    OutcomeCompleted,
		Cause:      CauseNone,
		StartedAt:  start,
		FinishedAt: finish,
	}

	if !res.IsSuccess() {
		t.Errorf("expected IsSuccess() == true for OutcomeCompleted")
	}
	if res.ExecutionDuration() != 50*time.Millisecond {
		t.Errorf("expected duration 50ms, got %v", res.ExecutionDuration())
	}

	failedRes := TaskResult{
		TaskID:  "task-2",
		Outcome: OutcomeFailed,
		Cause:   CauseTimeout,
	}
	if failedRes.IsSuccess() {
		t.Errorf("expected IsSuccess() == false for OutcomeFailed")
	}
}

func TestAdmissionError_Unwrap(t *testing.T) {
	base := errors.New("underlying issue")
	admErr := NewAdmissionError(ReasonOwnerQueueFull, base)

	if !errors.Is(admErr, base) {
		t.Errorf("expected errors.Is(admErr, base) == true")
	}
	if admErr.Reason != ReasonOwnerQueueFull {
		t.Errorf("expected reason %s, got %s", ReasonOwnerQueueFull, admErr.Reason)
	}
}
