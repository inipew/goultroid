package command

import (
	"fmt"
	"runtime"
	"time"

	"github.com/inipew/goultroid/internal/assistant/menu"
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
		latency := time.Since(start).Truncate(time.Millisecond)
		reply := fmt.Sprintf("🏓 <b>Pong!</b>\n\n• <b>Latency:</b> %s", latency)
		_, err := c.Reply(reply, nil)
		return err
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
		uptimeStr := uptime().Truncate(time.Second).String()
		body := fmt.Sprintf(
			"⚡ <b>GoUltroid Assistant Status</b>\n\n"+
				"• <b>Bot:</b> @%s\n"+
				"• <b>Status:</b> 🟢 Active & Running\n"+
				"• <b>Uptime:</b> %s\n"+
				"• <b>Go Version:</b> %s\n"+
				"• <b>Goroutines:</b> %d\n",
			username(), uptimeStr, runtime.Version(), runtime.NumGoroutine(),
		)
		_, err := c.Reply(body, nil)
		return err
	})
}
