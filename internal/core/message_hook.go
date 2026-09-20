package core

// MessageHookLane separates synchronous decision/interception work from
// asynchronous feature/observability work.
type MessageHookLane uint8

const (
	MessageHookDecision MessageHookLane = iota
	MessageHookEvent
)

// MessageDirectionMask describes which Telegram message directions a hook
// wants to receive.
type MessageDirectionMask uint8

const (
	MessageDirectionIncoming MessageDirectionMask = 1 << iota
	MessageDirectionOutgoing
	MessageDirectionAny = MessageDirectionIncoming | MessageDirectionOutgoing
)

// MessagePeerMask describes which Telegram peer classes a hook wants to
// receive.
type MessagePeerMask uint8

const (
	MessagePeerPrivate MessagePeerMask = 1 << iota
	MessagePeerGroup
	MessagePeerChannel
	MessagePeerUnknown
	MessagePeerStable = MessagePeerPrivate | MessagePeerGroup | MessagePeerChannel
	MessagePeerAny    = MessagePeerStable | MessagePeerUnknown
)

// MessageCommandMask describes whether a hook is interested in command-like
// messages, ordinary messages, or both.
type MessageCommandMask uint8

const (
	MessagePlain MessageCommandMask = 1 << iota
	MessageCommand
	MessageCommandAny = MessagePlain | MessageCommand
)

// MessageHookInterest is one structural routing clause. Multiple clauses in a
// MessageHookRouting are OR-ed together. Zero masks mean "any" for backwards
// compatibility; Require* fields narrow a clause further.
//
// The fields intentionally describe only facts available directly on the
// Telegram update. Runtime feature state (for example whether a chat currently
// has filters configured) belongs to the feature-state snapshot layer.
type MessageHookInterest struct {
	Directions     MessageDirectionMask
	Peers          MessagePeerMask
	Commands       MessageCommandMask
	RequireText    bool
	RequireMention bool
	RequireReply   bool
	RequireMedia   bool
}

// MessageHookRouting describes where a raw-message hook belongs and which
// structural message classes should be routed to it. Empty Interests means all
// message classes.
type MessageHookRouting struct {
	Lane      MessageHookLane
	Interests []MessageHookInterest
}
