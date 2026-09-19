package telegram

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/idempotency"
	"go.uber.org/zap"
)

func TestDispatcher_InvocationDeniedBeforeDurableAndTaskAdmission(t *testing.T) {
	router := core.NewRouter(".")
	var executed atomic.Bool
	if err := router.Register(core.Command{
		Name:       "ping",
		Permission: core.PermissionEveryone,
		Handler: func(*core.Context) error {
			executed.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	repo := &countingIdempotencyRepository{}
	d := NewDispatcher(router, core.NewPermissions(100, nil), nil, zap.NewNop())
	d.SetIdempotency(idempotency.NewManager(time.Minute, repo))

	msg := &tg.Message{
		ID:      1,
		PeerID:  &tg.PeerChat{ChatID: 10},
		FromID:  &tg.PeerUser{UserID: 300},
		Message: ".ping",
	}
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if executed.Load() {
		t.Fatal("invocation-denied command executed")
	}
	if got := repo.claims.Load(); got != 0 {
		t.Fatalf("invocation-denied command performed %d durable claims, want 0", got)
	}
}

func TestDispatcher_DefaultSudoCommandAllowsSudoInvocation(t *testing.T) {
	router := core.NewRouter(".")
	executed := make(chan struct{}, 1)
	if err := router.Register(core.Command{
		Name:       "ban",
		Permission: core.PermissionSudo,
		Handler: func(*core.Context) error {
			executed <- struct{}{}
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	d := NewDispatcher(router, core.NewPermissions(100, []int64{200}), nil, zap.NewNop())
	configureDispatcherTasks(t, d)
	msg := &tg.Message{
		ID:      2,
		PeerID:  &tg.PeerChat{ChatID: 10},
		FromID:  &tg.PeerUser{UserID: 200},
		Message: ".ban",
	}
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	select {
	case <-executed:
	case <-time.After(time.Second):
		t.Fatal("sudo command was not executed")
	}
}

func TestDispatcher_OutgoingOwnerBypassesSelfLookup(t *testing.T) {
	router := core.NewRouter(".")
	executed := make(chan struct{}, 1)
	if err := router.Register(core.Command{
		Name:       "ping",
		Permission: core.PermissionEveryone,
		Handler: func(*core.Context) error {
			executed <- struct{}{}
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	d := NewDispatcher(router, nil, nil, zap.NewNop())
	configureDispatcherTasks(t, d)
	msg := &tg.Message{
		ID:      3,
		Out:     true,
		PeerID:  &tg.PeerChat{ChatID: 10},
		Message: ".ping",
	}
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	select {
	case <-executed:
	case <-time.After(time.Second):
		t.Fatal("outgoing owner command was not executed")
	}
}
