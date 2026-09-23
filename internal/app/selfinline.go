package app

import "github.com/inipew/goultroid/internal/presentation/selfinline"

// SelfInlineRenderer returns the production userbot -> own Assistant inline
// render bridge. The Assistant username is read lazily on every render so a
// transport restart cannot leave stale bot identity in feature code.
func (a *App) SelfInlineRenderer() selfinline.Renderer {
	if a == nil || a.client == nil || a.assistant == nil {
		return nil
	}
	transport, ok := a.client.Service().(selfinline.Transport)
	if !ok || transport == nil {
		return nil
	}
	return selfinline.New(transport, func() string {
		if a.assistant == nil {
			return ""
		}
		return a.assistant.Username()
	})
}
