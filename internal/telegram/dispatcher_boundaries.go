package telegram

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
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
	_ DispatcherService         = (*Service)(nil)
	_ callbackQueryAnswerer     = (*Service)(nil)
	_ botOriginTracker          = (*Service)(nil)
	_ peerAwareBotOriginTracker = (*Service)(nil)
)
