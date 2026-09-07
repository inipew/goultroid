package peer

import "strconv"

// PeerKind identifies the Telegram peer type without importing gotd/tg.
type PeerKind uint8

const (
	PeerKindUser    PeerKind = iota // private user
	PeerKindChat                   // legacy group
	PeerKindChannel                // supergroup / channel
	PeerKindSelf                   // self
)

// PeerRef is the minimal stable identity for a Telegram peer.
// It contains only the fields required to build an InputPeer.
// Username, phone, title, display names are PeerMetadata, not identity.
type PeerRef struct {
	Kind       PeerKind
	ID         int64
	AccessHash int64
}

// IsZero reports whether the reference is empty.
func (p PeerRef) IsZero() bool {
	return p.ID == 0 && p.Kind == 0
}

// IsUser reports whether the peer is a user.
func (p PeerRef) IsUser() bool { return p.Kind == PeerKindUser }

// IsChannel reports whether the peer is a channel/supergroup.
func (p PeerRef) IsChannel() bool { return p.Kind == PeerKindChannel }

// String returns a debug representation.
func (p PeerRef) String() string {
	switch p.Kind {
	case PeerKindUser:
		return "user:" + itoa(p.ID)
	case PeerKindChat:
		return "chat:" + itoa(p.ID)
	case PeerKindChannel:
		return "channel:" + itoa(p.ID)
	case PeerKindSelf:
		return "self"
	default:
		return "unknown"
	}
}

func itoa(i int64) string {
	return strconv.FormatInt(i, 10)
}
