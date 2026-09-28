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

type callbackNativeStub struct {
	handled bool
	err     error
	calls   atomic.Int32
}

func (s *callbackNativeStub) HandleCallback(context.Context, *core.CallbackQueryEvent) (bool, error) {
	s.calls.Add(1)
	return s.handled, s.err
}

func newCallbackClaimDispatcher(native NativeInteractionDispatcher) (*Dispatcher, *idempotency.Manager, *callbackRecordingService) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	mgr := idempotency.NewManager(time.Minute)
	d.SetIdempotency(mgr)
	d.SetNativeInteractions(native)
	svc := newCallbackRecordingService()
	d.SetService(svc)
	return d, mgr, svc
}

func TestDispatcherCallbackClaimReleasedOnPreAdmissionFailure(t *testing.T) {
	native := &callbackNativeStub{handled: true, err: errors.New("pre-admission failure")}
	d, mgr, _ := newCallbackClaimDispatcher(native)
	ctx := context.Background()
	update := &tg.UpdateBotCallbackQuery{
		QueryID: 2001,
		UserID:  50,
		Peer:    &tg.PeerChat{ChatID: 10},
		MsgID:   20,
		Data:    []byte("a2:test:invalid.1"),
	}

	if err := d.OnBotCallbackQuery(ctx, tg.Entities{}, update); err != nil {
		t.Fatalf("first callback: %v", err)
	}
	processed, err := mgr.IsProcessedContext(ctx, "cb:2001")
	if err != nil {
		t.Fatalf("claim lookup: %v", err)
	}
	if processed {
		t.Fatal("pre-admission failure left callback durably claimed")
	}

	if err := d.OnBotCallbackQuery(ctx, tg.Entities{}, update); err != nil {
		t.Fatalf("retry callback: %v", err)
	}
	if got := native.calls.Load(); got != 2 {
		t.Fatalf("native callback calls=%d, want retry to reach native ingress", got)
	}
}

func TestDispatcherInlineCallbackClaimReleasedOnPreAdmissionFailure(t *testing.T) {
	native := &callbackNativeStub{handled: true, err: errors.New("pre-admission failure")}
	d, mgr, _ := newCallbackClaimDispatcher(native)
	ctx := context.Background()
	update := &tg.UpdateInlineBotCallbackQuery{
		QueryID: 2002,
		UserID:  50,
		MsgID:   &tg.InputBotInlineMessageID64{DCID: 1, ID: 2, AccessHash: 3},
		Data:    []byte("a2:test:invalid.1"),
	}

	if err := d.OnInlineBotCallbackQuery(ctx, tg.Entities{}, update); err != nil {
		t.Fatalf("first callback: %v", err)
	}
	processed, err := mgr.IsProcessedContext(ctx, "inline_cb:2002")
	if err != nil {
		t.Fatalf("claim lookup: %v", err)
	}
	if processed {
		t.Fatal("pre-admission inline failure left callback durably claimed")
	}

	if err := d.OnInlineBotCallbackQuery(ctx, tg.Entities{}, update); err != nil {
		t.Fatalf("retry callback: %v", err)
	}
	if got := native.calls.Load(); got != 2 {
		t.Fatalf("native inline callback calls=%d, want retry to reach native ingress", got)
	}
}

func TestDispatcherAcceptedCallbackClaimBlocksDuplicateAndAnswersIt(t *testing.T) {
	native := &callbackNativeStub{handled: true}
	d, mgr, svc := newCallbackClaimDispatcher(native)
	ctx := context.Background()
	update := &tg.UpdateBotCallbackQuery{
		QueryID: 2003,
		UserID:  50,
		Peer:    &tg.PeerChat{ChatID: 10},
		MsgID:   20,
		Data:    []byte("a2:test:valid.1"),
	}

	if err := d.OnBotCallbackQuery(ctx, tg.Entities{}, update); err != nil {
		t.Fatalf("first callback: %v", err)
	}
	processed, err := mgr.IsProcessedContext(ctx, "cb:2003")
	if err != nil || !processed {
		t.Fatalf("accepted callback processed=%v err=%v", processed, err)
	}

	if err := d.OnBotCallbackQuery(ctx, tg.Entities{}, update); err != nil {
		t.Fatalf("duplicate callback: %v", err)
	}
	if got := native.calls.Load(); got != 1 {
		t.Fatalf("duplicate reached native ingress: calls=%d", got)
	}
	if got := svc.callCount.Load(); got != 1 {
		t.Fatalf("duplicate ACK calls=%d, want 1", got)
	}
	svc.mu.Lock()
	answer := svc.answered[update.QueryID]
	svc.mu.Unlock()
	if answer != "" {
		t.Fatalf("duplicate callback answer=%q, want silent ACK", answer)
	}
}
