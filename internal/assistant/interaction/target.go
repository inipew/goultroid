package interaction

import (
	"github.com/gotd/td/tg"
)

// TargetKind identifies whether an interaction targets a standard dialog message or an inline query result.
type TargetKind uint8

const (
	// TargetKindMessage represents a callback targeting a normal message in a private chat, group, or channel.
	TargetKindMessage TargetKind = iota
	// TargetKindInline represents a callback targeting an inline bot message.
	TargetKindInline
)

// String returns a human-readable representation of TargetKind.
func (k TargetKind) String() string {
	switch k {
	case TargetKindMessage:
		return "message"
	case TargetKindInline:
		return "inline"
	default:
		return "unknown"
	}
}

// Target defines the contract for an interaction destination.
type Target interface {
	Kind() TargetKind
	IsValid() bool
}

// MessageTarget encapsulates the coordinates required to address a dialog-scoped Telegram message.
type MessageTarget struct {
	Peer         tg.InputPeerClass
	MessageID    int
	ChatID       int64
	ChatInstance int64
}

var _ Target = (*MessageTarget)(nil)

// NewMessageTarget creates an immutable MessageTarget.
func NewMessageTarget(peer tg.InputPeerClass, msgID int, chatID int64, chatInstance int64) MessageTarget {
	return MessageTarget{
		Peer:         peer,
		MessageID:    msgID,
		ChatID:       chatID,
		ChatInstance: chatInstance,
	}
}

// Kind returns TargetKindMessage.
func (m MessageTarget) Kind() TargetKind {
	return TargetKindMessage
}

// IsValid reports whether MessageTarget has a non-nil peer and valid positive message ID.
func (m MessageTarget) IsValid() bool {
	return m.Peer != nil && m.MessageID > 0
}

// InlineTarget encapsulates the coordinates for an inline-sent message.
type InlineTarget struct {
	QueryID      int64
	MessageID    tg.InputBotInlineMessageIDClass
	ChatInstance int64
}

var _ Target = (*InlineTarget)(nil)

// NewInlineTarget creates an immutable InlineTarget.
func NewInlineTarget(queryID int64, msgID tg.InputBotInlineMessageIDClass, chatInstance int64) InlineTarget {
	return InlineTarget{
		QueryID:      queryID,
		MessageID:    msgID,
		ChatInstance: chatInstance,
	}
}

// Kind returns TargetKindInline.
func (i InlineTarget) Kind() TargetKind {
	return TargetKindInline
}

// IsValid reports whether InlineTarget has a valid query ID or non-nil message ID.
func (i InlineTarget) IsValid() bool {
	return i.MessageID != nil
}
