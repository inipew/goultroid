package command

import (
	"time"

	appStatus "github.com/inipew/goultroid/internal/application/status"
	"github.com/inipew/goultroid/internal/assistant/menu"
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

		startTime := time.Now().Add(-uptime)
		ownerID := r.OwnerID()
		snapshot := appStatus.CollectSnapshot(startTime, ownerID)
		cardText := appStatus.RenderAliveCard(snapshot, username)

		_, err := c.Reply(cardText, nil)
		return err
	})
}

// AttachDefaultCommands registers /start, /help, /ping, /alive, and /status into the command Router.
func AttachDefaultCommands(r *Router, getUsername func() string, getStartTime func() time.Time, renderer menu.RendererFunc) {
	AttachDefaultCommandsWithStore(r, getUsername, getStartTime, renderer, nil)
}

// AttachDefaultCommandsWithStore registers commands with optional instance store for session tracking.
func AttachDefaultCommandsWithStore(r *Router, getUsername func() string, getStartTime func() time.Time, renderer menu.RendererFunc, store menu.InstanceStore) {
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

	RegisterStart(r, username, uptime, renderer, store)
	RegisterHelp(r, username, renderer, store)
	RegisterPing(r)
	RegisterStatus(r, username, uptime, renderer, store)
	RegisterAlive(r, username, uptime)
}

func formatDuration(d time.Duration) string {
	return appStatus.FormatDuration(d)
}
