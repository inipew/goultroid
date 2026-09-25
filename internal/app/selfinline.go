package app

import (
	"errors"

	assistantclient "github.com/inipew/goultroid/internal/assistant/client"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
)

type selfInlineServiceProvider interface{ Service() core.TelegramServicer }

type selfInlineAssistantIdentity interface {
	InlineUsername() (string, error)
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
	return selfinline.NewWithIdentity(transport, func() (string, error) {
		username, err := assistantClient.InlineUsername()
		if errors.Is(err, assistantclient.ErrInlineDisabled) {
			return "", selfinline.ErrInlineDisabled
		}
		return username, err
	})
}

// SelfInlineRenderer returns the production userbot -> own Assistant inline
// render bridge. Both Telegram transport and Assistant inline capability are
// resolved lazily on every render so startup/reconnect cannot retain stale
// identities and disabled @BotFather inline mode fails before querying Telegram.
func (a *App) SelfInlineRenderer() selfinline.Renderer {
	if a == nil || a.client == nil || a.assistant == nil {
		return nil
	}
	return newSelfInlineRenderer(a.client, a.assistant)
}
