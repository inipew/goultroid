package command

import (
	"fmt"
	"runtime"
	"time"

	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/ui"
)

// RegisterAlive attaches the /alive command handler to the Router.
func RegisterAlive(r *Router, usernameProvider func() string, uptimeProvider func() time.Duration) {
	if r == nil {
		return
	}
	r.Register("/alive", func(c *Context) error {
		username := "GoUltroidBot"
		if usernameProvider != nil {
			username = usernameProvider()
		}
		var uptime time.Duration
		if uptimeProvider != nil {
			uptime = uptimeProvider()
		}

		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		allocMB := float64(mem.Alloc) / 1024 / 1024
		sysMB := float64(mem.Sys) / 1024 / 1024

		card := ui.NewCard("GoUltroid Assistant is Alive & Running!").
			WithIcon("✨").
			AddField("Bot", "@"+username).
			AddField("Uptime", formatDuration(uptime)).
			AddField("Go Version", ui.Code(runtime.Version())).
			AddField("RAM Usage", fmt.Sprintf("%s (%.1f / %.1f MB)", ui.ProgressBar(int64(mem.Alloc), int64(mem.Sys), 8), allocMB, sysMB)).
			AddField("Goroutines", ui.Code(fmt.Sprintf("%d", runtime.NumGoroutine()))).
			AddField("Status", "🟢 Active & Running").
			WithFooter("<i>Powered by Go & gotd</i>")

		_, err := c.Reply(card.Render(), nil)
		return err
	})
}

// AttachDefaultCommands registers /start, /help, /ping, /alive, and /status into the command Router.
func AttachDefaultCommands(r *Router, getUsername func() string, getStartTime func() time.Time, renderer menu.RendererFunc) {
	if r == nil {
		return
	}

	uptime := func() time.Duration {
		if getStartTime != nil {
			return time.Since(getStartTime())
		}
		return 0
	}

	username := func() string {
		if getUsername != nil {
			return getUsername()
		}
		return "GoUltroidBot"
	}

	RegisterStart(r, username, uptime, renderer)
	RegisterHelp(r, username, renderer)
	RegisterPing(r)
	RegisterStatus(r, username, uptime, renderer)
	RegisterAlive(r, username, uptime)
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
