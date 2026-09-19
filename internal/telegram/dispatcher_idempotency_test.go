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
	"go.uber.org/zap"
)

func TestDispatcher_IdempotencyAndEventPublish(t *testing.T) {
	router := core.NewRouter(".")
	perms := core.NewPermissions(12345, nil)
	d := NewDispatcher(router, perms, nil, zap.NewNop())

	bus := core.NewEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = bus.Start(ctx)
	defer func() { _ = bus.Close() }()

	d.SetEventBus(bus)

	idemp := idempotency.NewManager(1 * time.Minute)
	defer idemp.Close()
	d.SetIdempotency(idemp)

	var eventsReceived atomic.Int32
	bus.Subscribe(core.EventTypeMessageCreated, func(evt core.Event) {
		eventsReceived.Add(1)
	})

	var interceptorCalls atomic.Int32
	d.AddMessageHandler(func(ctx context.Context, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) error {
		interceptorCalls.Add(1)
		return nil
	})

	msg := &tg.Message{
		ID:      42,
		PeerID:  &tg.PeerChat{ChatID: 999},
		Message: "hello world",
	}

	// First dispatch: should succeed, interceptor called, event published
	if err := d.dispatch(ctx, tg.Entities{}, msg); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}

	if interceptorCalls.Load() != 1 {
		t.Fatalf("expected 1 interceptor call, got %d", interceptorCalls.Load())
	}

	// Wait for async event bus delivery
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if eventsReceived.Load() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if eventsReceived.Load() != 1 {
		t.Fatalf("expected 1 event received, got %d", eventsReceived.Load())
	}

	// Second dispatch with same msg ID: should be dropped by idempotency manager
	if err := d.dispatch(ctx, tg.Entities{}, msg); err != nil {
		t.Fatalf("second dispatch: %v", err)
	}

	// Interceptor count and event count should NOT increase
	if interceptorCalls.Load() != 1 {
		t.Fatalf("expected duplicate message to be dropped, got %d interceptor calls", interceptorCalls.Load())
	}
}

type failingIdempotencyRepository struct {
	err error
}

func (r failingIdempotencyRepository) InitSchema(context.Context) error { return nil }
func (r failingIdempotencyRepository) Claim(context.Context, string, time.Time, time.Time) (bool, error) {
	return false, r.err
}
func (r failingIdempotencyRepository) IsProcessed(context.Context, string, time.Time) (bool, error) {
	return false, r.err
}
func (r failingIdempotencyRepository) DeleteExpired(context.Context, time.Time) (int, error) {
	return 0, r.err
}
func (r failingIdempotencyRepository) EarliestExpiry(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, r.err
}
func (r failingIdempotencyRepository) Size(context.Context, time.Time) (int, error) {
	return 0, r.err
}

func TestDispatcher_IdempotencyFailureFailsClosedForCommand(t *testing.T) {
	router := core.NewRouter(".")
	var executed atomic.Bool
	if err := router.Register(core.Command{
		Name: "mutate",
		Handler: func(*core.Context) error {
			executed.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	d := NewDispatcher(router, core.NewPermissions(1, nil), nil, zap.NewNop())
	d.SetIdempotency(idempotency.NewManager(time.Minute, failingIdempotencyRepository{err: errors.New("db unavailable")}))

	msg := &tg.Message{ID: 7, Out: true, PeerID: &tg.PeerChat{ChatID: 10}, Message: ".mutate"}
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if executed.Load() {
		t.Fatal("command executed while idempotency claim was unavailable")
	}
}

func TestDispatcher_IdempotencyFailureFailsOpenForPlainMessage(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	d.SetIdempotency(idempotency.NewManager(time.Minute, failingIdempotencyRepository{err: errors.New("db unavailable")}))

	var called atomic.Bool
	d.AddMessageHandler(func(context.Context, tg.Entities, *tg.Message, bool, string) error {
		called.Store(true)
		return nil
	})

	msg := &tg.Message{ID: 8, PeerID: &tg.PeerChat{ChatID: 10}, Message: "hello"}
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !called.Load() {
		t.Fatal("plain message hook was blocked by idempotency backend failure")
	}
}

func TestDispatcher_CallbackIdempotencyFailureFailsClosed(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	d.SetIdempotency(idempotency.NewManager(time.Minute, failingIdempotencyRepository{err: errors.New("db unavailable")}))
	svc := newCallbackRecordingService()
	d.SetService(svc)

	update := &tg.UpdateBotCallbackQuery{
		QueryID: 1234,
		UserID:  5,
		Peer:    &tg.PeerChat{ChatID: 10},
		MsgID:   20,
		Data:    []byte("v1:test:act:-"),
	}
	if err := d.OnBotCallbackQuery(context.Background(), tg.Entities{}, update); err != nil {
		t.Fatalf("callback dispatch: %v", err)
	}
	if svc.callCount.Load() != 1 {
		t.Fatalf("expected one availability answer, got %d", svc.callCount.Load())
	}
	svc.mu.Lock()
	answer := svc.answered[update.QueryID]
	svc.mu.Unlock()
	if answer != "Interaction service temporarily unavailable." {
		t.Fatalf("unexpected callback answer %q", answer)
	}
}
