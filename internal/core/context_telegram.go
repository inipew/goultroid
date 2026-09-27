package core

// TelegramCapabilities groups capability-sized Telegram ports for a command
// Context without requiring one implementation to expose unrelated operations.
// Production may bind the same concrete transport to multiple fields.
type TelegramCapabilities struct {
	Messages           MessageServicer
	MessageActions     MessageActionServicer
	Reactions          MessageReactionServicer
	Forwarding         MessageForwardServicer
	Admin              AdminServicer
	Media              MediaServicer
	MediaSend          MediaSendServicer
	MediaDownload      MediaDownloadServicer
	Peers              PeerServicer
	FullChat           FullChatServicer
	Profile            ProfileServicer
	ContextualMessages ContextualMessageServicer
	ContextualMedia    ContextualMediaServicer
}

// TelegramCapabilitiesFrom adapts one aggregate command transport into the
// capability container used by Context. Optional contextual extensions are
// discovered independently so message and media support do not depend on each
// other.
func TelegramCapabilitiesFrom(service CommandTelegramServicer) TelegramCapabilities {
	if service == nil {
		return TelegramCapabilities{}
	}
	caps := TelegramCapabilities{
		Messages:       service,
		MessageActions: service,
		Reactions:      service,
		Forwarding:     service,
		Admin:          service,
		Media:          service,
		MediaSend:      service,
		MediaDownload:  service,
		Peers:          service,
		FullChat:       service,
		Profile:        service,
	}
	if contextual, ok := service.(ContextualMessageServicer); ok {
		caps.ContextualMessages = contextual
	}
	if contextual, ok := service.(ContextualMediaServicer); ok {
		caps.ContextualMedia = contextual
	}
	return caps
}

func (c *Context) messageActionServicer() MessageActionServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.MessageActions != nil {
		return c.Telegram.MessageActions
	}
	if c.Telegram.Messages != nil {
		return c.Telegram.Messages
	}
	return c.Svc
}

func (c *Context) reactionServicer() MessageReactionServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.Reactions != nil {
		return c.Telegram.Reactions
	}
	if c.Telegram.Messages != nil {
		return c.Telegram.Messages
	}
	if c.Svc != nil {
		return c.Svc
	}
	return nil
}

func (c *Context) forwardServicer() MessageForwardServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.Forwarding != nil {
		return c.Telegram.Forwarding
	}
	if c.Telegram.Messages != nil {
		return c.Telegram.Messages
	}
	if c.Svc != nil {
		return c.Svc
	}
	return nil
}

func (c *Context) messageServicer() MessageServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.Messages != nil {
		return c.Telegram.Messages
	}
	return c.Svc
}

func (c *Context) adminServicer() AdminServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.Admin != nil {
		return c.Telegram.Admin
	}
	return c.Svc
}

func (c *Context) mediaSendServicer() MediaSendServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.MediaSend != nil {
		return c.Telegram.MediaSend
	}
	if c.Telegram.Media != nil {
		return c.Telegram.Media
	}
	return c.Svc
}

func (c *Context) mediaDownloadServicer() MediaDownloadServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.MediaDownload != nil {
		return c.Telegram.MediaDownload
	}
	if c.Telegram.Media != nil {
		return c.Telegram.Media
	}
	return c.Svc
}

func (c *Context) mediaServicer() MediaServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.Media != nil {
		return c.Telegram.Media
	}
	return c.Svc
}

func (c *Context) fullChatServicer() FullChatServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.FullChat != nil {
		return c.Telegram.FullChat
	}
	if c.Telegram.Peers != nil {
		return c.Telegram.Peers
	}
	return c.Svc
}

func (c *Context) peerServicer() PeerServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.Peers != nil {
		return c.Telegram.Peers
	}
	return c.Svc
}

func (c *Context) profileServicer() ProfileServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.Profile != nil {
		return c.Telegram.Profile
	}
	return c.Svc
}

func (c *Context) contextualMessageServicer() ContextualMessageServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.ContextualMessages != nil {
		return c.Telegram.ContextualMessages
	}
	if c.Svc != nil {
		if contextual, ok := c.Svc.(ContextualMessageServicer); ok {
			return contextual
		}
	}
	return nil
}

func (c *Context) contextualMediaServicer() ContextualMediaServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.ContextualMedia != nil {
		return c.Telegram.ContextualMedia
	}
	if c.Svc != nil {
		if contextual, ok := c.Svc.(ContextualMediaServicer); ok {
			return contextual
		}
	}
	return nil
}

// MessageService returns the message capability available to this Context.
// Legacy Svc fallback is resolved here so production callers do not need to
// depend on the compatibility aggregate.
func (c *Context) MessageService() MessageServicer {
	return c.messageServicer()
}

// MessageActionService returns the send/edit/delete/get/pin/purge capability
// without implying reaction or forwarding support.
func (c *Context) MessageActionService() MessageActionServicer {
	return c.messageActionServicer()
}

// AdminService returns the moderation/admin capability available to this
// Context.
func (c *Context) AdminService() AdminServicer {
	return c.adminServicer()
}

// MediaService returns the file/media capability available to this Context.
func (c *Context) MediaService() MediaServicer {
	return c.mediaServicer()
}

// MediaSendService returns media delivery without implying download support.
func (c *Context) MediaSendService() MediaSendServicer {
	return c.mediaSendServicer()
}

// MediaDownloadService returns media download without implying send support.
func (c *Context) MediaDownloadService() MediaDownloadServicer {
	return c.mediaDownloadServicer()
}

// PeerService returns the peer lookup/state capability available to this
// Context.
func (c *Context) PeerService() PeerServicer {
	return c.peerServicer()
}

// FullChatService returns the chat lookup capability without implying user
// lookup or block/unblock support.
func (c *Context) FullChatService() FullChatServicer {
	return c.fullChatServicer()
}

// ProfileService returns the self-profile/dialog/contact capability available
// to this Context.
func (c *Context) ProfileService() ProfileServicer {
	return c.profileServicer()
}

// ContextualMessageService returns the optional contextual message transport.
func (c *Context) ContextualMessageService() ContextualMessageServicer {
	return c.contextualMessageServicer()
}

// ContextualMediaService returns the optional contextual media transport.
func (c *Context) ContextualMediaService() ContextualMediaServicer {
	return c.contextualMediaServicer()
}
