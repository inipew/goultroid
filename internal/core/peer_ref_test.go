package core

import (
	"errors"
	"testing"

	"github.com/gotd/td/tg"
)

func TestPeerRefRejectsMissingAccessHash(t *testing.T) {
	for _, peer := range []PeerRef{
		{Kind: PeerKindUser, ID: 1},
		{Kind: PeerKindChannel, ID: 2},
	} {
		if _, err := peer.InputPeer(); !errors.Is(err, ErrAccessHashMissing) {
			t.Fatalf("expected ErrAccessHashMissing, got %v", err)
		}
	}
}

func TestPeerRefRoundTrip(t *testing.T) {
	input := &tg.InputPeerUser{UserID: 42, AccessHash: 99}
	peer, err := PeerRefFromInputPeer(input)
	if err != nil {
		t.Fatal(err)
	}
	if !peer.Valid() || peer.Kind != PeerKindUser || peer.ID != 42 || peer.AccessHash != 99 {
		t.Fatalf("bad ref: %+v", peer)
	}
	output, err := peer.InputPeer()
	if err != nil {
		t.Fatal(err)
	}
	got := output.(*tg.InputPeerUser)
	if got.UserID != 42 || got.AccessHash != 99 {
		t.Fatalf("bad output: %+v", got)
	}
}

func TestPeerRefChatHasNoAccessHash(t *testing.T) {
	peer := PeerRef{Kind: PeerKindChat, ID: 7}
	if !peer.Valid() {
		t.Fatal("chat peer should be valid without access hash")
	}
	output, err := peer.InputPeer()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := output.(*tg.InputPeerChat); !ok {
		t.Fatalf("got %T", output)
	}
}
