package core

import (
	"context"

	"github.com/gotd/td/tg"
)

// PeerBotOriginTracker classifies bot-origin messages using both peer identity
// and message ID. ID-only origin checks cannot distinguish Telegram dialogs.
type PeerBotOriginTracker interface {
	IsBotSentForPeer(peer tg.PeerClass, messageID int, selfID int64) bool
}

// IsAutomatedOutgoingMessage uses the canonical ingress decision if present.
// Direct plugin callers may fall back only to peer-aware origin tracking.
// Unknown legacy ID-only trackers are intentionally not consulted.
func IsAutomatedOutgoingMessage(ctx context.Context, message *MessageEnvelope, tracker any, selfID int64) bool {
	if message == nil || !message.Outgoing {
		return false
	}
	if decision := GetMessageDecision(ctx); decision != nil {
		return decision.Origin() == ExecutionAutomation
	}
	classifier, ok := tracker.(PeerBotOriginTracker)
	if !ok || classifier == nil || message.ID <= 0 || message.Peer.ID == 0 {
		return false
	}
	var peer tg.PeerClass
	switch message.Peer.Kind {
	case PeerKindUser:
		peer = &tg.PeerUser{UserID: message.Peer.ID}
	case PeerKindChat:
		peer = &tg.PeerChat{ChatID: message.Peer.ID}
	case PeerKindChannel:
		peer = &tg.PeerChannel{ChannelID: message.Peer.ID}
	default:
		return false
	}
	return classifier.IsBotSentForPeer(peer, message.ID, selfID)
}
