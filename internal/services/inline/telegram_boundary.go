package inline

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// telegramAnswerer is the inline engine's consumer-owned Telegram boundary.
// Local-media preparation remains a separate optional capability.
type TelegramAnswerer interface {
	AnswerInlineQuery(context.Context, int64, []tg.InputBotInlineResultClass, string, int) error
	AnswerInlineQueryOptions(context.Context, int64, []tg.InputBotInlineResultClass, core.InlineAnswerOptions) error
}

// The legacy aggregate remains assignable during compatibility migration.
var _ TelegramAnswerer = (core.TelegramServicer)(nil)
