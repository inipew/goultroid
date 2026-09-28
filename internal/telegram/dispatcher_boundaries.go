package telegram

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	presentationselfinline "github.com/inipew/goultroid/internal/presentation/selfinline"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
)

// DispatcherService is the composition-boundary contract for the userbot
// dispatcher. Individual code paths obtain narrower views through typed getters.
type DispatcherService interface {
	core.CommandTelegramServicer
	inlineservice.TelegramAnswerer
	callbackQueryAnswerer
	botOriginTracker
}

type dispatcherCapabilities struct {
	command            core.TelegramCapabilities
	inline             inlineservice.TelegramAnswerer
	callback           callbackQueryAnswerer
	origin             botOriginTracker
	contextualMessages core.ContextualMessageServicer
	contextualMedia    core.ContextualMediaServicer
	presentation       presentationtelegram.BridgeService
	selfInline         presentationselfinline.Transport
}

func dispatcherCapabilitiesFrom(service DispatcherService) dispatcherCapabilities {
	if service == nil {
		return dispatcherCapabilities{}
	}
	caps := dispatcherCapabilities{
		command:  core.TelegramCapabilitiesFrom(service),
		inline:   service,
		callback: service,
		origin:   service,
	}
	if contextual, ok := service.(core.ContextualMessageServicer); ok {
		caps.contextualMessages = contextual
	}
	if contextual, ok := service.(core.ContextualMediaServicer); ok {
		caps.contextualMedia = contextual
	}
	if presentation, ok := service.(presentationtelegram.BridgeService); ok {
		caps.presentation = presentation
	}
	if selfInline, ok := service.(presentationselfinline.Transport); ok {
		caps.selfInline = selfInline
	}
	return caps
}

// OriginTracker is the narrow bot-origin classification boundary exposed to
// application composition.
type OriginTracker interface {
	IsBotSent(int) bool
}

// callbackQueryAnswerer is owned by Telegram callback ingress. Callback
// dispatch must not grow a dependency on unrelated message/media/profile APIs.
type callbackQueryAnswerer interface {
	AnswerCallbackQuery(context.Context, int64, string, bool) error
}

// botOriginTracker is the compatibility origin classifier used when peer-aware
// tracking is unavailable.
type botOriginTracker interface {
	IsBotSent(msgID int) bool
}

// peerAwareBotOriginTracker is the preferred origin classifier because message
// IDs are not globally unique across peers.
type peerAwareBotOriginTracker interface {
	botOriginTracker
	IsBotSentForPeer(peer tg.PeerClass, msgID int, selfID int64) bool
}

var (
	_ DispatcherService                = (*Service)(nil)
	_ callbackQueryAnswerer            = (*Service)(nil)
	_ botOriginTracker                 = (*Service)(nil)
	_ peerAwareBotOriginTracker        = (*Service)(nil)
	_ presentationselfinline.Transport = (*Service)(nil)
)
