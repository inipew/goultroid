package adapter

import (
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/domain/peer"
)

// ToInputPeer converts a domain PeerRef to a Telegram InputPeerClass.
// This is the anti-corruption layer: core/domain never imports tg, only this adapter does.
func ToInputPeer(ref peer.PeerRef) (tg.InputPeerClass, error) {
	switch ref.Kind {
	case peer.PeerKindSelf:
		return &tg.InputPeerSelf{}, nil
	case peer.PeerKindUser:
		return &tg.InputPeerUser{UserID: ref.ID, AccessHash: ref.AccessHash}, nil
	case peer.PeerKindChat:
		return &tg.InputPeerChat{ChatID: ref.ID}, nil
	case peer.PeerKindChannel:
		return &tg.InputPeerChannel{ChannelID: ref.ID, AccessHash: ref.AccessHash}, nil
	default:
		return nil, fmt.Errorf("unknown peer kind %d", ref.Kind)
	}
}

// FromInputPeer converts a Telegram InputPeerClass to a domain PeerRef.
func FromInputPeer(p tg.InputPeerClass) (peer.PeerRef, error) {
	switch v := p.(type) {
	case *tg.InputPeerSelf:
		return peer.PeerRef{Kind: peer.PeerKindSelf}, nil
	case *tg.InputPeerUser:
		return peer.PeerRef{Kind: peer.PeerKindUser, ID: v.UserID, AccessHash: v.AccessHash}, nil
	case *tg.InputPeerChat:
		return peer.PeerRef{Kind: peer.PeerKindChat, ID: v.ChatID}, nil
	case *tg.InputPeerChannel:
		return peer.PeerRef{Kind: peer.PeerKindChannel, ID: v.ChannelID, AccessHash: v.AccessHash}, nil
	case nil:
		return peer.PeerRef{}, fmt.Errorf("nil InputPeer")
	default:
		return peer.PeerRef{}, fmt.Errorf("unsupported InputPeer type %T", p)
	}
}

// FromPeer converts a Telegram PeerClass (from tg.Entities) to a domain PeerRef.
// AccessHash is resolved via entities when available; if missing, AccessHash is 0 and caller should resolve via storage.
func FromPeer(p tg.PeerClass, e tg.Entities) peer.PeerRef {
	switch v := p.(type) {
	case *tg.PeerUser:
		var hash int64
		if u, ok := e.Users[v.UserID]; ok && u != nil {
			hash = u.AccessHash
		}
		return peer.PeerRef{Kind: peer.PeerKindUser, ID: v.UserID, AccessHash: hash}
	case *tg.PeerChat:
		return peer.PeerRef{Kind: peer.PeerKindChat, ID: v.ChatID}
	case *tg.PeerChannel:
		var hash int64
		if ch, ok := e.Channels[v.ChannelID]; ok && ch != nil {
			hash = ch.AccessHash
		}
		return peer.PeerRef{Kind: peer.PeerKindChannel, ID: v.ChannelID, AccessHash: hash}
	default:
		return peer.PeerRef{}
	}
}
