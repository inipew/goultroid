package telegram

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func TestPhase8DispatcherAutomationOriginIsPeerScoped(t *testing.T) {
	service := NewService(nil)
	service.recordBotSent(&tg.InputPeerUser{UserID: 42, AccessHash: 111}, 77)
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1001, nil), service, zap.NewNop())
	var origins []core.ExecutionSource
	d.AddPrioritizedCanonicalMessageHandlerWithRouting(PrioritySecurity, core.MessageHookRouting{Lane: core.MessageHookDecision}, func(ctx context.Context, _ *core.MessageEnvelope) error {
		decision := core.GetMessageDecision(ctx)
		if decision == nil {
			t.Fatal("ingress decision was not attached")
		}
		origins = append(origins, decision.Origin())
		return nil
	})
	for _, tc := range []struct {
		name string
		peer tg.PeerClass
		want core.ExecutionSource
	}{
		{"sent private peer", &tg.PeerUser{UserID: 42}, core.ExecutionAutomation},
		{"other private peer", &tg.PeerUser{UserID: 43}, core.ExecutionInteractive},
		{"same number group", &tg.PeerChat{ChatID: 42}, core.ExecutionInteractive},
		{"same number channel", &tg.PeerChannel{ChannelID: 42}, core.ExecutionInteractive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origins = nil
			msg := &tg.Message{ID: 77, Out: true, PeerID: tc.peer, Message: "manual text"}
			if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
				t.Fatal(err)
			}
			if len(origins) != 1 || origins[0] != tc.want {
				t.Fatalf("origin=%v want %v", origins, tc.want)
			}
		})
	}
}
