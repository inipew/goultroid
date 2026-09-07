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

// MessageTarget encapsulates immutable coordinates required to address a dialog-scoped Telegram message.
type MessageTarget struct {
	peer         tg.InputPeerClass
	messageID    int
	chatID       int64
	chatInstance int64
}

var _ Target = (*MessageTarget)(nil)

// NewMessageTarget creates an immutable MessageTarget.
func NewMessageTarget(peer tg.InputPeerClass, msgID int, chatID int64, chatInstance int64) MessageTarget {
	return MessageTarget{
		peer:         peer,
		messageID:    msgID,
		chatID:       chatID,
		chatInstance: chatInstance,
	}
}

// Peer returns the InputPeer of this target.
func (m MessageTarget) Peer() tg.InputPeerClass {
	return m.peer
}

// MessageID returns the message ID on Telegram.
func (m MessageTarget) MessageID() int {
	return m.messageID
}

// ChatID returns the chat ID.
func (m MessageTarget) ChatID() int64 {
	return m.chatID
}

// ChatInstance returns the callback chat instance coordinate.
func (m MessageTarget) ChatInstance() int64 {
	return m.chatInstance
}

// Kind returns TargetKindMessage.
func (m MessageTarget) Kind() TargetKind {
	return TargetKindMessage
}

// IsValid reports whether MessageTarget has a non-nil peer and valid positive message ID.
func (m MessageTarget) IsValid() bool {
	return m.peer != nil && m.messageID > 0
}

// InlineTarget encapsulates immutable coordinates for an inline-sent message.
type InlineTarget struct {
	queryID      int64
	messageID    tg.InputBotInlineMessageIDClass
	chatInstance int64
}

var _ Target = (*InlineTarget)(nil)

// NewInlineTarget creates an immutable InlineTarget.
func NewInlineTarget(queryID int64, msgID tg.InputBotInlineMessageIDClass, chatInstance int64) InlineTarget {
	return InlineTarget{
		queryID:      queryID,
		messageID:    msgID,
		chatInstance: chatInstance,
	}
}

// QueryID returns the original query ID.
func (i InlineTarget) QueryID() int64 {
	return i.queryID
}

// MessageID returns the inline message ID.
func (i InlineTarget) MessageID() tg.InputBotInlineMessageIDClass {
	return i.messageID
}

// ChatInstance returns the callback chat instance coordinate.
func (i InlineTarget) ChatInstance() int64 {
	return i.chatInstance
}

// Kind returns TargetKindInline.
func (i InlineTarget) Kind() TargetKind {
	return TargetKindInline
}

// IsValid reports whether InlineTarget has a non-nil message ID and non-zero query ID.
func (i InlineTarget) IsValid() bool {
	return i.messageID != nil && i.queryID != 0
}
