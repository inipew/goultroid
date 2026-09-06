package alive

import (
	"fmt"
	"runtime"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/ui"
)

// Plugin provides the alive status command.
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

// Commands returns the list of commands provided by this plugin.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "alive",
			Aliases:     []string{"a"},
			Description: "Show userbot uptime, system resources, and version",
			Usage:       ".alive",
			Category:    "Utility",
			Permission:  core.PermissionEveryone,
			Cooldown:    3 * time.Second,
			Handler:     p.handleAlive,
		},
	}
}

func (p *Plugin) handleAlive(ctx *core.Context) error {
	uptime := time.Since(p.startTime)

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	allocMB := float64(mem.Alloc) / 1024 / 1024
	sysMB := float64(mem.Sys) / 1024 / 1024

	var ownerStr string
	if ctx.Perms != nil && ctx.Perms.OwnerID != 0 {
		ownerStr = ui.Code(fmt.Sprintf("%d", ctx.Perms.OwnerID))
	} else {
		ownerStr = "<i>Not configured</i>"
	}

	card := ui.NewCard("GoUltroid is Alive & Running!").
		WithIcon("✨").
		AddField("Uptime", formatDuration(uptime)).
		AddField("Go Version", ui.Code(runtime.Version())).
		AddField("RAM Usage", fmt.Sprintf("%s (%.1f / %.1f MB)", ui.ProgressBar(int64(mem.Alloc), int64(mem.Sys), 8), allocMB, sysMB)).
		AddField("Goroutines", ui.Code(fmt.Sprintf("%d", runtime.NumGoroutine()))).
		AddField("Owner", ownerStr).
		AddField("Prefix", ui.Code(".")).
		WithFooter("<i>Powered by Go & gotd</i>")

	return ctx.EditOrReply(card.Render())
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	mins := d / time.Minute
	d -= mins * time.Minute
	secs := d / time.Second

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm %ds", days, hours, mins, secs)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, mins, secs)
	}
	if mins > 0 {
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	return fmt.Sprintf("%ds", secs)
}
