package core

import (
	"errors"
	"fmt"
)

var (
	// ErrEditDeliveryUnconfirmed means the edit RPC was attempted but Telegram's
	// response did not confirm whether the mutation committed.
	ErrEditDeliveryUnconfirmed = errors.New("message edit delivery could not be confirmed")
	// ErrEditPostCommit means Telegram confirmed the edit, but a local follow-up
	// action (for example delayed deletion scheduling) failed afterwards.
	ErrEditPostCommit = errors.New("message edit committed but post-edit action failed")
)

// MessageEditStage identifies how far an edit operation progressed.
type MessageEditStage uint8

const (
	MessageEditStageUnknown MessageEditStage = iota
	MessageEditStagePreflight
	MessageEditStageSend
	MessageEditStagePostCommit
)

// MessageEditFailure preserves the root cause while making duplicate-delivery
// safety explicit to callers.
type MessageEditFailure struct {
	Stage            MessageEditStage
	MayHaveCommitted bool
	Err              error
}

func (e *MessageEditFailure) Error() string {
	if e == nil {
		return "message edit failed"
	}
	switch e.Stage {
	case MessageEditStageSend:
		return ErrEditDeliveryUnconfirmed.Error()
	case MessageEditStagePostCommit:
		return ErrEditPostCommit.Error()
	default:
		if e.Err != nil {
			return e.Err.Error()
		}
		return "message edit preflight failed"
	}
}

func (e *MessageEditFailure) Unwrap() []error {
	if e == nil {
		return nil
	}
	out := make([]error, 0, 2)
	switch e.Stage {
	case MessageEditStageSend:
		out = append(out, ErrEditDeliveryUnconfirmed)
	case MessageEditStagePostCommit:
		out = append(out, ErrEditPostCommit)
	}
	if e.Err != nil {
		out = append(out, e.Err)
	}
	return out
}

// MessageEditFailureStage extracts the stage-aware edit contract.
func MessageEditFailureStage(err error) MessageEditStage {
	var failure *MessageEditFailure
	if !errors.As(err, &failure) || failure == nil {
		return MessageEditStageUnknown
	}
	return failure.Stage
}

// MessageEditMayHaveCommitted reports whether automatic alternate delivery could
// duplicate an edit that Telegram may already have applied.
func MessageEditMayHaveCommitted(err error) bool {
	var failure *MessageEditFailure
	return errors.As(err, &failure) && failure != nil && failure.MayHaveCommitted
}

// MessageEditFallbackSafe is true only for failures before an edit RPC was sent.
func MessageEditFallbackSafe(err error) bool {
	var failure *MessageEditFailure
	return errors.As(err, &failure) &&
		failure != nil &&
		failure.Stage == MessageEditStagePreflight &&
		!failure.MayHaveCommitted
}

func messageEditFailure(stage MessageEditStage, mayHaveCommitted bool, err error) error {
	if err == nil {
		return nil
	}
	return &MessageEditFailure{
		Stage:            stage,
		MayHaveCommitted: mayHaveCommitted,
		Err:              err,
	}
}

func editableResponseAnchor(c *Context) (msgID int, outgoingTrigger bool, ok bool) {
	if c == nil {
		return 0, false, false
	}
	if c.LastResponseID > 0 {
		return c.LastResponseID, false, true
	}
	if c.Message != nil && c.Message.IsOutgoing && c.Message.ID > 0 {
		return c.Message.ID, true, true
	}
	return 0, false, false
}

func (m *MessagesFacade) editMessageID(text string, msgID int) error {
	c := m.ctx
	if c == nil {
		return messageEditFailure(MessageEditStagePreflight, false, errors.New("context is nil"))
	}
	if c.Svc == nil {
		return messageEditFailure(MessageEditStagePreflight, false, errors.New("telegram service not initialized"))
	}
	if c.PeerID == nil {
		return messageEditFailure(MessageEditStagePreflight, false, errors.New("peer is nil"))
	}
	if msgID <= 0 {
		return messageEditFailure(MessageEditStagePreflight, false, errors.New("no message to edit"))
	}
	if err := c.Svc.EditMessage(c.Ctx, c.PeerID, msgID, text); err != nil {
		return messageEditFailure(
			MessageEditStageSend,
			true,
			fmt.Errorf("edit message %d: %w", msgID, err),
		)
	}
	return nil
}
