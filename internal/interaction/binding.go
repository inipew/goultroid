package interaction

import (
	"fmt"
	"strings"
)

type TargetBinding struct {
	ChatID          int64
	MessageID       int
	InlineMessageID string
}

func (t TargetBinding) IsZero() bool {
	return t.ChatID == 0 && t.MessageID == 0 && strings.TrimSpace(t.InlineMessageID) == ""
}

func (t TargetBinding) validateComplete() error {
	inlineID := strings.TrimSpace(t.InlineMessageID)
	if inlineID != "" {
		if t.ChatID != 0 || t.MessageID != 0 {
			return fmt.Errorf("%w: inline target cannot also carry chat/message ids", ErrInvalidBinding)
		}
		return nil
	}
	if t.ChatID == 0 || t.MessageID <= 0 {
		return fmt.Errorf("%w: message target requires chat and positive message id", ErrInvalidBinding)
	}
	return nil
}

// Binding optionally constrains a session to an actor and/or message target.
// Zero fields are unbound dimensions; at least one dimension must be bound.
type Binding struct {
	ActorID         int64
	ChatID          int64
	MessageID       int
	InlineMessageID string
}

func (b Binding) normalized() Binding {
	b.InlineMessageID = strings.TrimSpace(b.InlineMessageID)
	return b
}

func (b Binding) Validate() error {
	inlineID := strings.TrimSpace(b.InlineMessageID)
	if inlineID != "" && (b.ChatID != 0 || b.MessageID != 0) {
		return fmt.Errorf("%w: inline binding cannot also carry chat/message ids", ErrInvalidBinding)
	}
	if b.MessageID < 0 {
		return fmt.Errorf("%w: message id cannot be negative", ErrInvalidBinding)
	}
	if b.MessageID > 0 && b.ChatID == 0 {
		return fmt.Errorf("%w: message id requires chat id", ErrInvalidBinding)
	}
	if b.ActorID == 0 && b.ChatID == 0 && b.MessageID == 0 && inlineID == "" {
		return fmt.Errorf("%w: session must bind actor or target", ErrInvalidBinding)
	}
	return nil
}

// Matches applies only the dimensions bound by the stored session.
func (b Binding) Matches(actual Binding) bool {
	if b.ActorID != 0 && actual.ActorID != b.ActorID {
		return false
	}
	if b.ChatID != 0 && actual.ChatID != b.ChatID {
		return false
	}
	if b.MessageID != 0 && actual.MessageID != b.MessageID {
		return false
	}
	if b.InlineMessageID != "" && actual.InlineMessageID != b.InlineMessageID {
		return false
	}
	return true
}

func (b Binding) withTarget(target TargetBinding) (Binding, error) {
	if err := target.validateComplete(); err != nil {
		return Binding{}, err
	}
	if b.InlineMessageID != "" {
		if b.InlineMessageID != target.InlineMessageID {
			return Binding{}, ErrTargetBound
		}
		return b, nil
	}
	if b.ChatID != 0 && target.ChatID != b.ChatID {
		return Binding{}, ErrTargetBound
	}
	if b.MessageID != 0 && target.MessageID != b.MessageID {
		return Binding{}, ErrTargetBound
	}
	if target.InlineMessageID != "" && (b.ChatID != 0 || b.MessageID != 0) {
		return Binding{}, ErrTargetBound
	}
	b.ChatID = target.ChatID
	b.MessageID = target.MessageID
	b.InlineMessageID = strings.TrimSpace(target.InlineMessageID)
	return b, nil
}
