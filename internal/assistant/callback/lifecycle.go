package callback

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
)

// TransactionState represents the lifecycle stage of a callback transaction.
type TransactionState uint32

const (
	// StateReceived marks a freshly parsed callback query.
	StateReceived TransactionState = iota
	// StateAnswered marks a query that has received its MTProto acknowledgement.
	StateAnswered
	// StateExecuting marks a transaction currently executing a business handler.
	StateExecuting
	// StateCompleted marks a successfully finalized transaction.
	StateCompleted
	// StateFailed marks a transaction that ended with an unrecovered failure.
	StateFailed
)

// String returns a human-readable representation of TransactionState.
func (s TransactionState) String() string {
	switch s {
	case StateReceived:
		return "received"
	case StateAnswered:
		return "answered"
	case StateExecuting:
		return "executing"
	case StateCompleted:
		return "completed"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Transaction encapsulates the context, target, and lifecycle of an incoming callback query.
type Transaction struct {
	QueryID     int64
	UserID      int64
	Payload     ParsedPayload
	Target      interaction.MessageTarget
	Interaction interaction.MessageInteraction

	answerOnce sync.Once
	answerErr  error
	state      uint32
}

// NewTransaction creates an initialized callback transaction in StateReceived.
func NewTransaction(queryID int64, userID int64, payload ParsedPayload, target interaction.MessageTarget, inter interaction.MessageInteraction) *Transaction {
	return &Transaction{
		QueryID:     queryID,
		UserID:      userID,
		Payload:     payload,
		Target:      target,
		Interaction: inter,
		state:       uint32(StateReceived),
	}
}

// State returns the current lifecycle state.
func (t *Transaction) State() TransactionState {
	return TransactionState(atomic.LoadUint32(&t.state))
}

// SetState updates the lifecycle state atomically.
func (t *Transaction) SetState(s TransactionState) {
	atomic.StoreUint32(&t.state, uint32(s))
}

// IsAnswered reports whether the callback query has been answered.
func (t *Transaction) IsAnswered() bool {
	st := t.State()
	return st == StateAnswered || st == StateExecuting || st == StateCompleted
}

// Answer sends an acknowledgement or alert to Telegram.
// It is single-flight and guaranteed to execute RPC answering at most once.
func (t *Transaction) Answer(ctx context.Context, text string, alert bool) error {
	t.answerOnce.Do(func() {
		if t.Interaction != nil && t.QueryID != 0 {
			t.answerErr = t.Interaction.Answer(ctx, t.QueryID, text, alert)
		}
		if t.answerErr == nil {
			t.SetState(StateAnswered)
		}
	})
	return t.answerErr
}

// Edit mutates the text and reply markup of the message that triggered this callback.
func (t *Transaction) Edit(ctx context.Context, text string, markup tg.ReplyMarkupClass) error {
	if t.Interaction == nil {
		return interaction.ErrInvalidTarget
	}
	return t.Interaction.Edit(ctx, t.Target, text, markup)
}

// Delete removes the message that triggered this callback using idempotent delete semantics.
func (t *Transaction) Delete(ctx context.Context) error {
	if t.Interaction == nil {
		return interaction.ErrInvalidTarget
	}
	return t.Interaction.Delete(ctx, t.Target)
}
