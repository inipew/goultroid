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
	handler       MessageHandler
	scope         tasks.ScopeIdentity
}

// MessageHandler is invoked for each incoming message.
type MessageHandler = func(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error

// AddMessageHandler registers an interceptor for raw message processing with default PriorityFeature.
func (d *Dispatcher) AddMessageHandler(h MessageHandler) {
	_ = d.AddPrioritizedMessageHandler(PriorityFeature, h)
}

// AddPrioritizedMessageHandler registers an interceptor with an explicit priority.
func (d *Dispatcher) AddPrioritizedMessageHandler(priority HandlerPriority, h MessageHandler) func() {
	return d.AddScopedMessageHandler(priority, tasks.ScopeIdentity{}, h)
}

// AddScopedMessageHandler registers a handler owned by a plugin generation.
func (d *Dispatcher) AddScopedMessageHandler(priority HandlerPriority, scope tasks.ScopeIdentity, h MessageHandler) func() {
	if h == nil {
		return func() {}
	}
	d.mu.Lock()
	d.nextHandlerID++
	id := d.nextHandlerID
	d.messageHandlers = append(d.messageHandlers, prioritizedHandler{
		id: id, priority: priority, failurePolicy: failurePolicyForPriority(priority), handler: h, scope: scope,
	})
	sort.SliceStable(d.messageHandlers, func(i, j int) bool {
		return d.messageHandlers[i].priority < d.messageHandlers[j].priority
	})
	d.mu.Unlock()

	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		for i, ph := range d.messageHandlers {
			if ph.id == id {
				d.messageHandlers = append(d.messageHandlers[:i], d.messageHandlers[i+1:]...)
				break
			}
		}
	}
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
