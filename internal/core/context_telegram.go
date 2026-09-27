package core

// TelegramCapabilities groups capability-sized Telegram ports for a command
// Context without requiring one implementation to expose unrelated operations.
// Production may bind the same concrete transport to multiple fields.
type TelegramCapabilities struct {
	Messages           MessageServicer
	Admin              AdminServicer
	Media              MediaServicer
	Peers              PeerServicer
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
		Messages: service,
		Admin:    service,
		Media:    service,
		Peers:    service,
		Profile:  service,
	}
	if contextual, ok := service.(ContextualMessageServicer); ok {
		caps.ContextualMessages = contextual
	}
	if contextual, ok := service.(ContextualMediaServicer); ok {
		caps.ContextualMedia = contextual
	}
	return caps
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

func (c *Context) mediaServicer() MediaServicer {
	if c == nil {
		return nil
	}
	if c.Telegram.Media != nil {
		return c.Telegram.Media
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
