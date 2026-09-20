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
	id               uint64
	priority         HandlerPriority
	failurePolicy    HandlerFailurePolicy
	routing          core.MessageHookRouting
	stateGate        func(int64) bool
	handler          MessageHandler
	canonicalHandler CanonicalMessageHandler
	scope            tasks.ScopeIdentity
}

const messageRouteClassCount = 256

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

// MessageHandler is the privileged raw Telegram compatibility hook.
type MessageHandler = func(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error

// CanonicalMessageHandler is the default plugin hook contract. It receives a
// lightweight normalized envelope and no raw Telegram entity container.
type CanonicalMessageHandler = func(ctx context.Context, message *core.MessageEnvelope) error

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
	return d.addMessageHandler(priority, tasks.ScopeIdentity{}, routing, nil, h, nil)
}

// AddPrioritizedCanonicalMessageHandlerWithRouting registers an unscoped
// canonical handler with explicit lane and structural interests.
func (d *Dispatcher) AddPrioritizedCanonicalMessageHandlerWithRouting(priority HandlerPriority, routing core.MessageHookRouting, h CanonicalMessageHandler) func() {
	return d.addMessageHandler(priority, tasks.ScopeIdentity{}, routing, nil, nil, h)
}

// AddPrioritizedMessageHandlerWithRoutingAndState registers an unscoped
// interceptor with structural routing plus a dynamic chat-state gate.
func (d *Dispatcher) AddPrioritizedMessageHandlerWithRoutingAndState(priority HandlerPriority, routing core.MessageHookRouting, stateGate func(int64) bool, h MessageHandler) func() {
	return d.addMessageHandler(priority, tasks.ScopeIdentity{}, routing, stateGate, h, nil)
}

// AddPrioritizedCanonicalMessageHandlerWithRoutingAndState registers an
// unscoped canonical handler with structural routing and a dynamic state gate.
func (d *Dispatcher) AddPrioritizedCanonicalMessageHandlerWithRoutingAndState(priority HandlerPriority, routing core.MessageHookRouting, stateGate func(int64) bool, h CanonicalMessageHandler) func() {
	return d.addMessageHandler(priority, tasks.ScopeIdentity{}, routing, stateGate, nil, h)
}

// AddScopedMessageHandler registers a plugin-owned handler with routing derived
// from the legacy priority/scope convention.
func (d *Dispatcher) AddScopedMessageHandler(priority HandlerPriority, scope tasks.ScopeIdentity, h MessageHandler) func() {
	return d.addMessageHandler(priority, scope, legacyMessageHookRouting(priority, scope), nil, h, nil)
}

// AddScopedMessageHandlerWithRouting registers a plugin-owned handler with
// explicit decision/event lane and indexed interests.
func (d *Dispatcher) AddScopedMessageHandlerWithRouting(priority HandlerPriority, scope tasks.ScopeIdentity, routing core.MessageHookRouting, h MessageHandler) func() {
	return d.addMessageHandler(priority, scope, routing, nil, h, nil)
}

// AddScopedCanonicalMessageHandlerWithRouting registers a plugin-owned
// canonical handler with explicit decision/event lane and indexed interests.
func (d *Dispatcher) AddScopedCanonicalMessageHandlerWithRouting(priority HandlerPriority, scope tasks.ScopeIdentity, routing core.MessageHookRouting, h CanonicalMessageHandler) func() {
	return d.addMessageHandler(priority, scope, routing, nil, nil, h)
}

// AddScopedMessageHandlerWithRoutingAndState registers a plugin-owned handler
// with structural routing plus a dynamic chat-state gate.
func (d *Dispatcher) AddScopedMessageHandlerWithRoutingAndState(priority HandlerPriority, scope tasks.ScopeIdentity, routing core.MessageHookRouting, stateGate func(int64) bool, h MessageHandler) func() {
	return d.addMessageHandler(priority, scope, routing, stateGate, h, nil)
}

// AddScopedCanonicalMessageHandlerWithRoutingAndState registers a plugin-owned
// canonical handler with structural routing and a dynamic chat-state gate.
func (d *Dispatcher) AddScopedCanonicalMessageHandlerWithRoutingAndState(priority HandlerPriority, scope tasks.ScopeIdentity, routing core.MessageHookRouting, stateGate func(int64) bool, h CanonicalMessageHandler) func() {
	return d.addMessageHandler(priority, scope, routing, stateGate, nil, h)
}

func (d *Dispatcher) addMessageHandler(priority HandlerPriority, scope tasks.ScopeIdentity, routing core.MessageHookRouting, stateGate func(int64) bool, h MessageHandler, canonical CanonicalMessageHandler) func() {
	if h == nil && canonical == nil {
		return func() {}
	}
	d.mu.Lock()
	d.nextHandlerID++
	id := d.nextHandlerID
	d.messageHandlers = append(d.messageHandlers, prioritizedHandler{
		id: id, priority: priority, failurePolicy: failurePolicyForPriority(priority), routing: routing, stateGate: stateGate, handler: h, canonicalHandler: canonical, scope: scope,
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
	if msg == nil {
		return nil, nil
	}
	return d.messageHandlersForClass(classifyMessageRoute(msg, isCommand))
}

func (d *Dispatcher) messageHandlersForEnvelope(message *core.MessageEnvelope) (decision, event []prioritizedHandler) {
	if message == nil {
		return nil, nil
	}
	return d.messageHandlersForClass(classifyCanonicalMessageRoute(message))
}

func (d *Dispatcher) messageHandlersForClass(class uint8) (decision, event []prioritizedHandler) {
	idx := d.messageRouteIndex.Load()
	if idx == nil {
		return nil, nil
	}
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
	if msg.Media != nil {
		class |= 1 << 7
	}
	return class
}

func classifyCanonicalMessageRoute(message *core.MessageEnvelope) uint8 {
	if message == nil {
		return 0
	}
	var class uint8
	if message.Outgoing {
		class |= 1 << 0
	}
	switch message.Chat.Type {
	case "private":
		class |= 1 << 1
	case "group":
		class |= 2 << 1
	case "supergroup", "channel":
		class |= 3 << 1
	}
	if message.IsCommand {
		class |= 1 << 3
	}
	if message.Text != "" {
		class |= 1 << 4
	}
	if message.Mentioned || len(message.Mentions) > 0 {
		class |= 1 << 5
	}
	if message.ReplyToID != 0 {
		class |= 1 << 6
	}
	if message.Media != nil {
		class |= 1 << 7
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
	if interest.RequireMedia && class&(1<<7) == 0 {
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
) bool {
	return d.safeExecuteMessageHook(ctx, policy, func(interceptorCtx context.Context) error {
		return h(interceptorCtx, e, msg, isCmd, cmdName)
	})
}

func (d *Dispatcher) safeExecuteRegisteredInterceptor(
	ctx context.Context,
	registered prioritizedHandler,
	e tg.Entities,
	msg *tg.Message,
	message *core.MessageEnvelope,
) bool {
	if registered.canonicalHandler != nil {
		return d.safeExecuteMessageHook(ctx, registered.failurePolicy, func(interceptorCtx context.Context) error {
			return registered.canonicalHandler(interceptorCtx, message)
		})
	}
	if registered.handler == nil {
		return false
	}
	isCmd, cmdName := false, ""
	if message != nil {
		isCmd = message.IsCommand
		cmdName = message.CommandName
	}
	return d.safeExecuteInterceptor(ctx, registered.handler, e, msg, isCmd, cmdName, registered.failurePolicy)
}

func (d *Dispatcher) safeExecuteMessageHook(
	ctx context.Context,
	policy HandlerFailurePolicy,
	run func(context.Context) error,
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

	if run == nil {
		return false
	}
	if err := run(interceptorCtx); err != nil {
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
