package telegram

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/idempotency"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type retryAdmissionTaskClient struct {
	reject      atomic.Bool
	submissions atomic.Int32
}

func (c *retryAdmissionTaskClient) Submit(_ context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.submissions.Add(1)
	if c.reject.Load() {
		return nil, errors.New("admission rejected for test")
	}
	if spec.OnComplete != nil {
		spec.OnComplete(tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted})
	}
	return nil, nil
}

func (*retryAdmissionTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*retryAdmissionTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*retryAdmissionTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func newRetryCommandDispatcher(t *testing.T, client tasks.Client) (*Dispatcher, *idempotency.Manager) {
	t.Helper()
	router := core.NewRouter(".")
	if err := router.Register(core.Command{
		Name:       "retry",
		Permission: core.PermissionEveryone,
		Invocation: core.InvocationPolicy{Userbot: core.InvocationAnyone},
		Handler:    func(*core.Context) error { return nil },
	}); err != nil {
		t.Fatalf("register retry command: %v", err)
	}

	d := NewDispatcher(router, core.NewPermissions(100, nil), nil, zap.NewNop())
	d.SetSelfID(100)
	d.SetTasks(client)
	mgr := idempotency.NewManager(time.Minute)
	d.SetIdempotency(mgr)
	return d, mgr
}

func TestDispatcherReleasesCommandClaimsWhenTaskAdmissionFails(t *testing.T) {
	client := &retryAdmissionTaskClient{}
	client.reject.Store(true)
	d, mgr := newRetryCommandDispatcher(t, client)

	ctx := context.Background()
	msg := &tg.Message{
		ID:      91,
		PeerID:  &tg.PeerChat{ChatID: 10},
		FromID:  &tg.PeerUser{UserID: 200},
		Message: ".retry",
	}
	if err := d.dispatch(ctx, tg.Entities{}, msg); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if got := client.submissions.Load(); got != 1 {
		t.Fatalf("first submissions=%d, want 1", got)
	}
	if processed, err := mgr.IsProcessedContext(ctx, "msg:chat:10:91"); err != nil || processed {
		t.Fatalf("rejected command remained durably claimed: processed=%v err=%v", processed, err)
	}

	client.reject.Store(false)
	if err := d.dispatch(ctx, tg.Entities{}, msg); err != nil {
		t.Fatalf("retry dispatch: %v", err)
	}
	if got := client.submissions.Load(); got != 2 {
		t.Fatalf("retry submissions=%d, want 2", got)
	}
	if processed, err := mgr.IsProcessedContext(ctx, "msg:chat:10:91"); err != nil || !processed {
		t.Fatalf("accepted command was not committed: processed=%v err=%v", processed, err)
	}

	if err := d.dispatch(ctx, tg.Entities{}, msg); err != nil {
		t.Fatalf("accepted duplicate dispatch: %v", err)
	}
	if got := client.submissions.Load(); got != 2 {
		t.Fatalf("accepted duplicate reached TaskEngine: submissions=%d", got)
	}
}

func TestDispatcherReleasesCommandClaimsWhenPeerResolutionFails(t *testing.T) {
	client := &retryAdmissionTaskClient{}
	d, mgr := newRetryCommandDispatcher(t, client)

	ctx := context.Background()
	msg := &tg.Message{
		ID:      92,
		PeerID:  &tg.PeerUser{UserID: 200},
		FromID:  &tg.PeerUser{UserID: 200},
		Message: ".retry",
	}
	if err := d.dispatch(ctx, tg.Entities{}, msg); err != nil {
		t.Fatalf("unresolved dispatch: %v", err)
	}
	if got := client.submissions.Load(); got != 0 {
		t.Fatalf("unresolved peer reached TaskEngine: submissions=%d", got)
	}
	if processed, err := mgr.IsProcessedContext(ctx, "msg:user:200:92"); err != nil || processed {
		t.Fatalf("unresolved command remained durably claimed: processed=%v err=%v", processed, err)
	}

	entities := tg.Entities{Users: map[int64]*tg.User{
		200: {ID: 200, AccessHash: 12345, FirstName: "Retry"},
	}}
	if err := d.dispatch(ctx, entities, msg); err != nil {
		t.Fatalf("resolved retry dispatch: %v", err)
	}
	if got := client.submissions.Load(); got != 1 {
		t.Fatalf("resolved retry submissions=%d, want 1", got)
	}
	if processed, err := mgr.IsProcessedContext(ctx, "msg:user:200:92"); err != nil || !processed {
		t.Fatalf("resolved command was not committed: processed=%v err=%v", processed, err)
	}
}
