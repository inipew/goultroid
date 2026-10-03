package telegram

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

func TestDispatcher_StateGateSkipsDecisionBeforeTaskAdmission(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())

	var handlerCalls atomic.Int32
	d.AddScopedMessageHandlerWithRoutingAndState(
		PrioritySecurity,
		tasks.ScopeIdentity{Owner: "plugin:blacklist", Generation: 1},
		core.MessageHookRouting{
			Lane: core.MessageHookDecision,
			Interests: []core.MessageHookInterest{{
				Directions:  core.MessageDirectionIncoming,
				Peers:       core.MessagePeerGroup,
				Commands:    core.MessagePlain,
				RequireText: true,
			}},
		},
		func(chatID int64) bool { return chatID == 42 },
		func(context.Context, tg.Entities, *tg.Message, bool, string) error {
			handlerCalls.Add(1)
			return nil
		},
	)

	handlers, _ := d.messageHandlersFor(&tg.Message{
		PeerID:  &tg.PeerChat{ChatID: 7},
		Message: "ordinary traffic",
	}, false)
	if len(handlers) != 1 {
		t.Fatalf("structural route handlers=%d, want 1", len(handlers))
	}

	// No TaskEngine is configured. If the state gate is evaluated after task
	// admission this would fail closed; instead inactive chat 7 is skipped.
	if handled := d.executeDecisionHandlers(context.Background(), handlers, tg.Entities{}, &tg.Message{
		ID:      1,
		PeerID:  &tg.PeerChat{ChatID: 7},
		Message: "ordinary traffic",
	}, false, ""); handled {
		t.Fatal("inactive feature state unexpectedly failed closed")
	}
	if got := handlerCalls.Load(); got != 0 {
		t.Fatalf("inactive state handler calls=%d, want 0", got)
	}
}

func TestDispatcher_StateGatePanicFailsOpen(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	registered := prioritizedHandler{
		id:        1,
		stateGate: func(int64) bool { panic("broken snapshot") },
	}
	if !d.messageHookStateInterested(registered, 7) {
		t.Fatal("panicking state gate must fail open")
	}
}
