package help

import (
	"fmt"
	"sort"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/ui"
)

const maxTelegramLen = 4096

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
			Description: "Show available commands or detailed info for a specific command/module",
			Usage:       ".help [command|module]",
			Category:    "Utility",
			Permission:  core.PermissionEveryone,
			Handler:     p.handleHelp,
		},
	}
}

// sendResult edits the trigger message in-place; if the text is too long it
// edits with the first chunk and replies with subsequent chunks.
func sendResult(ctx *core.Context, text string) error {
	chunks := splitMessage(text, maxTelegramLen)
	if len(chunks) == 0 {
		return nil
	}
	// Edit the command trigger message (edit-in-place userbot UX).
	if err := ctx.Edit(chunks[0]); err != nil {
		return err
	}
	// Overflow chunks sent as follow-up replies.
	for _, chunk := range chunks[1:] {
		if err := ctx.Reply(chunk); err != nil {
			return err
		}
	}
	return nil
}

// splitMessage splits text into chunks of at most maxLen bytes on HTML-safe
// boundaries (newline preferred; hard-cut if necessary).
func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}
		cut := maxLen
		// Try to cut at the last newline within the limit.
		if idx := strings.LastIndex(text[:cut], "\n"); idx > 0 {
			cut = idx + 1
		}
		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	return chunks
}

func (p *Plugin) handleHelp(ctx *core.Context) error {
	prefix := p.router.Prefix()

	// If specific command or category requested: e.g. .help ping or .help admin
	if len(ctx.Args) > 0 {
		target := strings.TrimPrefix(ctx.Args[0], prefix)

		// 1. Check if target matches a command name or alias
		if cmd, exists := p.router.Find(target); exists {
			var aliasesStr string
			if len(cmd.Aliases) > 0 {
				var prefixedAliases []string
				for _, a := range cmd.Aliases {
					prefixedAliases = append(prefixedAliases, ui.Code(prefix+a))
				}
				aliasesStr = strings.Join(prefixedAliases, ", ")
			} else {
				aliasesStr = "—"
			}

			category := cmd.Category
			if category == "" {
				category = "General"
			}

			usage := cmd.Usage
			if usage == "" {
				usage = prefix + cmd.Name
			}

			card := ui.NewCard(fmt.Sprintf("Command: %s%s", prefix, cmd.Name)).
				WithIcon("📖")

			if cmd.Description != "" {
				card.WithHeader(cmd.Description)
			}

			card.AddField("Category", category).
				AddField("Permission", ui.Badge(cmd.Permission)).
				AddField("Usage", ui.Code(usage)).
				AddField("Aliases", aliasesStr)

			if cmd.Cooldown > 0 {
				card.AddField("Cooldown", cmd.Cooldown.String())
			}
			if cmd.Timeout > 0 {
				card.AddField("Timeout", cmd.Timeout.String())
			}

			var scope []string
			if cmd.GroupOnly {
				scope = append(scope, "Groups only")
			}
			if cmd.PrivateOnly {
				scope = append(scope, "PM only")
			}
			if cmd.ReplyOnly {
				scope = append(scope, "Requires reply")
			}
			if len(scope) > 0 {
				card.AddField("Constraints", strings.Join(scope, ", "))
			}

			card.WithFooter(fmt.Sprintf("<i>Run with <code>%s%s</code></i>", prefix, cmd.Name))
			return sendResult(ctx, card.Render())
		}

		// 2. Check if target matches a category/module
		all := p.router.All()
		var matchedCat string
		var catCmds []core.Command
		for _, c := range all {
			cat := c.Category
			if cat == "" {
				cat = "General"
			}
			if strings.EqualFold(cat, target) {
				matchedCat = cat
				catCmds = append(catCmds, c)
			}
		}

		if matchedCat != "" {
			sort.Slice(catCmds, func(i, j int) bool {
				return catCmds[i].Name < catCmds[j].Name
			})

			var sb strings.Builder
			for _, c := range catCmds {
				desc := c.Description
				if desc == "" {
					desc = "No description"
				}
				sb.WriteString(fmt.Sprintf("• <code>%s%s</code> — %s\n", prefix, c.Name, ui.EscapeHTML(desc)))
			}

			card := ui.NewCard(fmt.Sprintf("Module: %s", matchedCat)).
				WithIcon("📂").
				WithHeader(fmt.Sprintf("%d commands available in this module:", len(catCmds))).
				WithRaw(sb.String()).
				WithFooter(fmt.Sprintf("<i>Tip: Use <code>%shelp &lt;command&gt;</code> for details.</i>", prefix))

			return sendResult(ctx, card.Render())
		}

		// 3. Not found
		return sendResult(ctx, ui.Error(fmt.Sprintf("Command or module %q not found.", ctx.Args[0])))
	}

	// General overview: compact category list only (no per-command listing).
	// This keeps the message well within Telegram's 4096-char limit even with
	// many plugins. Use `.help <module>` to expand a specific module.
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
	sb.WriteString("📚 <b>GoUltroid Help</b>\n")
	sb.WriteString(fmt.Sprintf("<i>%d commands across %d modules.</i>\n\n", len(all), len(catNames)))

	for _, cat := range catNames {
		cmds := categories[cat]
		sort.Slice(cmds, func(i, j int) bool {
			return cmds[i].Name < cmds[j].Name
		})

		// Build a compact preview: just the command names (no descriptions).
		var names []string
		for _, cmd := range cmds {
			names = append(names, fmt.Sprintf("<code>%s%s</code>", prefix, cmd.Name))
		}
		preview := strings.Join(names, "  ")

		sb.WriteString(fmt.Sprintf("📂 <b>%s</b> <code>(%d)</code>\n", cat, len(cmds)))
		sb.WriteString(preview)
		sb.WriteString("\n\n")
	}

	sb.WriteString(fmt.Sprintf(
		"💡 <i>Use <code>%shelp &lt;module&gt;</code> or <code>%shelp &lt;command&gt;</code> for details.</i>",
		prefix, prefix,
	))

	return sendResult(ctx, strings.TrimSpace(sb.String()))
}
