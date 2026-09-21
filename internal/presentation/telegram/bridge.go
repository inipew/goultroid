package telegram

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation"
)

var ErrInvalidTarget = errors.New("presentation/telegram: invalid target")

type MessageTarget struct {
	Peer      tg.InputPeerClass
	ChatID    int64
	MessageID int
}

func (MessageTarget) PresentationTargetKind() string { return "message" }

type InlineTarget struct {
	MessageID tg.InputBotInlineMessageIDClass
}

func (InlineTarget) PresentationTargetKind() string { return "inline" }

type Bridge struct {
	Service core.TelegramServicer
}

var _ presentation.Port = (*Bridge)(nil)

func NewBridge(service core.TelegramServicer) *Bridge {
	return &Bridge{Service: service}
}

func (b *Bridge) Send(ctx context.Context, target presentation.Target, view presentation.CompiledView) (presentation.Target, error) {
	if b == nil || b.Service == nil {
		return nil, core.ErrUnavailable
	}
	messageTarget, ok := target.(MessageTarget)
	if !ok || messageTarget.Peer == nil {
		return nil, ErrInvalidTarget
	}
	msg, err := b.Service.SendMessageWithMarkup(ctx, messageTarget.Peer, view.Text, markup(view))
	if err != nil {
		return nil, err
	}
	if msg == nil || msg.ID <= 0 {
		return nil, fmt.Errorf("%w: send returned no message id", ErrInvalidTarget)
	}
	messageTarget.MessageID = msg.ID
	return messageTarget, nil
}

func (b *Bridge) Edit(ctx context.Context, target presentation.Target, view presentation.CompiledView) error {
	if b == nil || b.Service == nil {
		return core.ErrUnavailable
	}
	switch t := target.(type) {
	case MessageTarget:
		if t.Peer == nil || t.MessageID <= 0 {
			return ErrInvalidTarget
		}
		return b.Service.EditMessageMarkup(ctx, t.Peer, t.MessageID, view.Text, markup(view))
	case InlineTarget:
		if t.MessageID == nil {
			return ErrInvalidTarget
		}
		return b.Service.EditInlineBotMessage(ctx, t.MessageID, view.Text, markup(view))
	default:
		return ErrInvalidTarget
	}
}

func (b *Bridge) Answer(ctx context.Context, answer presentation.Answer) error {
	if b == nil || b.Service == nil {
		return core.ErrUnavailable
	}
	if answer.QueryID == 0 {
		return ErrInvalidTarget
	}
	return b.Service.AnswerCallbackQuery(ctx, answer.QueryID, answer.Text, answer.Alert)
}

func markup(view presentation.CompiledView) tg.ReplyMarkupClass {
	if len(view.Rows) == 0 {
		return nil
	}
	rows := make([]tg.KeyboardButtonRow, 0, len(view.Rows))
	for _, row := range view.Rows {
		buttons := make([]tg.KeyboardButtonClass, 0, len(row))
		for _, button := range row {
			buttons = append(buttons, &tg.KeyboardButtonCallback{Text: button.Text, Data: append([]byte(nil), button.Data...)})
		}
		if len(buttons) > 0 {
			rows = append(rows, tg.KeyboardButtonRow{Buttons: buttons})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return &tg.ReplyInlineMarkup{Rows: rows}
}
