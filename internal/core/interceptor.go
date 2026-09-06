package core

import (
	"context"
	"sync"
	"time"
)

// InterceptDecision controls whether message processing continues after an interceptor.
type InterceptDecision uint8

const (
	InterceptContinue InterceptDecision = iota
	InterceptHandled
)

type handledMessageKey struct {
	chatID    int64
	messageID int
}
type handledMessageEntry struct {
	expiresAt time.Time
}

var handledMessages sync.Map

func MarkMessageHandled(chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	handledMessages.Store(handledMessageKey{chatID: chatID, messageID: messageID}, handledMessageEntry{expiresAt: time.Now().Add(30 * time.Second)})
}

func ConsumeMessageHandled(chatID int64, messageID int) bool {
	if messageID == 0 {
		return false
	}
	key := handledMessageKey{chatID: chatID, messageID: messageID}
	value, ok := handledMessages.LoadAndDelete(key)
	if !ok {
		return false
	}
	entry, ok := value.(handledMessageEntry)
	return ok && time.Now().Before(entry.expiresAt)
}

// MessageDecision coordinates automation arbitration and suppression flags across message interceptors.
type MessageDecision struct {
	mu                 sync.RWMutex
	handled            bool
	suppressAutomation bool
	suppressAFK        bool
	suppressFilters    bool
	suppressCommands   bool
	origin             ExecutionSource
}

func NewMessageDecision(origin ExecutionSource) *MessageDecision {
	return &MessageDecision{origin: origin}
}

func (d *MessageDecision) SetHandled(v bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.handled = v
	d.mu.Unlock()
}

func (d *MessageDecision) IsHandled() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.handled
}

func (d *MessageDecision) SetSuppressAutomation(v bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.suppressAutomation = v
	d.mu.Unlock()
}

func (d *MessageDecision) IsSuppressedAutomation() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.suppressAutomation
}

func (d *MessageDecision) SetSuppressAFK(v bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.suppressAFK = v
	d.mu.Unlock()
}

func (d *MessageDecision) IsSuppressedAFK() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.suppressAFK
}

func (d *MessageDecision) SetSuppressFilters(v bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.suppressFilters = v
	d.mu.Unlock()
}

func (d *MessageDecision) IsSuppressedFilters() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.suppressFilters
}

func (d *MessageDecision) SetSuppressCommands(v bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.suppressCommands = v
	d.mu.Unlock()
}

func (d *MessageDecision) IsSuppressedCommands() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.suppressCommands
}

func (d *MessageDecision) Origin() ExecutionSource {
	if d == nil {
		return ExecutionInteractive
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.origin
}

func (d *MessageDecision) SetOrigin(origin ExecutionSource) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.origin = origin
	d.mu.Unlock()
}

type messageDecisionCtxKey struct{}

// WithMessageDecision binds a MessageDecision to the context.
func WithMessageDecision(ctx context.Context, d *MessageDecision) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, messageDecisionCtxKey{}, d)
}

// GetMessageDecision retrieves the MessageDecision from the context, or nil if not present.
func GetMessageDecision(ctx context.Context) *MessageDecision {
	if ctx == nil {
		return nil
	}
	if d, ok := ctx.Value(messageDecisionCtxKey{}).(*MessageDecision); ok {
		return d
	}
	return nil
}
