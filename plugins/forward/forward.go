package forward

import (
	"github.com/inipew/goultroid/internal/core"
)

// Plugin provides message forwarding commands.
type Plugin struct{}

// New creates a new forward Plugin instance.
func New() *Plugin {
	return &Plugin{}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "forward"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Commands returns the list of commands provided by this plugin.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "forward",
			Aliases:     []string{"fwd"},
			Description: "Forward the replied message to Saved Messages",
			Usage:       ".forward",
			Category:    "Utility",
			Permission:  core.PermissionSudo,
			ReplyOnly:   true,
			Handler:     p.handleForward,
		},
	}
}

func (p *Plugin) handleForward(ctx *core.Context) error {
	if err := ctx.ForwardToSelf(); err != nil {
		return err
	}
	return ctx.Reply("📤 Message forwarded to Saved Messages!")
}
