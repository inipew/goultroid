package help

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/ui"
	"github.com/inipew/goultroid/internal/ui/render"
)

const maxTelegramLen = 4096

var (
	_ callback.Handler            = (*Plugin)(nil)
	_ callback.HandlerWithOptions = (*Plugin)(nil)
)

type helpMenuState struct {
	Category string `json:"c"`
	Page     int    `json:"p"`
	UserID   int64  `json:"u"`
}

// Plugin provides the help command and interactive module browser.
type Plugin struct {
	router     *core.Router
	stateStore *callback.StateStore
}

// New creates a new help Plugin.
func New(router *core.Router) *Plugin {
	return &Plugin{
		router: router,
	}
}

// SetStateStore configures the state store for interactive inline buttons.
func (p *Plugin) SetStateStore(store *callback.StateStore) {
	p.stateStore = store
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "help"
}

func (p *Plugin) Namespace() string {
	return "help"
}

func (p *Plugin) CallbackOptions() callback.CallbackHandlerOptions {
	return callback.CallbackHandlerOptions{
		AutoAnswer: true,
	}
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Capabilities declares the capabilities provided by this plugin (§4 bug16_1).
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "help",
			Name:        "Help",
			Description: "Interactive help and command documentation browser",
			Category:    "Utility",
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		},
	}
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
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
			Handler:     p.handleHelp,
		},
	}
}

// sendResult edits the trigger message in-place; if the text is too long it
// edits with the first chunk and replies with subsequent chunks.
func sendResult(ctx *core.Context, text string) error {
	return sendResultMarkup(ctx, text, nil)
}

func sendResultMarkup(ctx *core.Context, text string, markup tg.ReplyMarkupClass) error {
	chunks := splitMessage(text, maxTelegramLen)
	if len(chunks) == 0 {
		return nil
	}

	if markup != nil {
		if err := ctx.EditMarkup(chunks[0], markup); err == nil {
			for _, chunk := range chunks[1:] {
				if err := ctx.Reply(chunk); err != nil {
					return err
				}
			}
			return nil
		}
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

// splitMessage splits text into chunks of at most maxLen runes on safe HTML boundaries.
func splitMessage(text string, maxLen int) []string {
	return core.SplitTelegramHTML(text, maxLen)
}

func (p *Plugin) handleHelp(ctx *core.Context) error {
	prefix := p.router.Prefix()

	// If specific command or category requested: e.g. .help ping or .help admin
	if len(ctx.Args) > 0 {
		target := strings.TrimPrefix(ctx.Args[0], prefix)

		// 1. Check if target matches a command name or alias
		if cmd, exists := p.router.Find(target); exists && cmd.IsAvailableOn(execution.SourceUserbot) {
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
		matchedCat, catCmds := p.getCategoryCommands(target)
		if matchedCat != "" {
			cardText := p.renderCategoryCard(matchedCat, catCmds, prefix)
			return sendResult(ctx, cardText)
		}

		// 3. Not found
		return sendResult(ctx, ui.Error(fmt.Sprintf("Command or module %q not found.", ctx.Args[0])))
	}

	// General overview
	categories, catNames := p.getCategoryNames()
	if p.stateStore != nil {
		all := p.userbotCommands()
		overviewText := p.renderInteractiveOverview(prefix, len(all), len(catNames))
		markup := p.buildOverviewMarkup(catNames, ctx.SenderID())
		return sendResultMarkup(ctx, overviewText, markup)
	}

	overviewText := p.renderOverviewWithCategories(prefix, categories, catNames)
	return sendResult(ctx, overviewText)
}

func (p *Plugin) userbotCommands() []core.Command {
	all := p.router.All()
	var res []core.Command
	for _, cmd := range all {
		if cmd.IsAvailableOn(execution.SourceUserbot) {
			res = append(res, cmd)
		}
	}
	return res
}

func (p *Plugin) renderInteractiveOverview(prefix string, totalCmds, totalModules int) string {
	var sb strings.Builder
	sb.WriteString("📚 <b>GoUltroid Help</b>\n")
	sb.WriteString(fmt.Sprintf("<i>%d commands across %d modules.</i>\n\n", totalCmds, totalModules))
	sb.WriteString("<i>Select a module below to browse its commands:</i>\n\n")
	sb.WriteString(fmt.Sprintf("💡 <i>Use <code>%shelp &lt;module&gt;</code> or <code>%shelp &lt;command&gt;</code> for details.</i>", prefix, prefix))
	return strings.TrimSpace(sb.String())
}

func (p *Plugin) getCategoryNames() (map[string][]core.Command, []string) {
	all := p.userbotCommands()
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
	return categories, catNames
}

func (p *Plugin) getCategoryCommands(target string) (string, []core.Command) {
	all := p.userbotCommands()
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
	sort.Slice(catCmds, func(i, j int) bool {
		return catCmds[i].Name < catCmds[j].Name
	})
	return matchedCat, catCmds
}

func (p *Plugin) renderCategoryCard(cat string, cmds []core.Command, prefix string) string {
	var sb strings.Builder
	for _, c := range cmds {
		desc := c.Description
		if desc == "" {
			desc = "No description"
		}
		sb.WriteString(fmt.Sprintf("• <code>%s%s</code> — %s\n", prefix, c.Name, ui.EscapeHTML(desc)))
	}

	card := ui.NewCard(fmt.Sprintf("Module: %s", cat)).
		WithIcon("📂").
		WithHeader(fmt.Sprintf("%d commands available in this module:", len(cmds))).
		WithRaw(sb.String()).
		WithFooter(fmt.Sprintf("<i>Tip: Use <code>%shelp &lt;command&gt;</code> for details.</i>", prefix))

	return card.Render()
}

func (p *Plugin) renderOverview(prefix string) (string, []string) {
	categories, catNames := p.getCategoryNames()
	return p.renderOverviewWithCategories(prefix, categories, catNames), catNames
}

func (p *Plugin) renderOverviewWithCategories(prefix string, categories map[string][]core.Command, catNames []string) string {
	all := p.userbotCommands()
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

	return strings.TrimSpace(sb.String())
}


func (p *Plugin) buildOverviewMarkup(catNames []string, userID int64) tg.ReplyMarkupClass {
	if p.stateStore == nil {
		return nil
	}

	var rows []ui.ButtonRow
	var row []ui.Button

	for _, cat := range catNames {
		st := helpMenuState{Category: cat, UserID: userID}
		oid := p.stateStore.Store(st, userID, 15*time.Minute)
		btn := ui.NewCallbackButton("📂 "+cat, callback.EncodeCallbackData("help", "cat", oid))
		row = append(row, btn)
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	backBtn := ui.NewCallbackButton("« Back to Menu", callback.EncodeCallbackData("assistant", "start", callback.ActionNoop))
	closeBtn := ui.NewCallbackButton("❌ Close", callback.EncodeCallbackData("help", "close", callback.ActionNoop))
	rows = append(rows, ui.ButtonRow{backBtn, closeBtn})

	return render.ToTelegramMarkup(ui.Markup{Rows: rows})
}

// HandleCallback handles interactive module browser navigation.
func (p *Plugin) HandleCallback(ctx *callback.CallbackContext) error {
	switch ctx.Action {
	case "close":
		return ctx.DisableButtons("✅ Help menu closed.")

	case "home":
		prefix := p.router.Prefix()
		all := p.router.All()
		_, catNames := p.getCategoryNames()
		overviewText := p.renderInteractiveOverview(prefix, len(all), len(catNames))
		markup := p.buildOverviewMarkup(catNames, ctx.UserID)
		return ctx.Edit(overviewText, markup)

	case "cat":
		var state helpMenuState
		if ctx.State != nil {
			if s, ok := ctx.State.(helpMenuState); ok {
				state = s
			}
		}
		if state.Category == "" {
			return ctx.Answer("Module not found", false)
		}

		matchedCat, catCmds := p.getCategoryCommands(state.Category)
		if matchedCat == "" {
			return ctx.Answer("Module not found", false)
		}

		cardText := p.renderCategoryCard(matchedCat, catCmds, p.router.Prefix())

		homeOid := p.stateStore.Store(helpMenuState{UserID: ctx.UserID}, ctx.UserID, 15*time.Minute)
		navRow := ui.ButtonRow{
			ui.NewCallbackButton("🔙 Back", callback.EncodeCallbackData("help", "home", homeOid)),
			ui.NewCallbackButton("❌ Close", callback.EncodeCallbackData("help", "close", callback.ActionNoop)),
		}
		markup := render.ToTelegramMarkup(ui.Markup{Rows: []ui.ButtonRow{navRow}})

		return ctx.Edit(cardText, markup)

	default:
		return nil
	}
}
