package app

import (
	"github.com/inipew/goultroid/internal/assistant"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
	"github.com/inipew/goultroid/internal/telegram"
)

type selfInlineRendererAware interface {
	SetSelfInlineRenderer(selfinline.Renderer)
}

// wireSelfInlineRenderers injects the shared P8-B renderer only into features
// that explicitly opt in. Authorization is evaluated on every render against
// the feature's current manifest; no capability grant is cached in the plugin.
func wireSelfInlineRenderers(manager *plugin.Manager, client *telegram.Client, assistantClient assistant.Client, gate *plugin.CapabilityGate) {
	if manager == nil || client == nil || assistantClient == nil || gate == nil {
		return
	}
	transport, ok := client.Service().(selfinline.Transport)
	if !ok || transport == nil {
		return
	}
	base := selfinline.New(transport, assistantClient.Username)
	for _, registered := range manager.Plugins() {
		aware, ok := registered.(selfInlineRendererAware)
		if !ok {
			continue
		}
		pluginID := registered.Name()
		aware.SetSelfInlineRenderer(selfinline.Authorized(base, func() error {
			if err := gate.Check(pluginID, plugin.CapTelegramRead); err != nil {
				return err
			}
			return gate.Check(pluginID, plugin.CapTelegramSendMessage)
		}))
	}
}
