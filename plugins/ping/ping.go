package ping

import (
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

// Plugin provides the ping command.
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

// Commands returns the commands registered by this plugin.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "ping",
			Aliases:     []string{"p", "latency"},
			Description: "Check userbot response latency",
			Usage:       ".ping",
			Category:    "Utility",
			Permission:  core.PermissionEveryone,
			Handler:     p.handlePing,
		},
	}
}

func (p *Plugin) handlePing(ctx *core.Context) error {
	start := time.Now()

	if err := ctx.Reply("🏓 ..."); err != nil {
		return err
	}

	latency := time.Since(start)
	return ctx.Edit(fmt.Sprintf("🏓 <b>Pong!</b>\n⚡ <b>Latency:</b> <code>%d ms</code>", latency.Milliseconds()))
}
