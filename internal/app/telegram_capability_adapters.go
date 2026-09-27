package app

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/telegram"
)

type appOriginTracker interface {
	IsBotSent(int) bool
}

type moduleTelegramCapabilityProvider interface {
	MessageService() core.MessageServicer
	AdminService() core.AdminServicer
	MediaService() core.MediaServicer
	ContextualMessageService() core.ContextualMessageServicer
	ContextualMediaService() core.ContextualMediaServicer
	OriginTracker() telegram.OriginTracker
	Resolver() core.PeerResolver
}

func newModuleTelegramRuntime(provider moduleTelegramCapabilityProvider) module.TelegramRuntime {
	if provider == nil {
		return module.TelegramRuntime{}
	}
	return module.TelegramRuntime{
		MessageService: func() core.MessageServicer {
			return provider.MessageService()
		},
		AdminService: func() core.AdminServicer {
			return provider.AdminService()
		},
		MediaService: func() core.MediaServicer {
			return provider.MediaService()
		},
		ContextualMessageService: func() core.ContextualMessageServicer {
			return provider.ContextualMessageService()
		},
		ContextualMediaService: func() core.ContextualMediaServicer {
			return provider.ContextualMediaService()
		},
		OriginTracker: func() module.BotOriginTracker {
			return provider.OriginTracker()
		},
		Resolver: provider.Resolver(),
	}
}

type pmPermitTelegramAdapter struct {
	messages core.MessageServicer
	peers    core.PeerServicer
	origin   appOriginTracker
}

func newPMPermitTelegramAdapter(messages core.MessageServicer, peers core.PeerServicer, origin appOriginTracker) *pmPermitTelegramAdapter {
	if messages == nil || peers == nil {
		return nil
	}
	return &pmPermitTelegramAdapter{messages: messages, peers: peers, origin: origin}
}

func (a *pmPermitTelegramAdapter) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if a == nil || a.messages == nil {
		return nil, core.ErrUnavailable
	}
	return a.messages.SendMessage(ctx, peer, text)
}

func (a *pmPermitTelegramAdapter) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	if a == nil || a.messages == nil {
		return core.ErrUnavailable
	}
	return a.messages.DeleteMessage(ctx, peer, msgIDs)
}

func (a *pmPermitTelegramAdapter) BlockUser(ctx context.Context, peer tg.InputPeerClass) error {
	if a == nil || a.peers == nil {
		return core.ErrUnavailable
	}
	return a.peers.BlockUser(ctx, peer)
}

func (a *pmPermitTelegramAdapter) UnblockUser(ctx context.Context, peer tg.InputPeerClass) error {
	if a == nil || a.peers == nil {
		return core.ErrUnavailable
	}
	return a.peers.UnblockUser(ctx, peer)
}

func (a *pmPermitTelegramAdapter) IsBotSent(msgID int) bool {
	return a != nil && a.origin != nil && a.origin.IsBotSent(msgID)
}

type broadcastTelegramAdapter struct {
	messages core.MessageServicer
	media    core.MediaServicer
}

func newBroadcastTelegramAdapter(messages core.MessageServicer, media core.MediaServicer) *broadcastTelegramAdapter {
	if messages == nil || media == nil {
		return nil
	}
	return &broadcastTelegramAdapter{messages: messages, media: media}
}

func (a *broadcastTelegramAdapter) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if a == nil || a.messages == nil {
		return nil, core.ErrUnavailable
	}
	return a.messages.SendMessage(ctx, peer, text)
}

func (a *broadcastTelegramAdapter) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType, path, caption string) (*tg.Message, error) {
	if a == nil || a.media == nil {
		return nil, core.ErrUnavailable
	}
	return a.media.SendMedia(ctx, peer, mediaType, path, caption)
}
