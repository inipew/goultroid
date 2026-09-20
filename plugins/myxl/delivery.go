package myxl

import (
	"errors"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

const myxlTelegramMessageRunes = 4096

func deliverHTML(ctx *core.Context, text string) error {
	return deliverHTMLWithMarkup(ctx, text, nil)
}

func deliverHTMLWithMarkup(ctx *core.Context, text string, markup tg.ReplyMarkupClass) error {
	if ctx == nil {
		return errors.New("myxl: context is nil")
	}
	chunks := core.SplitTelegramHTML(text, myxlTelegramMessageRunes)
	if len(chunks) == 0 {
		return nil
	}
	if len(chunks) == 1 {
		if markup != nil {
			return ctx.Messages().ReplyMarkup(chunks[0], markup)
		}
		return ctx.EditOrReply(chunks[0])
	}

	if err := ctx.EditOrReply(chunks[0]); err != nil {
		return err
	}
	for i := 1; i < len(chunks)-1; i++ {
		if err := ctx.Reply(chunks[i]); err != nil {
			return err
		}
	}
	last := chunks[len(chunks)-1]
	if markup != nil {
		return ctx.Messages().ReplyMarkup(last, markup)
	}
	return ctx.Reply(last)
}
