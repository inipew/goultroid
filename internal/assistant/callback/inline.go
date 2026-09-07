package callback

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
)

// InlineTransaction encapsulates the context, target, and lifecycle of an incoming inline bot callback query.
type InlineTransaction struct {
	QueryID     int64
	UserID      int64
	Payload     ParsedPayload
	Target      interaction.InlineTarget
	Interaction interaction.InlineInteraction

	answerOnce sync.Once
	answerErr  error
	answered   uint32
	state      uint32
}

// NewInlineTransaction creates an initialized inline callback transaction in StateReceived.
func NewInlineTransaction(queryID int64, userID int64, payload ParsedPayload, target interaction.InlineTarget, inter interaction.InlineInteraction) *InlineTransaction {
	return &InlineTransaction{
		QueryID:     queryID,
		UserID:      userID,
		Payload:     payload,
		Target:      target,
		Interaction: inter,
		state:       uint32(StateReceived),
	}
}

// State returns the current lifecycle state.
func (t *InlineTransaction) State() TransactionState {
	return TransactionState(atomic.LoadUint32(&t.state))
}

// SetState updates the lifecycle state atomically.
func (t *InlineTransaction) SetState(s TransactionState) {
	atomic.StoreUint32(&t.state, uint32(s))
}

// Transition attempts to move to a new state if valid according to the lifecycle DAG.
func (t *InlineTransaction) Transition(target TransactionState) bool {
	for {
		current := t.State()
		if current == StateFailed && target != StateFailed {
			return false
		}
		if current == StateCompleted {
			return false
		}
		if target < current && target != StateFailed {
			return false
		}
		if atomic.CompareAndSwapUint32(&t.state, uint32(current), uint32(target)) {
			return true
		}
	}
}

// IsAnswered reports whether the inline callback query has been answered.
func (t *InlineTransaction) IsAnswered() bool {
	return atomic.LoadUint32(&t.answered) == 1
}

// Answer sends an acknowledgement or alert to Telegram for the inline query.
// It is single-flight and guaranteed to execute RPC answering at most once.
// Subsequent calls return interaction.ErrCallbackAlreadyAnswered.
func (t *InlineTransaction) Answer(ctx context.Context, text string, alert bool) error {
	var firstCall bool
	t.answerOnce.Do(func() {
		firstCall = true
		atomic.StoreUint32(&t.answered, 1)
		_ = t.Transition(StateAnswering)
		if t.Interaction != nil && t.QueryID != 0 {
			t.answerErr = t.Interaction.Answer(ctx, t.QueryID, text, alert)
		}
		if t.answerErr == nil {
			_ = t.Transition(StateAnswered)
		}
	})
	if !firstCall {
		return interaction.ErrCallbackAlreadyAnswered
	}
	return t.answerErr
}

// Edit mutates the text and reply markup of the inline bot message.
func (t *InlineTransaction) Edit(ctx context.Context, text string, markup tg.ReplyMarkupClass) error {
	if t.Interaction == nil || !t.Target.IsValid() {
		return interaction.ErrInvalidTarget
	}
	return t.Interaction.Edit(ctx, t.Target, text, markup)
}

// Delete returns ErrUnsupportedTarget because Telegram does not support deleting inline bot messages.
func (t *InlineTransaction) Delete(ctx context.Context) error {
	return interaction.ErrUnsupportedTarget
}
