package ping

import (
	appPing "github.com/inipew/goultroid/internal/application/ping"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

// Plugin provides the ping command across Userbot and Assistant surfaces.
type Plugin struct{}

// New creates a new ping Plugin.
func New() *Plugin {
	return &Plugin{}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "ping"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Capabilities declares the capabilities provided by this plugin (§4 bug16_1).
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "ping",
			Name:        "Ping",
			Description: "Check response latency",
			Category:    "Utility",
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		},
	}
}

// Commands returns the commands registered by this plugin.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "ping",
			Aliases:     []string{"p", "latency"},
			Description: "Check response latency",
			Usage:       ".ping",
			Category:    "Utility",
			Permission:  core.PermissionEveryone,
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
			Handler:     p.handlePing,
		},
	}
}

func (p *Plugin) handlePing(ctx *core.Context) error {
	uc := appPing.NewUseCase()
	res, err := uc.Execute(func() error {
		return ctx.EditOrReply("🏓 ...")
	})
	if err != nil {
		return err
	}
	return ctx.Edit(appPing.FormatResult(res.Latency))
}
