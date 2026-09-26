package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/presentation"
)

var ErrInvalidTarget = errors.New("presentation/telegram: invalid target")

type MessageTarget struct {
	Peer      tg.InputPeerClass
	ChatID    int64
	MessageID int
}

func (MessageTarget) PresentationTargetKind() string { return "message" }
func (t MessageTarget) SessionBinding(actorID int64) interaction.Binding {
	return interaction.Binding{ActorID: actorID, ChatID: t.ChatID, MessageID: t.MessageID}
}
func (t MessageTarget) TargetBinding() (interaction.TargetBinding, bool) {
	if t.ChatID == 0 || t.MessageID <= 0 {
		return interaction.TargetBinding{}, false
	}
	return interaction.TargetBinding{ChatID: t.ChatID, MessageID: t.MessageID}, true
}

type InlineTarget struct {
	MessageID tg.InputBotInlineMessageIDClass
	BindingID string
}

func (InlineTarget) PresentationTargetKind() string { return "inline" }
func (t InlineTarget) SessionBinding(actorID int64) interaction.Binding {
	return interaction.Binding{ActorID: actorID, InlineMessageID: strings.TrimSpace(t.BindingID)}
}
func (t InlineTarget) TargetBinding() (interaction.TargetBinding, bool) {
	id := strings.TrimSpace(t.BindingID)
	if id == "" {
		return interaction.TargetBinding{}, false
	}
	return interaction.TargetBinding{InlineMessageID: id}, true
}

type Bridge struct {
	Service core.TelegramServicer
}

var _ presentation.Port = (*Bridge)(nil)
var _ presentation.MediaDeliverer = (*Bridge)(nil)
var _ presentation.SessionTarget = MessageTarget{}
var _ presentation.SessionTarget = InlineTarget{}

func NewBridge(service core.TelegramServicer) *Bridge {
	return &Bridge{Service: service}
}

func (b *Bridge) Send(ctx context.Context, target presentation.Target, view presentation.CompiledView) (presentation.Target, error) {
	if b == nil || b.Service == nil {
		return nil, core.ErrUnavailable
	}
	messageTarget, ok := target.(MessageTarget)
	if !ok || messageTarget.Peer == nil || messageTarget.ChatID == 0 {
		return nil, ErrInvalidTarget
	}
	msg, err := b.Service.SendMessageWithMarkup(ctx, messageTarget.Peer, view.Text, EncodeMarkup(view.Rows))
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
		if t.Peer == nil || t.ChatID == 0 || t.MessageID <= 0 {
			return ErrInvalidTarget
		}
		return b.Service.EditMessageMarkup(ctx, t.Peer, t.MessageID, view.Text, EncodeMarkup(view.Rows))
	case InlineTarget:
		if t.MessageID == nil || strings.TrimSpace(t.BindingID) == "" {
			return ErrInvalidTarget
		}
		return b.Service.EditInlineBotMessage(ctx, t.MessageID, view.Text, EncodeMarkup(view.Rows))
	default:
		return ErrInvalidTarget
	}
}

type inlineMediaEditor interface {
	EditInlineBotMedia(context.Context, tg.InputBotInlineMessageIDClass, presentation.Media) error
}

func (b *Bridge) DeliverMedia(ctx context.Context, target presentation.Target, media presentation.Media) error {
	if b == nil || b.Service == nil {
		return core.ErrUnavailable
	}
	if strings.TrimSpace(media.Path) == "" {
		return ErrInvalidTarget
	}
	switch t := target.(type) {
	case MessageTarget:
		if t.Peer == nil || t.ChatID == 0 || t.MessageID <= 0 {
			return ErrInvalidTarget
		}
		if contextual, ok := b.Service.(core.ContextualTelegramServicer); ok {
			_, err := contextual.SendMediaContext(ctx, t.Peer, media.Type, media.Path, media.Caption, core.MessageSendContext{ReplyToID: t.MessageID})
			return err
		}
		_, err := b.Service.SendMedia(ctx, t.Peer, media.Type, media.Path, media.Caption)
		return err
	case InlineTarget:
		if t.MessageID == nil || strings.TrimSpace(t.BindingID) == "" {
			return ErrInvalidTarget
		}
		editor, ok := b.Service.(inlineMediaEditor)
		if !ok {
			return core.ErrUnsupported
		}
		return editor.EditInlineBotMedia(ctx, t.MessageID, media)
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

