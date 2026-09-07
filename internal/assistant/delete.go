package assistant

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// deleteCallbackMessage removes the message that owns an assistant callback.
// It uses the peer-aware MTProto deletion method for channels and revokes the
// message for all participants in user/basic-group dialogs.
func (a *BotServiceAdapter) deleteCallbackMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	if a == nil || a.api == nil {
		return core.ErrInternal
	}
	if peer == nil || msgID == 0 {
		return fmt.Errorf("assistant bot delete callback message: invalid target: %w", core.ErrInternal)
	}

	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		_, err := a.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash},
			ID:      []int{msgID},
		})
		if err != nil {
			return fmt.Errorf("assistant bot delete callback message (channel): %w", err)
		}
		return nil
	case *tg.InputPeerUser, *tg.InputPeerChat, *tg.InputPeerSelf:
		req := &tg.MessagesDeleteMessagesRequest{ID: []int{msgID}}
		req.SetRevoke(true)
		if _, err := a.api.MessagesDeleteMessages(ctx, req); err != nil {
			return fmt.Errorf("assistant bot delete callback message: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("assistant bot delete callback message: unsupported peer %T: %w", peer, core.ErrUnsupported)
	}
}
