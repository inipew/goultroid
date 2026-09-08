package app

import (
	"github.com/inipew/goultroid/internal/plugin"
)

// defaultPluginCatalog returns plugins configured outside the modular registry.
// All builtin features now register via internal/module and generated_modules.go.
func defaultPluginCatalog(core *coreDependencies, tg *telegramRuntime, dom *domainServices) ([]plugin.Plugin, error) {
	return nil, nil
}

func buildPlugins(core *coreDependencies, tg *telegramRuntime, dom *domainServices) ([]plugin.Plugin, error) {
	return defaultPluginCatalog(core, tg, dom)
}
