package core

import (
	"errors"
	"fmt"

	"github.com/gotd/td/tg"
)

type PeerKind uint8

const (
	PeerKindUser PeerKind = iota
	PeerKindChat
	PeerKindChannel
)

type PeerRef struct {
	Kind       PeerKind
	ID         int64
	AccessHash int64
}

func (p PeerRef) Valid() bool {
	if p.ID == 0 {
		return false
	}
	return p.Kind == PeerKindChat || p.AccessHash != 0
}

func (p PeerRef) IsUser() bool    { return p.Kind == PeerKindUser }
func (p PeerRef) IsChat() bool    { return p.Kind == PeerKindChat }
func (p PeerRef) IsChannel() bool { return p.Kind == PeerKindChannel }

func (p PeerRef) InputPeer() (tg.InputPeerClass, error) {
	if p.ID == 0 {
		return nil, errors.New("peer ID is zero")
	}
	switch p.Kind {
	case PeerKindUser:
		if p.AccessHash == 0 {
			return nil, fmt.Errorf("%w: user %d", ErrAccessHashMissing, p.ID)
		}
		return &tg.InputPeerUser{UserID: p.ID, AccessHash: p.AccessHash}, nil
	case PeerKindChat:
		return &tg.InputPeerChat{ChatID: p.ID}, nil
	case PeerKindChannel:
		if p.AccessHash == 0 {
			return nil, fmt.Errorf("%w: channel %d", ErrAccessHashMissing, p.ID)
		}
		return &tg.InputPeerChannel{ChannelID: p.ID, AccessHash: p.AccessHash}, nil
	default:
		return nil, fmt.Errorf("unknown peer kind %d", p.Kind)
	}
}

func PeerRefFromInputPeer(p tg.InputPeerClass) (PeerRef, error) {
	switch v := p.(type) {
	case *tg.InputPeerUser:
		return PeerRef{Kind: PeerKindUser, ID: v.UserID, AccessHash: v.AccessHash}, nil
	case *tg.InputPeerChat:
		return PeerRef{Kind: PeerKindChat, ID: v.ChatID}, nil
	case *tg.InputPeerChannel:
		return PeerRef{Kind: PeerKindChannel, ID: v.ChannelID, AccessHash: v.AccessHash}, nil
	case *tg.InputPeerSelf:
		return PeerRef{}, nil
	default:
		return PeerRef{}, fmt.Errorf("unsupported input peer %T", p)
	}
}

func PeerRefFromPeer(p tg.PeerClass, accessHash int64) (PeerRef, error) {
	switch v := p.(type) {
	case *tg.PeerUser:
		return PeerRef{Kind: PeerKindUser, ID: v.UserID, AccessHash: accessHash}, nil
	case *tg.PeerChat:
		return PeerRef{Kind: PeerKindChat, ID: v.ChatID}, nil
	case *tg.PeerChannel:
		return PeerRef{Kind: PeerKindChannel, ID: v.ChannelID, AccessHash: accessHash}, nil
	default:
		return PeerRef{}, fmt.Errorf("unsupported peer %T", p)
	}
}
