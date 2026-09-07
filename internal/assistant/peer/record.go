package peer

import (
	"time"
)

// PeerKind specifies whether a peer is a user, a legacy basic chat, or a broadcast/supergroup channel.
type PeerKind uint8

const (
	// PeerKindUser identifies a private Telegram user.
	PeerKindUser PeerKind = iota
	// PeerKindChat identifies a basic Telegram group chat.
	PeerKindChat
	// PeerKindChannel identifies a supergroup or broadcast channel.
	PeerKindChannel
)

// String returns a readable string of the peer kind.
func (k PeerKind) String() string {
	switch k {
	case PeerKindUser:
		return "user"
	case PeerKindChat:
		return "chat"
	case PeerKindChannel:
		return "channel"
	default:
		return "unknown"
	}
}

// PeerRecord holds cached authorization coordinates for a Telegram entity.
type PeerRecord struct {
	ID         int64
	Kind       PeerKind
	AccessHash int64
	UpdatedAt  time.Time
}
