package command

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
)

// ErrGroupMutationUnavailable fences Assistant group mutations until the P7
// manager transport has authoritative actor/bot rights revalidation and managed
// Telegram RPC wiring. It prevents the embedded mock fallback from reporting a
// mutation as successful when no Telegram operation occurred.
var ErrGroupMutationUnavailable = fmt.Errorf("%w: assistant group mutation transport is not configured", core.ErrUnavailable)

// assistantServicerAdapter adapts Assistant MessageInteraction into core.TelegramServicer
// so plugin command handlers can transparently use ctx.Reply, ctx.EditOrReply, and ctx.ReplyMarkup.
type assistantServicerAdapter struct {
	core.MockTelegramServicer
	inter interaction.MessageInteraction
}

func (a *assistantServicerAdapter) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if a.inter != nil {
		return a.inter.SendMessage(ctx, peer, text, nil)
	}
	return a.MockTelegramServicer.SendMessage(ctx, peer, text)
}

func (a *assistantServicerAdapter) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if a.inter != nil {
		return a.inter.SendMessage(ctx, peer, text, markup)
	}
	return a.MockTelegramServicer.SendMessageWithMarkup(ctx, peer, text, markup)
}

func (a *assistantServicerAdapter) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	if a.inter != nil {
		return a.inter.SendMedia(ctx, peer, mediaType, filePath, caption)
	}
	return a.MockTelegramServicer.SendMedia(ctx, peer, mediaType, filePath, caption)
}

func (a *assistantServicerAdapter) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	if a.inter != nil {
		chatID := extractChatIDFromInputPeer(peer)
		return a.inter.Edit(ctx, interaction.NewMessageTarget(peer, msgID, chatID, 0), text, nil)
	}
	return a.MockTelegramServicer.EditMessage(ctx, peer, msgID, text)
}

func (a *assistantServicerAdapter) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	if a.inter != nil {
		chatID := extractChatIDFromInputPeer(peer)
		return a.inter.Edit(ctx, interaction.NewMessageTarget(peer, msgID, chatID, 0), text, markup)
	}
	return a.MockTelegramServicer.EditMessageMarkup(ctx, peer, msgID, text, markup)
}

func (a *assistantServicerAdapter) EditMessageMarkupOnly(ctx context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error {
	if a.inter != nil {
		chatID := extractChatIDFromInputPeer(peer)
		return a.inter.EditMarkup(ctx, interaction.NewMessageTarget(peer, msgID, chatID, 0), markup)
	}
	return a.MockTelegramServicer.EditMessageMarkupOnly(ctx, peer, msgID, markup)
}

func (a *assistantServicerAdapter) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	if a.inter != nil {
		chatID := extractChatIDFromInputPeer(peer)
		for _, id := range msgIDs {
			_ = a.inter.Delete(ctx, interaction.NewMessageTarget(peer, id, chatID, 0))
		}
		return nil
	}
	return a.MockTelegramServicer.DeleteMessage(ctx, peer, msgIDs)
}

func (a *assistantServicerAdapter) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	if a.inter != nil {
		chatID := extractChatIDFromInputPeer(peer)
		return a.inter.GetMessage(ctx, interaction.NewMessageTarget(peer, msgID, chatID, 0))
	}
	return a.MockTelegramServicer.GetMessage(ctx, peer, msgID)
}


func (*assistantServicerAdapter) PinMessage(context.Context, tg.InputPeerClass, int, bool) error {
	return ErrGroupMutationUnavailable
}

func (*assistantServicerAdapter) UnpinMessage(context.Context, tg.InputPeerClass, int) error {
	return ErrGroupMutationUnavailable
}

func (*assistantServicerAdapter) BanUser(context.Context, tg.InputPeerClass, tg.InputPeerClass, int) error {
	return ErrGroupMutationUnavailable
}

func (*assistantServicerAdapter) UnbanUser(context.Context, tg.InputPeerClass, tg.InputPeerClass) error {
	return ErrGroupMutationUnavailable
}

func (*assistantServicerAdapter) KickUser(context.Context, tg.InputPeerClass, tg.InputPeerClass) error {
	return ErrGroupMutationUnavailable
}

func (*assistantServicerAdapter) MuteUser(context.Context, tg.InputPeerClass, tg.InputPeerClass, int) error {
	return ErrGroupMutationUnavailable
}

func (*assistantServicerAdapter) UnmuteUser(context.Context, tg.InputPeerClass, tg.InputPeerClass) error {
	return ErrGroupMutationUnavailable
}

func (*assistantServicerAdapter) PurgeMessages(context.Context, tg.InputPeerClass, int, int, int) (int, error) {
	return 0, ErrGroupMutationUnavailable
}

func (*assistantServicerAdapter) PromoteAdmin(context.Context, tg.InputPeerClass, tg.InputPeerClass, string) error {
	return ErrGroupMutationUnavailable
}

func (*assistantServicerAdapter) DemoteAdmin(context.Context, tg.InputPeerClass, tg.InputPeerClass) error {
	return ErrGroupMutationUnavailable
}

func (*assistantServicerAdapter) EditChatDefaultBannedRights(context.Context, tg.InputPeerClass, tg.ChatBannedRights) error {
	return ErrGroupMutationUnavailable
}
