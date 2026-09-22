package client

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"go.uber.org/zap"
)

type precedenceResolver struct{}

func (precedenceResolver) Resolve(_ context.Context, raw tg.PeerClass, senderID int64, _ tg.Entities) (tg.InputPeerClass, error) {
	switch p := raw.(type) {
	case *tg.PeerUser:
		return &tg.InputPeerUser{UserID: p.UserID, AccessHash: 1}, nil
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: p.ChatID}, nil
	default:
		return &tg.InputPeerUser{UserID: senderID, AccessHash: 1}, nil
	}
}
func (precedenceResolver) ReResolve(_ context.Context, input tg.InputPeerClass) (tg.InputPeerClass, error) {
	return input, nil
}
func (precedenceResolver) InvalidatePeer(tg.InputPeerClass) {}
func (precedenceResolver) Cache() peer.Cache { return nil }

type precedenceTextIngress struct {
	calls   int
	handled bool
	err     error
}

func (i *precedenceTextIngress) tryText(context.Context, string, int64, int64, tg.InputPeerClass) (bool, error) {
	i.calls++
	return i.handled, i.err
}

type precedenceRelayIngress struct {
	ownerCalls     int
	visitorCalls   int
	ownerHandled   bool
	visitorHandled bool
	ownerErr       error
	visitorErr     error
}

func (i *precedenceRelayIngress) tryOwnerReply(context.Context, pmrelay.IngressMessage) (bool, error) {
	i.ownerCalls++
	return i.ownerHandled, i.ownerErr
}

func (i *precedenceRelayIngress) tryVisitor(context.Context, pmrelay.IngressMessage) (bool, error) {
	i.visitorCalls++
	return i.visitorHandled, i.visitorErr
}

func dispatchPrecedenceMessage(t *testing.T, deps UpdateHandlerDeps, message *tg.Message) {
	t.Helper()
	dispatcher := tg.NewUpdateDispatcher()
	RegisterUpdateHandlers(&dispatcher, deps)
	if err := dispatcher.Handle(context.Background(), &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: message}},
	}); err != nil {
		t.Fatalf("dispatcher.Handle() error = %v", err)
	}
}

func basePrecedenceDeps(input interactionTextIngress, relay relayMessageIngress) UpdateHandlerDeps {
	return UpdateHandlerDeps{
		Logger:             zap.NewNop(),
		Resolver:           precedenceResolver{},
		Interaction:        interaction.NewClientInteraction(nil, zap.NewNop()),
		InteractionIngress: input,
		RelayIngress:       relay,
	}
}

func TestRelayPrecedenceOwnerMappedReplyOutranksAwaitInput(t *testing.T) {
	input := &precedenceTextIngress{handled: true}
	relay := &precedenceRelayIngress{ownerHandled: true, visitorHandled: true}
	dispatchPrecedenceMessage(t, basePrecedenceDeps(input, relay), &tg.Message{
		ID:      101,
		Message: "owner reply",
		FromID:  &tg.PeerUser{UserID: 7},
		PeerID:  &tg.PeerUser{UserID: 7},
		ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 100},
	})
	if relay.ownerCalls != 1 {
		t.Fatalf("owner relay calls=%d, want 1", relay.ownerCalls)
	}
	if input.calls != 0 {
		t.Fatalf("AwaitInput calls=%d after mapped owner reply, want 0", input.calls)
	}
	if relay.visitorCalls != 0 {
		t.Fatalf("visitor fallback calls=%d, want 0", relay.visitorCalls)
	}
}

func TestRelayPrecedenceAwaitInputOutranksVisitorFallback(t *testing.T) {
	input := &precedenceTextIngress{handled: true}
	relay := &precedenceRelayIngress{}
	dispatchPrecedenceMessage(t, basePrecedenceDeps(input, relay), &tg.Message{
		ID:      11,
		Message: "pending setting value",
		FromID:  &tg.PeerUser{UserID: 42},
		PeerID:  &tg.PeerUser{UserID: 42},
	})
	if relay.ownerCalls != 1 {
		t.Fatalf("owner classification calls=%d, want 1", relay.ownerCalls)
	}
	if input.calls != 1 {
		t.Fatalf("AwaitInput calls=%d, want 1", input.calls)
	}
	if relay.visitorCalls != 0 {
		t.Fatalf("visitor fallback ran after claimed input: %d", relay.visitorCalls)
	}
}

func TestRelayPrecedenceVisitorFallbackRunsLast(t *testing.T) {
	input := &precedenceTextIngress{}
	relay := &precedenceRelayIngress{visitorHandled: true}
	dispatchPrecedenceMessage(t, basePrecedenceDeps(input, relay), &tg.Message{
		ID:      11,
		Message: "hello",
		FromID:  &tg.PeerUser{UserID: 42},
		PeerID:  &tg.PeerUser{UserID: 42},
	})
	if input.calls != 1 {
		t.Fatalf("AwaitInput calls=%d, want 1 before fallback", input.calls)
	}
	if relay.visitorCalls != 1 {
		t.Fatalf("visitor fallback calls=%d, want 1", relay.visitorCalls)
	}
}

func TestRelayPrecedenceSlashCommandsNeverFallThroughToRelay(t *testing.T) {
	for _, text := range []string{"/start", "/unknown", "/unknown@assistant"} {
		t.Run(text, func(t *testing.T) {
			input := &precedenceTextIngress{}
			relay := &precedenceRelayIngress{ownerHandled: true, visitorHandled: true}
			dispatchPrecedenceMessage(t, basePrecedenceDeps(input, relay), &tg.Message{
				ID:      21,
				Message: text,
				FromID:  &tg.PeerUser{UserID: 42},
				PeerID:  &tg.PeerUser{UserID: 42},
				ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 20},
			})
			if relay.ownerCalls != 0 || relay.visitorCalls != 0 {
				t.Fatalf("slash %q reached relay owner=%d visitor=%d", text, relay.ownerCalls, relay.visitorCalls)
			}
			if input.calls != 0 {
				t.Fatalf("slash %q reached generic input %d times", text, input.calls)
			}
		})
	}
}

func TestRelayPrecedenceCancelRemainsInteractionControl(t *testing.T) {
	input := &precedenceTextIngress{handled: true}
	relay := &precedenceRelayIngress{ownerHandled: true, visitorHandled: true}
	dispatchPrecedenceMessage(t, basePrecedenceDeps(input, relay), &tg.Message{
		ID:      31,
		Message: "/cancel",
		FromID:  &tg.PeerUser{UserID: 7},
		PeerID:  &tg.PeerUser{UserID: 7},
		ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 30},
	})
	if input.calls != 1 {
		t.Fatalf("/cancel input calls=%d, want 1", input.calls)
	}
	if relay.ownerCalls != 0 || relay.visitorCalls != 0 {
		t.Fatalf("/cancel reached relay owner=%d visitor=%d", relay.ownerCalls, relay.visitorCalls)
	}
}

func TestRelayIngressShutdownBarrierRunsBeforePrepare(t *testing.T) {
	relay := &precedenceRelayIngress{ownerHandled: true, visitorHandled: true}
	deps := basePrecedenceDeps(&precedenceTextIngress{}, relay)
	deps.IsShuttingDown = func() bool { return true }
	dispatchPrecedenceMessage(t, deps, &tg.Message{
		ID:      41,
		Message: "hello",
		FromID:  &tg.PeerUser{UserID: 42},
		PeerID:  &tg.PeerUser{UserID: 42},
	})
	if relay.ownerCalls != 0 || relay.visitorCalls != 0 {
		t.Fatalf("shutdown update reached relay owner=%d visitor=%d", relay.ownerCalls, relay.visitorCalls)
	}
}
