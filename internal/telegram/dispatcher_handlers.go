package telegram

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

// HandlerPriority determines the execution order of raw message handlers.
type HandlerPriority = int

const (
	PrioritySecurity      HandlerPriority = 10 // PMPermit, Blacklist, Access Control
	PriorityModeration    HandlerPriority = 20 // Filters, Auto-Moderation, Anti-Flood
	PriorityFeature       HandlerPriority = 50 // AFK, Custom Handlers, Feature Plugins
	PriorityObservability HandlerPriority = 90 // UserLog, Analytics, Auditing
)

type HandlerFailurePolicy uint8

const (
	FailurePolicyFailOpen HandlerFailurePolicy = iota
	FailurePolicyFailClosed
)

func failurePolicyForPriority(priority HandlerPriority) HandlerFailurePolicy {
	if priority <= PrioritySecurity {
		return FailurePolicyFailClosed
	}
	return FailurePolicyFailOpen
}

type prioritizedHandler struct {
	id            uint64
	priority      HandlerPriority
	failurePolicy HandlerFailurePolicy
	routing       core.MessageHookRouting
	stateGate     func(int64) bool
	handler       MessageHandler
	scope         tasks.ScopeIdentity
}

const messageRouteClassCount = 128

type messageHandlerBucket struct {
	decision []prioritizedHandler
	event    []prioritizedHandler
}

// messageHandlerIndex is immutable after publication through Dispatcher.
// Registration/removal rebuilds it under d.mu; ingress only performs one atomic
// load and one fixed-array lookup.
type messageHandlerIndex struct {
	buckets [messageRouteClassCount]messageHandlerBucket
}

// MessageHandler is invoked for each incoming message.
type MessageHandler = func(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error

// AddMessageHandler registers a compatibility interceptor with default
// PriorityFeature. Unscoped feature handlers remain in the decision lane to
// preserve the historical synchronous test/local-handler contract.
func (d *Dispatcher) AddMessageHandler(h MessageHandler) {
	_ = d.AddPrioritizedMessageHandler(PriorityFeature, h)
}

// AddPrioritizedMessageHandler registers an interceptor with legacy routing.
func (d *Dispatcher) AddPrioritizedMessageHandler(priority HandlerPriority, h MessageHandler) func() {
	return d.AddScopedMessageHandler(priority, tasks.ScopeIdentity{}, h)
}

// AddPrioritizedMessageHandlerWithRouting registers an unscoped interceptor
// with explicit lane and structural interests.
func (d *Dispatcher) AddPrioritizedMessageHandlerWithRouting(priority HandlerPriority, routing core.MessageHookRouting, h MessageHandler) func() {
	return d.addMessageHandler(priority, tasks.ScopeIdentity{}, routing, nil, h)
}

// AddPrioritizedMessageHandlerWithRoutingAndState registers an unscoped
// interceptor with structural routing plus a dynamic chat-state gate.
func (d *Dispatcher) AddPrioritizedMessageHandlerWithRoutingAndState(priority HandlerPriority, routing core.MessageHookRouting, stateGate func(int64) bool, h MessageHandler) func() {
	return d.addMessageHandler(priority, tasks.ScopeIdentity{}, routing, stateGate, h)
}

// AddScopedMessageHandler registers a plugin-owned handler with routing derived
// from the legacy priority/scope convention.
func (d *Dispatcher) AddScopedMessageHandler(priority HandlerPriority, scope tasks.ScopeIdentity, h MessageHandler) func() {
	return d.addMessageHandler(priority, scope, legacyMessageHookRouting(priority, scope), nil, h)
}

// AddScopedMessageHandlerWithRouting registers a plugin-owned handler with
// explicit decision/event lane and indexed interests.
func (d *Dispatcher) AddScopedMessageHandlerWithRouting(priority HandlerPriority, scope tasks.ScopeIdentity, routing core.MessageHookRouting, h MessageHandler) func() {
	return d.addMessageHandler(priority, scope, routing, nil, h)
}

// AddScopedMessageHandlerWithRoutingAndState registers a plugin-owned handler
// with structural routing plus a dynamic chat-state gate.
func (d *Dispatcher) AddScopedMessageHandlerWithRoutingAndState(priority HandlerPriority, scope tasks.ScopeIdentity, routing core.MessageHookRouting, stateGate func(int64) bool, h MessageHandler) func() {
	return d.addMessageHandler(priority, scope, routing, stateGate, h)
}

func (d *Dispatcher) addMessageHandler(priority HandlerPriority, scope tasks.ScopeIdentity, routing core.MessageHookRouting, stateGate func(int64) bool, h MessageHandler) func() {
	if h == nil {
		return func() {}
	}
	d.mu.Lock()
	d.nextHandlerID++
	id := d.nextHandlerID
	d.messageHandlers = append(d.messageHandlers, prioritizedHandler{
		id: id, priority: priority, failurePolicy: failurePolicyForPriority(priority), routing: routing, stateGate: stateGate, handler: h, scope: scope,
	})
	sort.SliceStable(d.messageHandlers, func(i, j int) bool {
		return d.messageHandlers[i].priority < d.messageHandlers[j].priority
	})
	d.rebuildMessageHandlerIndexLocked()
	d.mu.Unlock()

	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		for i, ph := range d.messageHandlers {
			if ph.id == id {
				d.messageHandlers = append(d.messageHandlers[:i], d.messageHandlers[i+1:]...)
				d.rebuildMessageHandlerIndexLocked()
				break
			}
		}
	}
}

func legacyMessageHookRouting(priority HandlerPriority, scope tasks.ScopeIdentity) core.MessageHookRouting {
	lane := core.MessageHookDecision
	if priority >= PriorityObservability || (priority >= PriorityFeature && !scope.IsZero()) {
		lane = core.MessageHookEvent
	}
	return core.MessageHookRouting{Lane: lane}
}

func (d *Dispatcher) rebuildMessageHandlerIndexLocked() {
	idx := &messageHandlerIndex{}
	for _, registered := range d.messageHandlers {
		for class := 0; class < messageRouteClassCount; class++ {
			if !messageHookRoutingMatchesClass(registered.routing, uint8(class)) {
				continue
			}
			bucket := &idx.buckets[class]
			if registered.routing.Lane == core.MessageHookEvent {
				bucket.event = append(bucket.event, registered)
			} else {
				bucket.decision = append(bucket.decision, registered)
			}
		}
	}
	d.messageRouteIndex.Store(idx)
}

func (d *Dispatcher) messageHandlersFor(msg *tg.Message, isCommand bool) (decision, event []prioritizedHandler) {
	idx := d.messageRouteIndex.Load()
	if idx == nil {
		return nil, nil
	}
	class := classifyMessageRoute(msg, isCommand)
	bucket := &idx.buckets[class]
	return bucket.decision, bucket.event
}

func classifyMessageRoute(msg *tg.Message, isCommand bool) uint8 {
	if msg == nil {
		return 0
	}
	var class uint8
	if msg.Out {
		class |= 1 << 0
	}
	class |= messagePeerClass(msg.PeerID) << 1
	if isCommand {
		class |= 1 << 3
	}
	if msg.Message != "" {
		class |= 1 << 4
	}
	if messageMentionCandidate(msg) {
		class |= 1 << 5
	}
	if msg.ReplyTo != nil {
		class |= 1 << 6
	}
	return class
}

func messagePeerClass(peer tg.PeerClass) uint8 {
	switch peer.(type) {
	case *tg.PeerUser:
		return 1
	case *tg.PeerChat:
		return 2
	case *tg.PeerChannel:
		return 3
	default:
		return 0
	}
}

func messageMentionCandidate(msg *tg.Message) bool {
	if msg == nil {
		return false
	}
	if msg.Mentioned {
		return true
	}
	for _, entity := range msg.Entities {
		switch entity.(type) {
		case *tg.MessageEntityMention, *tg.MessageEntityMentionName:
			return true
		}
	}
	return false
}

func messageHookRoutingMatchesClass(routing core.MessageHookRouting, class uint8) bool {
	if len(routing.Interests) == 0 {
		return true
	}
	for _, interest := range routing.Interests {
		if messageHookInterestMatchesClass(interest, class) {
			return true
		}
	}
	return false
}

func messageHookInterestMatchesClass(interest core.MessageHookInterest, class uint8) bool {
	directions := interest.Directions
	if directions == 0 {
		directions = core.MessageDirectionAny
	}
	if class&(1<<0) != 0 {
		if directions&core.MessageDirectionOutgoing == 0 {
			return false
		}
	} else if directions&core.MessageDirectionIncoming == 0 {
		return false
	}

	peers := interest.Peers
	if peers == 0 {
		peers = core.MessagePeerAny
	}
	var peerMask core.MessagePeerMask
	switch (class >> 1) & 0x3 {
	case 1:
		peerMask = core.MessagePeerPrivate
	case 2:
		peerMask = core.MessagePeerGroup
	case 3:
		peerMask = core.MessagePeerChannel
	default:
		peerMask = core.MessagePeerUnknown
	}
	if peers&peerMask == 0 {
		return false
	}

	commands := interest.Commands
	if commands == 0 {
		commands = core.MessageCommandAny
	}
	if class&(1<<3) != 0 {
		if commands&core.MessageCommand == 0 {
			return false
		}
	} else if commands&core.MessagePlain == 0 {
		return false
	}

	if interest.RequireText && class&(1<<4) == 0 {
		return false
	}
	if interest.RequireMention && class&(1<<5) == 0 {
		return false
	}
	if interest.RequireReply && class&(1<<6) == 0 {
		return false
	}
	return true
}

func (d *Dispatcher) safeExecuteInterceptor(
	ctx context.Context,
	h MessageHandler,
	e tg.Entities,
	msg *tg.Message,
	isCmd bool,
	cmdName string,
	policy HandlerFailurePolicy,
) (handled bool) {
	failClosed := policy == FailurePolicyFailClosed
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("message interceptor panicked",
				zap.Any("panic", r),
				zap.Bool("fail_closed", failClosed),
			)
			handled = failClosed
		}
	}()

	interceptorCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := h(interceptorCtx, e, msg, isCmd, cmdName); err != nil {
		if errors.Is(err, core.ErrInterceptHandled) {
			return true
		}
		d.logger.Warn("message interceptor returned error",
			zap.Error(err),
			zap.Bool("fail_closed", failClosed),
		)
		return failClosed
	}
	return false
}
