package telegram

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// bridgeService is the presentation bridge's consumer-owned Telegram boundary.
// Contextual media delivery and inline media editing remain optional extension
// capabilities because not every bridge transport supports them.
type bridgeService interface {
	SendMessageWithMarkup(context.Context, tg.InputPeerClass, string, tg.ReplyMarkupClass) (*tg.Message, error)
	EditMessageMarkup(context.Context, tg.InputPeerClass, int, string, tg.ReplyMarkupClass) error
	EditInlineBotMessage(context.Context, tg.InputBotInlineMessageIDClass, string, tg.ReplyMarkupClass) error
	DeleteMessage(context.Context, tg.InputPeerClass, []int) error
	SendMedia(context.Context, tg.InputPeerClass, string, string, string) (*tg.Message, error)
	AnswerCallbackQuery(context.Context, int64, string, bool) error
}

// M1 defines the target consumer boundary without changing Bridge.Service yet.
// M2 will switch Bridge construction to this interface.
var _ bridgeService = (core.TelegramServicer)(nil)
