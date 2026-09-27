package inline

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// telegramAnswerer is the inline engine's consumer-owned Telegram boundary.
// Local-media preparation remains a separate optional capability.
type telegramAnswerer interface {
	AnswerInlineQuery(context.Context, int64, []tg.InputBotInlineResultClass, string, int) error
	AnswerInlineQueryOptions(context.Context, int64, []tg.InputBotInlineResultClass, core.InlineAnswerOptions) error
}

// M1 is additive: Execute still accepts the compatibility service until M2
// migrates its signature, but the required inline-answer surface is now fenced
// independently.
var _ telegramAnswerer = (core.TelegramServicer)(nil)
