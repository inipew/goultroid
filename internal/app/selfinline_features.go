package app

import (
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
)

type selfInlineRendererAware interface {
	SetSelfInlineRenderer(selfinline.Renderer)
}

// wireSelfInlineRenderers injects the shared P8-B renderer only into features
// that explicitly opt in. Transport resolution is lazy/current, so this wiring
// is valid during App construction before telegram.Client.Run installs Service.
// Authorization is evaluated on every render against the feature's current
// enable state and manifest; no lifecycle/capability grant is cached.
func wireSelfInlineRenderers(manager *plugin.Manager, client selfInlineServiceProvider, assistantClient selfInlineAssistantIdentity, gate *plugin.CapabilityGate) {
	if manager == nil || client == nil || assistantClient == nil || gate == nil {
		return
	}
	base := newSelfInlineRenderer(client, assistantClient)
	if base == nil {
		return
	}
	for _, registered := range manager.Plugins() {
		aware, ok := registered.(selfInlineRendererAware)
		if !ok {
			continue
		}
		pluginID := registered.Name()
		aware.SetSelfInlineRenderer(selfinline.Authorized(base, selfInlineFeatureAuthorizer(manager, gate, pluginID)))
	}
}

func selfInlineFeatureAuthorizer(manager *plugin.Manager, gate *plugin.CapabilityGate, pluginID string) selfinline.Authorizer {
	return func() error {
		if manager == nil || gate == nil || !manager.IsEnabled(pluginID) {
			return selfinline.ErrUnavailable
		}
		if err := gate.Check(pluginID, plugin.CapTelegramRead); err != nil {
			return err
		}
		return gate.Check(pluginID, plugin.CapTelegramSendMessage)
	}
}
