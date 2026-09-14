package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/tasks"
)

type asyncTestSubmitter struct{}

func (asyncTestSubmitter) Submit(ctx context.Context, _ string, task tasks.Task) error {
	go func() { _ = task.Execute(ctx) }()
	return nil
}

func TestTriggerAndWaitReturnsConcreteExecutionResult(t *testing.T) {
	want := errors.New("execution failed")
	m := NewManager(asyncTestSubmitter{})
	if err := m.Register(Job{
		ID:    "managed-1",
		Owner: "test",
		Run: func(context.Context) error {
			return want
		},
	}); err != nil {
		t.Fatal(err)
	}

	err := m.TriggerAndWait(context.Background(), "managed-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected concrete run error %v, got %v", want, err)
	}
}
