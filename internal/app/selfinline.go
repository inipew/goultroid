package app

import (
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
)

type selfInlineServiceProvider interface {
	Service() core.TelegramServicer
}

type selfInlineAssistantIdentity interface {
	Username() string
}

func newSelfInlineRenderer(client selfInlineServiceProvider, assistantClient selfInlineAssistantIdentity) selfinline.Renderer {
	if client == nil || assistantClient == nil {
		return nil
	}
	transport := selfinline.CurrentTransport(func() selfinline.Transport {
		current, _ := client.Service().(selfinline.Transport)
		return current
	})
	if transport == nil {
		return nil
	}
	return selfinline.New(transport, assistantClient.Username)
}

// SelfInlineRenderer returns the production userbot -> own Assistant inline
// render bridge. Both Telegram transport and Assistant username are resolved
// lazily on every render so startup/reconnect cannot retain stale identities or
// require Telegram Service to exist during App construction.
func (a *App) SelfInlineRenderer() selfinline.Renderer {
	if a == nil || a.client == nil || a.assistant == nil {
		return nil
	}
	return newSelfInlineRenderer(a.client, a.assistant)
}
