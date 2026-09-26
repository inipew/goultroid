package downloader

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

type deferredDeliveryTaskClient struct {
	tasks.Client
	specs []tasks.WorkSpec
}

func (c *deferredDeliveryTaskClient) Submit(_ context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.specs = append(c.specs, spec)
	return nil, nil
}

func TestYTZTerminalEditIsDeferredOutOfCompletionCallback(t *testing.T) {
	client := &deferredDeliveryTaskClient{}
	p := New(client)
	calls := 0

	if err := p.submitTerminalEdit(context.Background(), "delivered", func(context.Context) error {
		calls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("terminal edit executed synchronously during Submit: calls=%d", calls)
	}
	if len(client.specs) != 1 {
		t.Fatalf("terminal edit tasks=%d, want 1", len(client.specs))
	}
	spec := client.specs[0]
	if spec.Pool != tasks.PoolID("general") || spec.Class != tasks.PriorityNormal || spec.ExecutionTimeout != downloaderStatusTimeout {
		t.Fatalf("terminal edit task pool/class/timeout=%q/%q/%v", spec.Pool, spec.Class, spec.ExecutionTimeout)
	}
	if len(spec.Resources) != 0 {
		t.Fatalf("terminal edit resources=%+v, want none", spec.Resources)
	}
	if err := spec.Handler(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("terminal edit handler calls=%d, want 1", calls)
	}
}

func TestYTZDeliveryCompletionEnqueuesTerminalEditInsteadOfRunningItInline(t *testing.T) {
	client := &deferredDeliveryTaskClient{}
	p := New(client)
	store := storage.NewMemoryStorage()
	asset := &storage.Asset{ID: "retained", Name: "sample.mp4", MIME: "video/mp4"}

	deliveredCalls := 0
	if err := p.submitRetainedDelivery(
		context.Background(),
		tasks.TaskID("test:delivery"),
		store,
		asset,
		download.MediaModeVideo,
		download.MediaFormatMP4,
		func(context.Context, presentation.Media) error { return nil },
		nil,
		func(context.Context) error {
			deliveredCalls++
			return nil
		},
		nil,
		"message",
	); err != nil {
		t.Fatal(err)
	}
	if len(client.specs) != 1 {
		t.Fatalf("initial tasks=%d, want media delivery only", len(client.specs))
	}

	mediaSpec := client.specs[0]
	mediaSpec.OnComplete(tasks.TaskResult{
		TaskID:  mediaSpec.ID,
		Outcome: tasks.OutcomeCompleted,
	})

	if deliveredCalls != 0 {
		t.Fatalf("delivered edit ran inside media OnComplete: calls=%d", deliveredCalls)
	}
	if len(client.specs) != 2 {
		t.Fatalf("tasks after media completion=%d, want queued terminal edit", len(client.specs))
	}
	statusSpec := client.specs[1]
	if len(statusSpec.Resources) != 0 {
		t.Fatalf("status edit resources=%+v, want none", statusSpec.Resources)
	}
	if err := statusSpec.Handler(context.Background()); err != nil {
		t.Fatal(err)
	}
	if deliveredCalls != 1 {
		t.Fatalf("delivered edit calls=%d, want 1 after status task runs", deliveredCalls)
	}
}

func TestYTZDeliveryFailureEnqueuesTerminalEditInsteadOfRunningItInline(t *testing.T) {
	client := &deferredDeliveryTaskClient{}
	p := New(client)
	store := storage.NewMemoryStorage()
	asset := &storage.Asset{ID: "retained", Name: "sample.mp4", MIME: "video/mp4"}

	failureCalls := 0
	if err := p.submitRetainedDelivery(
		context.Background(),
		tasks.TaskID("test:delivery"),
		store,
		asset,
		download.MediaModeVideo,
		download.MediaFormatMP4,
		func(context.Context, presentation.Media) error { return nil },
		func(context.Context) error {
			failureCalls++
			return nil
		},
		nil,
		nil,
		"message",
	); err != nil {
		t.Fatal(err)
	}

	mediaSpec := client.specs[0]
	mediaSpec.OnComplete(tasks.TaskResult{
		TaskID:  mediaSpec.ID,
		Outcome: tasks.OutcomeFailed,
		Failure: tasks.FailureInfo{Message: "telegram unavailable"},
	})

	if failureCalls != 0 {
		t.Fatalf("failure edit ran inside media OnComplete: calls=%d", failureCalls)
	}
	if len(client.specs) != 2 {
		t.Fatalf("tasks after media failure=%d, want queued terminal edit", len(client.specs))
	}
	if err := client.specs[1].Handler(context.Background()); err != nil {
		t.Fatal(err)
	}
	if failureCalls != 1 {
		t.Fatalf("failure edit calls=%d, want 1 after status task runs", failureCalls)
	}
}
