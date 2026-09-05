package help

import (
	"fmt"
	"sort"
	"strings"

	"github.com/inipew/goultroid/internal/core"
)

// Plugin provides the help command.
type Plugin struct {
	router *core.Router
}

// New creates a new help Plugin.
func New(router *core.Router) *Plugin {
	return &Plugin{
		router: router,
	}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "help"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Commands returns the commands registered by this plugin.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "help",
			Aliases:     []string{"h", "commands"},
			Description: "Show available commands or detailed info for a specific command",
			Usage:       ".help [command]",
			Category:    "Utility",
			Permission:  core.PermissionEveryone,
			Handler:     p.handleHelp,
		},
	}
}

func (p *Plugin) handleHelp(ctx *core.Context) error {
	prefix := p.router.Prefix()

	// If specific command requested: e.g. .help ping
	if len(ctx.Args) > 0 {
		target := strings.TrimPrefix(ctx.Args[0], prefix)
		cmd, exists := p.router.Find(target)
		if !exists {
			return ctx.Reply(fmt.Sprintf("❌ Command %q not found.", ctx.Args[0]))
		}

		aliases := "-"
		if len(cmd.Aliases) > 0 {
			var prefixedAliases []string
			for _, a := range cmd.Aliases {
				prefixedAliases = append(prefixedAliases, prefix+a)
			}
			aliases = strings.Join(prefixedAliases, ", ")
		}

		category := cmd.Category
		if category == "" {
			category = "General"
		}

		usage := cmd.Usage
		if usage == "" {
			usage = prefix + cmd.Name
		}

		text := fmt.Sprintf(
			"📖 **Command:** `%s%s`\n"+
				"🏷 **Category:** %s\n"+
				"🔒 **Permission:** %s\n"+
				"💬 **Description:** %s\n"+
				"📝 **Usage:** `%s`\n"+
				"⚡ **Aliases:** %s",
			prefix, cmd.Name,
			category,
			cmd.Permission.String(),
			cmd.Description,
			usage,
			aliases,
		)
		return ctx.Reply(text)
	}

	// General command list grouped by Category
	all := p.router.All()
	categories := make(map[string][]core.Command)

	for _, cmd := range all {
		cat := cmd.Category
		if cat == "" {
			cat = "General"
		}
		categories[cat] = append(categories[cat], cmd)
	}

	var catNames []string
	for cat := range categories {
		catNames = append(catNames, cat)
	}
	sort.Strings(catNames)

	var sb strings.Builder
	sb.WriteString("📚 **GoUltroid Help**\n\n")

	for _, cat := range catNames {
		sb.WriteString(fmt.Sprintf("📂 **[%s]**\n", cat))
		cmds := categories[cat]
		sort.Slice(cmds, func(i, j int) bool {
			return cmds[i].Name < cmds[j].Name
		})
		for _, cmd := range cmds {
			desc := cmd.Description
			if desc == "" {
				desc = "No description"
			}
			sb.WriteString(fmt.Sprintf("• `%s%s` — %s\n", prefix, cmd.Name, desc))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("💡 Type `%shelp <command>` for command details.", prefix))
	return ctx.Reply(sb.String())
}
