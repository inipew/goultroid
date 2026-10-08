package core

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
)

type peerOriginProbe struct {
	calls int
	peer  tg.PeerClass
}

func (p *peerOriginProbe) IsBotSentForPeer(peer tg.PeerClass, msgID int, selfID int64) bool {
	p.calls++
	p.peer = peer
	u, ok := peer.(*tg.PeerUser)
	return ok && u.UserID == 42 && msgID == 77 && selfID == 1001
}

type idOnlyOriginProbe struct{}

func (idOnlyOriginProbe) IsBotSent(int) bool { return true }

func TestAutomatedOutgoingMessageRequiresPeerAwareIdentity(t *testing.T) {
	cases := []struct {
		name string
		kind PeerKind
		id   int64
		out  bool
		want bool
	}{
		{"matching private peer", PeerKindUser, 42, true, true},
		{"other private peer", PeerKindUser, 43, true, false},
		{"same number group", PeerKindChat, 42, true, false},
		{"same number channel", PeerKindChannel, 42, true, false},
		{"incoming ignored", PeerKindUser, 42, false, false},
		{"missing peer ID", PeerKindUser, 0, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &peerOriginProbe{}
			msg := &MessageEnvelope{ID: 77, Outgoing: tc.out, Peer: PeerRef{Kind: tc.kind, ID: tc.id}}
			got := IsAutomatedOutgoingMessage(context.Background(), msg, probe, 1001)
			if got != tc.want {
				t.Fatalf("automated=%v want=%v", got, tc.want)
			}
			wantCalls := 0
			if tc.out && tc.id != 0 {
				wantCalls = 1
			}
			if probe.calls != wantCalls {
				t.Fatalf("peer lookups=%d want=%d", probe.calls, wantCalls)
			}
		})
	}
	msg := &MessageEnvelope{ID: 77, Outgoing: true, Peer: PeerRef{Kind: PeerKindUser, ID: 42}}
	if IsAutomatedOutgoingMessage(context.Background(), msg, idOnlyOriginProbe{}, 1001) {
		t.Fatal("ID-only tracker must not classify cross-peer messages")
	}
}

func TestAutomatedOutgoingMessageIngressDecisionIsAuthoritative(t *testing.T) {
	probe := &peerOriginProbe{}
	msg := &MessageEnvelope{ID: 77, Outgoing: true, Peer: PeerRef{Kind: PeerKindUser, ID: 42}}
	manualCtx := WithMessageDecision(context.Background(), NewMessageDecision(ExecutionInteractive))
	if IsAutomatedOutgoingMessage(manualCtx, msg, probe, 1001) || probe.calls != 0 {
		t.Fatal("interactive ingress decision overridden by tracker")
	}
	automationCtx := WithMessageDecision(context.Background(), NewMessageDecision(ExecutionAutomation))
	msg.Peer.ID = 43
	if !IsAutomatedOutgoingMessage(automationCtx, msg, probe, 1001) || probe.calls != 0 {
		t.Fatal("automation ingress decision overridden by tracker")
	}
}
