package app

import (
	"fmt"

	"github.com/inipew/goultroid/internal/plugin"
	addonPluginPkg "github.com/inipew/goultroid/plugins/addon"
	"github.com/inipew/goultroid/plugins/admin"
	"github.com/inipew/goultroid/plugins/afk"
	"github.com/inipew/goultroid/plugins/alive"
	"github.com/inipew/goultroid/plugins/blacklist"
	"github.com/inipew/goultroid/plugins/broadcast"
	"github.com/inipew/goultroid/plugins/downloader"
	"github.com/inipew/goultroid/plugins/filters"
	"github.com/inipew/goultroid/plugins/forward"
	"github.com/inipew/goultroid/plugins/fun"
	"github.com/inipew/goultroid/plugins/help"
	"github.com/inipew/goultroid/plugins/info"
	"github.com/inipew/goultroid/plugins/locks"
	"github.com/inipew/goultroid/plugins/media"
	"github.com/inipew/goultroid/plugins/notes"
	"github.com/inipew/goultroid/plugins/pin"
	"github.com/inipew/goultroid/plugins/ping"
	"github.com/inipew/goultroid/plugins/pmpermit"
	"github.com/inipew/goultroid/plugins/profile"
	schedPlugin "github.com/inipew/goultroid/plugins/scheduler"
	settingsPluginPkg "github.com/inipew/goultroid/plugins/settings"
	"github.com/inipew/goultroid/plugins/sticker"
	"github.com/inipew/goultroid/plugins/sudo"
	"github.com/inipew/goultroid/plugins/system"
	"github.com/inipew/goultroid/plugins/userlog"
)

// defaultPluginCatalog returns the ordered list of plugins for the application.
// Extracted from bootstrap.go to isolate the 25-entry hardcoded list.
func defaultPluginCatalog(core *coreDependencies, tg *telegramRuntime, dom *domainServices) ([]plugin.Plugin, error) {
	afkPlugin := afk.New(core.db, core.perms.OwnerID, tg.client.Service)
	afkPlugin.SetLogger(dom.logger)
	afkPlugin.SetResolver(tg.dispatcher.Resolver())

	filtersPlugin := filters.New(core.db, tg.client.Service)
	blacklistPlugin := blacklist.New(core.db, tg.client.Service)

	systemPlugin := system.New()
	systemPlugin.SetMetrics(core.metrics)

	downloaderPlugin := downloader.New(dom.downloadRegistry, dom.storage)
	mediaPlugin := media.New(dom.mediaService)

	pmpermitPlugin := pmpermit.New(dom.pmpermitService)
	userlogPlugin := userlog.New(dom.userlogService, core.perms.OwnerID)
	userlogPlugin.SetEventBus(core.eventBus)
	broadcastPlugin := broadcast.New(dom.broadcastService)
	addonPlugin := addonPluginPkg.New(dom.addonManager)

	helpPlugin := help.New(core.router)
	helpPlugin.SetStateStore(core.callbackStore)
	if err := core.callbackRouter.Register(helpPlugin); err != nil {
		return nil, fmt.Errorf("register help callback handler: %w", err)
	}

	settingsPlugin := settingsPluginPkg.New(dom.settingsService, core.callbackStore)
	settingsPlugin.SetLogger(dom.logger)
	if err := core.callbackRouter.Register(settingsPlugin); err != nil {
		return nil, fmt.Errorf("register settings callback handler: %w", err)
	}

	return []plugin.Plugin{
		ping.New(),
		helpPlugin,
		alive.New(dom.startTime),
		pin.New(),
		forward.New(),
		downloaderPlugin,
		sudo.New(core.db, core.perms),
		notes.New(core.db),
		afkPlugin,
		admin.New(dom.modService),
		mediaPlugin,
		sticker.New(),
		info.New(),
		systemPlugin,
		filtersPlugin,
		fun.New(),
		schedPlugin.New(dom.schedEngine),
		locks.New(),
		blacklistPlugin,
		profile.New(),
		pmpermitPlugin,
		broadcastPlugin,
		userlogPlugin,
		addonPlugin,
		settingsPlugin,
	}, nil
}

// buildPlugins is the public wiring entry used by app.New. Kept for compatibility; delegates to catalog.
func buildPlugins(core *coreDependencies, tg *telegramRuntime, dom *domainServices) ([]plugin.Plugin, error) {
	return defaultPluginCatalog(core, tg, dom)
}
