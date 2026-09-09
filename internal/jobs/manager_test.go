package jobs

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/tasks"
)

type mockSubmitter struct {
	submittedTasks []tasks.Task
}

func (m *mockSubmitter) Submit(ctx context.Context, poolName string, task tasks.Task) error {
	m.submittedTasks = append(m.submittedTasks, task)
	return nil
}

func TestJobManager_RegisterAndTrigger(t *testing.T) {
	submitter := &mockSubmitter{}
	mgr := NewManager(submitter)

	job := Job{
		ID:       "job-1",
		Owner:    "plugin:reminder",
		Type:     "reminder",
		Schedule: "@every 1h",
		Run: func(ctx context.Context) error {
			return nil
		},
	}

	if err := mgr.Register(job); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	ctx := context.Background()
	if err := mgr.Trigger(ctx, "job-1"); err != nil {
		t.Fatalf("trigger failed: %v", err)
	}

	if len(submitter.submittedTasks) != 1 {
		t.Fatalf("expected 1 submitted task, got %d", len(submitter.submittedTasks))
	}

	submitted := submitter.submittedTasks[0]
	if submitted.Owner != "plugin:reminder" || submitted.Name != "job:job-1" {
		t.Errorf("unexpected submitted task: %+v", submitted)
	}
}

func TestJobManager_CancelByOwner(t *testing.T) {
	mgr := NewManager(&mockSubmitter{})

	_ = mgr.Register(Job{
		ID:    "j1",
		Owner: "plugin:reminder",
		Run:   func(ctx context.Context) error { return nil },
	})
	_ = mgr.Register(Job{
		ID:    "j2",
		Owner: "plugin:reminder",
		Run:   func(ctx context.Context) error { return nil },
	})
	_ = mgr.Register(Job{
		ID:    "j3",
		Owner: "plugin:other",
		Run:   func(ctx context.Context) error { return nil },
	})

	cancelled := mgr.CancelByOwner("plugin:reminder")
	if cancelled != 2 {
		t.Errorf("expected 2 cancelled jobs, got %d", cancelled)
	}

	remaining := mgr.All()
	if len(remaining) != 1 || remaining[0].ID != "j3" {
		t.Errorf("expected only j3 remaining, got: %+v", remaining)
	}
}
