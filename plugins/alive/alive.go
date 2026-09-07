package alive

import (
	"time"

	appStatus "github.com/inipew/goultroid/internal/application/status"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

// Plugin provides the alive status command across Userbot and Assistant surfaces.
type Plugin struct {
	startTime time.Time
}

// New creates a new alive Plugin instance.
func New(startTime time.Time) *Plugin {
	return &Plugin{
		startTime: startTime,
	}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "alive"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Capabilities declares the capabilities provided by this plugin (§4 bug16_1).
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "alive",
			Name:        "Alive",
			Description: "Show uptime, system resources, and version",
			Category:    "Utility",
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		},
	}
}

// Commands returns the list of commands provided by this plugin.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "alive",
			Aliases:     []string{"a", "status", "uptime"},
			Description: "Show uptime, system resources, and version",
			Usage:       ".alive",
			Category:    "Utility",
			Permission:  core.PermissionEveryone,
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
			Cooldown:    3 * time.Second,
			Handler:     p.handleAlive,
		},
	}
}

func (p *Plugin) handleAlive(ctx *core.Context) error {
	var ownerID int64
	if ctx.Perms != nil {
		ownerID = ctx.Perms.OwnerID
	}

	snapshot := appStatus.CollectSnapshot(p.startTime, ownerID)
	cardText := appStatus.RenderAliveCard(snapshot, "")
	return ctx.EditOrReply(cardText)
}

func formatDuration(d time.Duration) string {
	return appStatus.FormatDuration(d)
}
