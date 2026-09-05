package alive

import (
	"fmt"
	"runtime"
	"time"

	"github.com/inipew/goultroid/internal/core"
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
		ownerStr = fmt.Sprintf("`%d`", ctx.Perms.OwnerID)
	} else {
		ownerStr = "Not configured"
	}

	text := fmt.Sprintf(
		"✨ **GoUltroid is Alive & Running!**\n\n"+
			"⏱️ **Uptime:** %s\n"+
			"🐹 **Go Version:** `%s`\n"+
			"🧠 **RAM Usage:** `%.2f MB` / `%.2f MB`\n"+
			"🔄 **Goroutines:** `%d`\n"+
			"👑 **Owner:** %s\n"+
			"⚡ **Prefix:** `.`",
		formatDuration(uptime),
		runtime.Version(),
		allocMB, sysMB,
		runtime.NumGoroutine(),
		ownerStr,
	)

	return ctx.Reply(text)
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
