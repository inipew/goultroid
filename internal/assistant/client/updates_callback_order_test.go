package client

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"go.uber.org/zap"
)

type countingCallbackResolver struct {
	calls atomic.Int32
	peer  tg.InputPeerClass
}

func (r *countingCallbackResolver) Resolve(context.Context, tg.PeerClass, int64, tg.Entities) (tg.InputPeerClass, error) {
	r.calls.Add(1)
	if r.peer != nil {
		return r.peer, nil
	}
	return &tg.InputPeerChat{ChatID: 10}, nil
}

func (*countingCallbackResolver) ReResolve(_ context.Context, input tg.InputPeerClass) (tg.InputPeerClass, error) {
	return input, nil
}
func (*countingCallbackResolver) InvalidatePeer(tg.InputPeerClass) {}
func (*countingCallbackResolver) Cache() peer.Cache                { return nil }

type recordingCallbackIngress struct {
	messageCalls atomic.Int32
	lastPeer     tg.InputPeerClass
}

func (*recordingCallbackIngress) tryText(context.Context, string, int64, int64, tg.InputPeerClass) (bool, error) {
	return false, nil
}
func (*recordingCallbackIngress) tryInline(context.Context, []byte, int64, int64, tg.InputBotInlineMessageIDClass) (bool, error) {
	return false, nil
}
func (i *recordingCallbackIngress) tryMessage(_ context.Context, _ []byte, _ int64, _ int64, inputPeer tg.InputPeerClass, _ int64, _ int) (bool, error) {
	i.messageCalls.Add(1)
	i.lastPeer = inputPeer
	return true, nil
}

func TestAssistantUnknownAndNoopCallbacksBypassPeerResolver(t *testing.T) {
	for _, data := range [][]byte{[]byte("unknown"), []byte("noop")} {
		t.Run(string(data), func(t *testing.T) {
			dispatcher := tg.NewUpdateDispatcher()
			api := &mockTelegramAPI{}
			resolver := &countingCallbackResolver{}
			RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
				Logger:          zap.NewNop(),
				Resolver:        resolver,
				Interaction:     assistantinteraction.NewClientInteraction(api, zap.NewNop()),
				CallbackDeduper: newCallbackQueryDeduper(),
			})

			update := &tg.UpdateBotCallbackQuery{
				QueryID: 3101,
				UserID:  42,
				Peer:    &tg.PeerChat{ChatID: 10},
				MsgID:   20,
				Data:    data,
			}
			if err := dispatcher.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{update}}); err != nil {
				t.Fatalf("handle callback: %v", err)
			}
			if got := resolver.calls.Load(); got != 0 {
				t.Fatalf("resolver calls=%d, want 0 for callback %q", got, data)
			}
		})
	}
}

func TestAssistantDuplicateA2CallbackBypassesPeerResolver(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	api := &mockTelegramAPI{}
	resolver := &countingCallbackResolver{}
	ingress := &recordingCallbackIngress{}
	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		Logger:             zap.NewNop(),
		Resolver:           resolver,
		Interaction:        assistantinteraction.NewClientInteraction(api, zap.NewNop()),
		CallbackDeduper:    newCallbackQueryDeduper(),
		InteractionIngress: ingress,
	})

	update := &tg.UpdateBotCallbackQuery{
		QueryID: 3102,
		UserID:  42,
		Peer:    &tg.PeerChat{ChatID: 10},
		MsgID:   20,
		Data:    []byte("a2:next:AAAAAAAAAAAAAAAAAAAAAA.1"),
	}
	if err := dispatcher.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{update}}); err != nil {
		t.Fatalf("first callback: %v", err)
	}
	if got := resolver.calls.Load(); got != 1 {
		t.Fatalf("first resolver calls=%d, want 1", got)
	}
	if got := ingress.messageCalls.Load(); got != 1 {
		t.Fatalf("first ingress calls=%d, want 1", got)
	}

	resolver.calls.Store(0)
	if err := dispatcher.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{update}}); err != nil {
		t.Fatalf("duplicate callback: %v", err)
	}
	if got := resolver.calls.Load(); got != 0 {
		t.Fatalf("duplicate resolver calls=%d, want 0", got)
	}
	if got := ingress.messageCalls.Load(); got != 1 {
		t.Fatalf("duplicate reached interaction ingress: calls=%d", got)
	}
}

func TestAssistantFreshA2CallbackResolvesPeerBeforeInteractionIngress(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	api := &mockTelegramAPI{}
	resolvedPeer := &tg.InputPeerChat{ChatID: 10}
	resolver := &countingCallbackResolver{peer: resolvedPeer}
	ingress := &recordingCallbackIngress{}
	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		Logger:             zap.NewNop(),
		Resolver:           resolver,
		Interaction:        assistantinteraction.NewClientInteraction(api, zap.NewNop()),
		CallbackDeduper:    newCallbackQueryDeduper(),
		InteractionIngress: ingress,
	})

	update := &tg.UpdateBotCallbackQuery{
		QueryID: 3103,
		UserID:  42,
		Peer:    &tg.PeerChat{ChatID: 10},
		MsgID:   20,
		Data:    []byte("a2:next:AAAAAAAAAAAAAAAAAAAAAA.1"),
	}
	if err := dispatcher.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{update}}); err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	if got := resolver.calls.Load(); got != 1 {
		t.Fatalf("resolver calls=%d, want 1", got)
	}
	if got := ingress.messageCalls.Load(); got != 1 {
		t.Fatalf("interaction ingress calls=%d, want 1", got)
	}
	if ingress.lastPeer != resolvedPeer {
		t.Fatalf("interaction ingress peer=%#v, want %#v", ingress.lastPeer, resolvedPeer)
	}
}
