package telegram

import (
	"context"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation"
)

var _ presentation.Deleter = (*Bridge)(nil)

// Delete removes a concrete dialog message. Inline targets are intentionally
// unsupported because Telegram does not expose equivalent bot-message deletion
// semantics for inline-sent messages through this presentation bridge.
func (b *Bridge) Delete(ctx context.Context, target presentation.Target) error {
	if b == nil || b.Service == nil {
		return core.ErrUnavailable
	}
	messageTarget, ok := target.(MessageTarget)
	if !ok || messageTarget.Peer == nil || messageTarget.ChatID == 0 || messageTarget.MessageID <= 0 {
		return ErrInvalidTarget
	}
	return b.Service.DeleteMessage(ctx, messageTarget.Peer, []int{messageTarget.MessageID})
}
