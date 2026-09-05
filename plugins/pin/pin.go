package pin

import (
	"strings"

	"github.com/inipew/goultroid/internal/core"
)

// Plugin provides message pin and unpin commands.
type Plugin struct{}

// New creates a new pin Plugin instance.
func New() *Plugin {
	return &Plugin{}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "pin"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Commands returns the list of commands provided by this plugin.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "pin",
			Aliases:     []string{"pinit"},
			Description: "Pin the replied message (or current message)",
			Usage:       ".pin [silent]",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			Handler:     p.handlePin,
		},
		{
			Name:        "unpin",
			Aliases:     []string{"unpinit"},
			Description: "Unpin the replied message (or current message)",
			Usage:       ".unpin",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			Handler:     p.handleUnpin,
		},
	}
}

func (p *Plugin) handlePin(ctx *core.Context) error {
	silent := false
	for _, arg := range ctx.Args {
		if strings.EqualFold(arg, "silent") || strings.EqualFold(arg, "quiet") {
			silent = true
			break
		}
	}

	if err := ctx.Pin(silent); err != nil {
		return err
	}

	if silent {
		return ctx.Reply("📌 Message pinned silently!")
	}
	return ctx.Reply("📌 Message pinned!")
}

func (p *Plugin) handleUnpin(ctx *core.Context) error {
	if err := ctx.Unpin(); err != nil {
		return err
	}
	return ctx.Reply("📌 Message unpinned!")
}
