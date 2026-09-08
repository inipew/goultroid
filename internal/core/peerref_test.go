package core_test

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

func TestPeerRefInputPeerRoundTrip(t *testing.T) {
	cases := []core.PeerRef{
		{Kind: core.PeerKindUser, ID: 42, AccessHash: 123},
		{Kind: core.PeerKindChat, ID: 77},
		{Kind: core.PeerKindChannel, ID: 88, AccessHash: 456},
	}
	for _, want := range cases {
		ip, err := want.InputPeer()
		if err != nil {
			t.Fatal(err)
		}
		got, err := core.PeerRefFromInputPeer(ip)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

func TestPeerRefRejectsMissingAccessHash(t *testing.T) {
	if _, err := (core.PeerRef{Kind: core.PeerKindUser, ID: 1}).InputPeer(); err == nil {
		t.Fatal("expected user access hash error")
	}
	if _, err := (core.PeerRef{Kind: core.PeerKindChannel, ID: 1}).InputPeer(); err == nil {
		t.Fatal("expected channel access hash error")
	}
	if _, err := core.PeerRefFromInputPeer(&tg.InputPeerUser{UserID: 1}); err == nil {
		t.Fatal("expected missing user access hash error")
	}
}
