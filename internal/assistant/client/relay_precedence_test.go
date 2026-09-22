package client

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
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
func (precedenceResolver) Cache() peer.Cache                { return nil }

type precedenceFailingResolver struct{}

func (precedenceFailingResolver) Resolve(context.Context, tg.PeerClass, int64, tg.Entities) (tg.InputPeerClass, error) {
	return nil, errors.New("peer unavailable")
}
func (precedenceFailingResolver) ReResolve(context.Context, tg.InputPeerClass) (tg.InputPeerClass, error) {
	return nil, errors.New("peer unavailable")
}
func (precedenceFailingResolver) InvalidatePeer(tg.InputPeerClass) {}
func (precedenceFailingResolver) Cache() peer.Cache                { return nil }

type precedenceTextIngress struct {
	calls   int
	handled bool
	err     error
}

func (i *precedenceTextIngress) tryText(context.Context, string, int64, int64, tg.InputPeerClass) (bool, error) {
	i.calls++
	return i.handled, i.err
}

func (i *precedenceTextIngress) tryInline(context.Context, []byte, int64, int64, tg.InputBotInlineMessageIDClass) (bool, error) {
	return false, nil
}

func (i *precedenceTextIngress) tryMessage(context.Context, []byte, int64, int64, tg.InputPeerClass, int64, int) (bool, error) {
	return false, nil
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

func basePrecedenceDeps(input interactionIngressPort, relay relayMessageIngress) UpdateHandlerDeps {
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

func TestRelayPrecedenceOwnerMappedMediaReplyOutranksAwaitInput(t *testing.T) {
	input := &precedenceTextIngress{handled: true}
	relay := &precedenceRelayIngress{ownerHandled: true}
	dispatchPrecedenceMessage(t, basePrecedenceDeps(input, relay), &tg.Message{
		ID:      102,
		Message: "media caption",
		FromID:  &tg.PeerUser{UserID: 7},
		PeerID:  &tg.PeerUser{UserID: 7},
		ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 100},
		Media:   &tg.MessageMediaPhoto{},
	})
	if relay.ownerCalls != 1 {
		t.Fatalf("owner relay calls=%d, want 1", relay.ownerCalls)
	}
	if input.calls != 0 {
		t.Fatalf("AwaitInput calls=%d after mapped owner media reply, want 0", input.calls)
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

func TestRelayPrecedenceInputClassificationErrorFailsClosed(t *testing.T) {
	input := &precedenceTextIngress{handled: false, err: errors.New("input store unavailable")}
	relay := &precedenceRelayIngress{visitorHandled: true}
	dispatchPrecedenceMessage(t, basePrecedenceDeps(input, relay), &tg.Message{
		ID:      14,
		Message: "must not become relay traffic",
		FromID:  &tg.PeerUser{UserID: 42},
		PeerID:  &tg.PeerUser{UserID: 42},
	})
	if input.calls != 1 {
		t.Fatalf("AwaitInput calls=%d, want 1", input.calls)
	}
	if relay.visitorCalls != 0 {
		t.Fatalf("visitor fallback ran after input classification error: %d", relay.visitorCalls)
	}
}

func TestRelayVisitorFallbackDoesNotRequireInteractionPresentation(t *testing.T) {
	relay := &precedenceRelayIngress{visitorHandled: true}
	dispatchPrecedenceMessage(t, UpdateHandlerDeps{
		Logger:       zap.NewNop(),
		RelayIngress: relay,
	}, &tg.Message{
		ID:      12,
		Message: "relay without a2 presentation",
		FromID:  &tg.PeerUser{UserID: 42},
		PeerID:  &tg.PeerUser{UserID: 42},
	})
	if relay.ownerCalls != 1 || relay.visitorCalls != 1 {
		t.Fatalf("relay calls owner=%d visitor=%d, want 1/1", relay.ownerCalls, relay.visitorCalls)
	}
}

func TestRelayVisitorFallbackSurvivesInteractionPeerResolutionFailure(t *testing.T) {
	input := &precedenceTextIngress{handled: true}
	relay := &precedenceRelayIngress{visitorHandled: true}
	dispatchPrecedenceMessage(t, UpdateHandlerDeps{
		Logger:             zap.NewNop(),
		Resolver:           precedenceFailingResolver{},
		Interaction:        interaction.NewClientInteraction(nil, zap.NewNop()),
		InteractionIngress: input,
		RelayIngress:       relay,
	}, &tg.Message{
		ID:      13,
		Message: "relay despite resolver failure",
		FromID:  &tg.PeerUser{UserID: 42},
		PeerID:  &tg.PeerUser{UserID: 42},
	})
	if input.calls != 0 {
		t.Fatalf("AwaitInput calls=%d with unresolved peer, want 0", input.calls)
	}
	if relay.ownerCalls != 1 || relay.visitorCalls != 1 {
		t.Fatalf("relay calls owner=%d visitor=%d, want 1/1", relay.ownerCalls, relay.visitorCalls)
	}
}

func TestRelayVisitorMediaDoesNotEnterP6CDeliveryCanary(t *testing.T) {
	input := &precedenceTextIngress{}
	relay := &precedenceRelayIngress{visitorHandled: true}
	dispatchPrecedenceMessage(t, basePrecedenceDeps(input, relay), &tg.Message{
		ID:      15,
		Message: "caption",
		FromID:  &tg.PeerUser{UserID: 42},
		PeerID:  &tg.PeerUser{UserID: 42},
		Media:   &tg.MessageMediaPhoto{},
	})
	if input.calls != 1 {
		t.Fatalf("AwaitInput calls=%d, want 1 before media gate", input.calls)
	}
	if relay.visitorCalls != 0 {
		t.Fatalf("P6-C visitor delivery accepted media: calls=%d", relay.visitorCalls)
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

func TestRelayPrecedenceTargetedCancelUsesAuthenticatedBotIdentity(t *testing.T) {
	input := &precedenceTextIngress{handled: true}
	relay := &precedenceRelayIngress{ownerHandled: true, visitorHandled: true}
	deps := basePrecedenceDeps(input, relay)
	cmdRouter := command.NewRouter(zap.NewNop())
	cmdRouter.SetBotUsername("Assistant")
	deps.CmdRouter = cmdRouter

	dispatchPrecedenceMessage(t, deps, &tg.Message{
		ID:      32,
		Message: "/cancel@Assistant",
		FromID:  &tg.PeerUser{UserID: 7},
		PeerID:  &tg.PeerUser{UserID: 7},
	})
	if input.calls != 1 {
		t.Fatalf("targeted /cancel input calls=%d, want 1", input.calls)
	}
	if relay.ownerCalls != 0 || relay.visitorCalls != 0 {
		t.Fatalf("targeted /cancel reached relay owner=%d visitor=%d", relay.ownerCalls, relay.visitorCalls)
	}
}

func TestRelayPrecedenceCancelForAnotherBotIsIgnored(t *testing.T) {
	input := &precedenceTextIngress{handled: true}
	relay := &precedenceRelayIngress{ownerHandled: true, visitorHandled: true}
	deps := basePrecedenceDeps(input, relay)
	cmdRouter := command.NewRouter(zap.NewNop())
	cmdRouter.SetBotUsername("Assistant")
	deps.CmdRouter = cmdRouter

	dispatchPrecedenceMessage(t, deps, &tg.Message{
		ID:      33,
		Message: "/cancel@OtherBot",
		FromID:  &tg.PeerUser{UserID: 7},
		PeerID:  &tg.PeerUser{UserID: 7},
	})
	if input.calls != 0 {
		t.Fatalf("other-bot /cancel reached interaction input %d times", input.calls)
	}
	if relay.ownerCalls != 0 || relay.visitorCalls != 0 {
		t.Fatalf("other-bot /cancel reached relay owner=%d visitor=%d", relay.ownerCalls, relay.visitorCalls)
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

func TestRelayIngressGroupMessageNeverReachesRelay(t *testing.T) {
	input := &precedenceTextIngress{}
	relay := &precedenceRelayIngress{ownerHandled: true, visitorHandled: true}
	dispatchPrecedenceMessage(t, basePrecedenceDeps(input, relay), &tg.Message{
		ID:      51,
		Message: "group message",
		FromID:  &tg.PeerUser{UserID: 42},
		PeerID:  &tg.PeerChat{ChatID: 99},
	})
	if relay.ownerCalls != 0 || relay.visitorCalls != 0 {
		t.Fatalf("group message reached relay owner=%d visitor=%d", relay.ownerCalls, relay.visitorCalls)
	}
}
