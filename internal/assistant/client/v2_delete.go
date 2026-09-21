package client

import (
	"context"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
)

func (s *v2PresentationServicer) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	if s == nil || s.interaction == nil || peer == nil {
		return ErrV2Unavailable
	}
	chatID := extractChatIDFromInputPeer(peer)
	if chatID == 0 {
		return ErrV2Unavailable
	}
	for _, msgID := range msgIDs {
		if msgID <= 0 {
			return ErrV2Unavailable
		}
		target := assistantinteraction.NewMessageTarget(peer, msgID, chatID, 0)
		if err := s.interaction.Delete(ctx, target); err != nil {
			return err
		}
	}
	return nil
}
