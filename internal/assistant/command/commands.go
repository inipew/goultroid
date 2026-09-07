package command

import (
	"fmt"
	"runtime"
	"time"

	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/ui"
)

// PingResult records latency metrics for assistant ping commands.
type PingResult struct {
	Latency time.Duration
}

// RuntimeStatus contains operational metrics for the assistant runtime.
type RuntimeStatus struct {
	Uptime      time.Duration
	BotUsername string
	Engine      string
	Goroutines  int
	GoVersion   string
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

	// /start
	r.Register("/start", func(c *Context) error {
		screen := menu.BuildStartScreen(username(), uptime())
		text, markup := renderer(screen)
		_, err := c.Reply(text, markup)
		return err
	})

	// /help
	r.Register("/help", func(c *Context) error {
		screen := menu.BuildHelpScreen(username())
		text, markup := renderer(screen)
		_, err := c.Reply(text, markup)
		return err
	})

	// /ping
	r.Register("/ping", func(c *Context) error {
		start := time.Now()
		sent, err := c.Reply("🏓 ...", nil)
		if err != nil {
			return err
		}
		latency := time.Since(start).Milliseconds()
		if sent != nil {
			target := interaction.NewMessageTarget(c.Peer, sent.ID, 0, 0)
			return c.Interaction.Edit(c.Ctx, target, fmt.Sprintf("🏓 <b>Pong!</b>\n⚡ <b>Latency:</b> <code>%d ms</code>", latency), nil)
		}
		return nil
	})

	// /status
	r.Register("/status", func(c *Context) error {
		screen := menu.BuildStatusScreen(username(), uptime(), "GoUltroid (MTProto) v2")
		text, markup := renderer(screen)
		_, err := c.Reply(text, markup)
		return err
	})

	// /alive
	r.Register("/alive", func(c *Context) error {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		allocMB := float64(mem.Alloc) / 1024 / 1024
		sysMB := float64(mem.Sys) / 1024 / 1024

		card := ui.NewCard("GoUltroid Assistant is Alive & Running!").
			WithIcon("✨").
			AddField("Bot", "@"+username()).
			AddField("Uptime", formatDuration(uptime())).
			AddField("Go Version", ui.Code(runtime.Version())).
			AddField("RAM Usage", fmt.Sprintf("%s (%.1f / %.1f MB)", ui.ProgressBar(int64(mem.Alloc), int64(mem.Sys), 8), allocMB, sysMB)).
			AddField("Goroutines", ui.Code(fmt.Sprintf("%d", runtime.NumGoroutine()))).
			AddField("Status", "🟢 Active & Running").
			WithFooter("<i>Powered by Go & gotd</i>")

		_, err := c.Reply(card.Render(), nil)
		return err
	})
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
