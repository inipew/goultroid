package core

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/tasks"
)

// RawMessageHookHandler is the privileged Telegram compatibility hook.
// New production hooks should prefer CanonicalMessageHookHandler.
type RawMessageHookHandler = func(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error

// CanonicalMessageHookHandler receives the normalized, transport-light message envelope.
type CanonicalMessageHookHandler = func(ctx context.Context, message *MessageEnvelope) error

// MessageHookFacts is the immutable, transport-neutral fact set available to a
// pure fast gate before TaskEngine admission. Keep this intentionally small:
// fields must already be known at dispatcher ingress and must not expose I/O
// capabilities or raw Telegram objects.
type MessageHookFacts struct {
	ChatID      int64
	Outgoing    bool
	IsCommand   bool
	CommandName string
	Origin      ExecutionSource
}

// MessageHookFastGate decides whether a structurally routed message is worth
// admitting to TaskEngine. Implementations must be pure, bounded, and
// non-blocking; a panic is treated as fail-open by the dispatcher.
type MessageHookFastGate func(MessageHookFacts) bool

// MessageHookRegistration is the single registration contract shared by the
// plugin manager and message dispatcher. Routing/state/scope are data rather
// than registrar capability interfaces.
//
// Exactly one of Handler or RawHandler should be set. LegacyRouting exists only
// for privileged raw compatibility hooks that predate explicit routing.
type MessageHookRegistration struct {
	Scope         tasks.ScopeIdentity
	Priority      int
	Routing       MessageHookRouting
	FastGate      MessageHookFastGate
	StateGate     func(int64) bool
	Execution     MessageHookExecutionPolicy
	Handler       CanonicalMessageHookHandler
	RawHandler    RawMessageHookHandler
	LegacyRouting bool
}

// MessageHookLane separates synchronous decision/interception work from
// asynchronous feature/observability work.
type MessageHookLane uint8

const (
	MessageHookDecision MessageHookLane = iota
	MessageHookEvent
)

// MessageHookFailurePolicy controls how decision hooks react when execution
// infrastructure or handler execution fails.
type MessageHookFailurePolicy uint8

const (
	MessageHookFailureDefault MessageHookFailurePolicy = iota
	MessageHookFailOpen
	MessageHookFailClosed
)

// MessageHookOrderingPolicy declares the TaskEngine serialization domain for
// one registration. Zero retains lane-specific canonical defaults.
type MessageHookOrderingPolicy uint8

const (
	MessageHookOrderingDefault MessageHookOrderingPolicy = iota
	MessageHookOrderingChat
	MessageHookOrderingPluginChat
)

// MessageHookExecutionPolicy is the execution contract carried by every
// canonical registration after plugin-manager normalization.
type MessageHookExecutionPolicy struct {
	FailurePolicy  MessageHookFailurePolicy
	HandlerTimeout time.Duration
	TaskTimeout    time.Duration
	Ordering       MessageHookOrderingPolicy
}

const (
	DefaultMessageHookHandlerTimeout      = 5 * time.Second
	DefaultMessageHookDecisionTaskTimeout = 5 * time.Second
	DefaultMessageHookEventTaskTimeout    = 10 * time.Second
)

// NormalizeMessageHookExecutionPolicy resolves compatibility defaults and
// rejects invalid execution contracts. Priority only supplies the historical
// default failure behavior when a caller did not declare one explicitly.
func NormalizeMessageHookExecutionPolicy(
	priority int,
	lane MessageHookLane,
	policy MessageHookExecutionPolicy,
) (MessageHookExecutionPolicy, error) {
	if lane != MessageHookDecision && lane != MessageHookEvent {
		return MessageHookExecutionPolicy{}, fmt.Errorf("invalid message hook lane %d", lane)
	}

	if policy.FailurePolicy == MessageHookFailureDefault {
		if priority <= 10 {
			policy.FailurePolicy = MessageHookFailClosed
		} else {
			policy.FailurePolicy = MessageHookFailOpen
		}
	}
	if policy.FailurePolicy != MessageHookFailOpen && policy.FailurePolicy != MessageHookFailClosed {
		return MessageHookExecutionPolicy{}, fmt.Errorf("invalid message hook failure policy %d", policy.FailurePolicy)
	}

	if policy.HandlerTimeout == 0 {
		policy.HandlerTimeout = DefaultMessageHookHandlerTimeout
	}
	if policy.HandlerTimeout < 0 {
		return MessageHookExecutionPolicy{}, fmt.Errorf("message hook handler timeout must be positive")
	}

	if policy.TaskTimeout == 0 {
		if lane == MessageHookDecision {
			policy.TaskTimeout = DefaultMessageHookDecisionTaskTimeout
		} else {
			policy.TaskTimeout = DefaultMessageHookEventTaskTimeout
		}
	}
	if policy.TaskTimeout < 0 {
		return MessageHookExecutionPolicy{}, fmt.Errorf("message hook task timeout must be positive")
	}
	if policy.HandlerTimeout > policy.TaskTimeout {
		return MessageHookExecutionPolicy{}, fmt.Errorf(
			"message hook handler timeout %s exceeds task timeout %s",
			policy.HandlerTimeout,
			policy.TaskTimeout,
		)
	}

	if policy.Ordering == MessageHookOrderingDefault {
		if lane == MessageHookDecision {
			policy.Ordering = MessageHookOrderingChat
		} else {
			policy.Ordering = MessageHookOrderingPluginChat
		}
	}
	if policy.Ordering != MessageHookOrderingChat && policy.Ordering != MessageHookOrderingPluginChat {
		return MessageHookExecutionPolicy{}, fmt.Errorf("invalid message hook ordering policy %d", policy.Ordering)
	}

	return policy, nil
}

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
