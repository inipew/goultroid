package command

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
)

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
