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
	// StateValidated marks a transaction that has passed payload schema validation.
	StateValidated
	// StateAuthorized marks a transaction that has passed authorization check.
	StateAuthorized
	// StateAnswering marks a query currently undergoing RPC answer transmission.
	StateAnswering
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
	case StateValidated:
		return "validated"
	case StateAuthorized:
		return "authorized"
	case StateAnswering:
		return "answering"
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
	answered   uint32
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

// Transition attempts to move to a new state if valid according to the lifecycle DAG.
func (t *Transaction) Transition(target TransactionState) bool {
	for {
		current := t.State()
		if current == StateFailed && target != StateFailed {
			return false
		}
		if current == StateCompleted {
			return false
		}
		// Prevent backwards transition / regression in the lifecycle DAG
		if target < current && target != StateFailed {
			return false
		}
		if atomic.CompareAndSwapUint32(&t.state, uint32(current), uint32(target)) {
			return true
		}
	}
}

// IsAnswered reports whether the callback query has been answered.
func (t *Transaction) IsAnswered() bool {
	return atomic.LoadUint32(&t.answered) == 1
}

// Answer sends an acknowledgement or alert to Telegram.
// It is single-flight and guaranteed to execute RPC answering at most once.
// Subsequent calls return interaction.ErrCallbackAlreadyAnswered.
func (t *Transaction) Answer(ctx context.Context, text string, alert bool) error {
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
