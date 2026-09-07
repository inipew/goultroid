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

type Plugin struct {
	router     *core.Router
	stateStore *callback.StateStore
}

func New(router *core.Router) *Plugin { return &Plugin{router: router} }

func (p *Plugin) SetStateStore(store *callback.StateStore) { p.stateStore = store }

func (p *Plugin) Name() string      { return "help" }
func (p *Plugin) Namespace() string { return "help" }

func (p *Plugin) CallbackOptions() callback.CallbackHandlerOptions {
	return callback.CallbackHandlerOptions{AutoAnswer: true}
}

func (p *Plugin) Init() error { return nil }

func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{{
		ID:          "help",
		Name:        "Help",
		Description: "Interactive help and command documentation browser",
		Category:    "Utility",
		Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
	}}
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{{
		Name:        "help",
		Aliases:     []string{"h", "commands"},
		Description: "Show available commands or detailed info for a specific command/module",
		Usage:       ".help [command|module]",
		Category:    "Utility",
		Permission:  core.PermissionEveryone,
		Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		Handler:     p.handleHelp,
	}}
}

func sendResult(ctx *core.Context, text string) error {
	return sendResultMarkup(ctx, text, nil)
}

func sendResultMarkup(ctx *core.Context, text string, markup tg.ReplyMarkupClass) error {
	chunks := splitMessage(text, maxTelegramLen)
	if len(chunks) == 0 {
		return nil
	}

	if ctx.IsAssistant() {
		if markup != nil {
			if err := ctx.ReplyMarkup(chunks[0], markup); err != nil {
				return err
			}
		} else if err := ctx.Reply(chunks[0]); err != nil {
			return err
		}
		for _, chunk := range chunks[1:] {
			if err := ctx.Reply(chunk); err != nil {
				return err
			}
		}
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

	if err := ctx.Edit(chunks[0]); err != nil {
		return err
	}
	for _, chunk := range chunks[1:] {
		if err := ctx.Reply(chunk); err != nil {
			return err
		}
	}
	return nil
}

func splitMessage(text string, maxLen int) []string {
	return core.SplitTelegramHTML(text, maxLen)
}

func (p *Plugin) handleHelp(ctx *core.Context) error {
	prefix := p.router.Prefix()
	source := ctx.Source.Surface()

	if len(ctx.Args) > 0 {
		target := strings.TrimPrefix(ctx.Args[0], prefix)

		if cmd, exists := p.router.Find(target); exists && cmd.IsAvailableOn(source) {
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

			card := ui.NewCard(fmt.Sprintf("Command: %s%s", prefix, cmd.Name)).WithIcon("📖")
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

		matchedCat, catCmds := p.getCategoryCommands(target, source)
		if matchedCat != "" {
			return sendResult(ctx, p.renderCategoryCard(matchedCat, catCmds, prefix))
		}

		return sendResult(ctx, ui.Error(fmt.Sprintf("Command or module %q not found.", ctx.Args[0])))
	}

	categories, catNames := p.getCategoryNames(source)
	overviewText := p.renderOverviewWithCategories(prefix, categories, catNames, source)
	if p.stateStore != nil {
		return sendResultMarkup(ctx, overviewText, p.buildOverviewMarkup(catNames, ctx.SenderID()))
	}
	return sendResult(ctx, overviewText)
}

func (p *Plugin) commandsForSource(source execution.Source) []core.Command {
	all := p.router.All()
	res := make([]core.Command, 0, len(all))
	for _, cmd := range all {
		if cmd.IsAvailableOn(source) {
			res = append(res, cmd)
		}
	}
	return res
}

func (p *Plugin) userbotCommands() []core.Command {
	return p.commandsForSource(execution.SourceUserbot)
}

func (p *Plugin) getCategoryNames(source execution.Source) (map[string][]core.Command, []string) {
	all := p.commandsForSource(source)
	categories := make(map[string][]core.Command)
	for _, cmd := range all {
		cat := cmd.Category
		if cat == "" {
			cat = "General"
		}
		categories[cat] = append(categories[cat], cmd)
	}
	catNames := make([]string, 0, len(categories))
	for cat := range categories {
		catNames = append(catNames, cat)
	}
	sort.Strings(catNames)
	return categories, catNames
}

func (p *Plugin) getCategoryCommands(target string, source execution.Source) (string, []core.Command) {
	all := p.commandsForSource(source)
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
	sort.Slice(catCmds, func(i, j int) bool { return catCmds[i].Name < catCmds[j].Name })
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
	categories, catNames := p.getCategoryNames(execution.SourceUserbot)
	return p.renderOverviewWithCategories(prefix, categories, catNames, execution.SourceUserbot), catNames
}

func (p *Plugin) renderOverviewWithCategories(prefix string, categories map[string][]core.Command, catNames []string, source execution.Source) string {
	all := p.commandsForSource(source)
	var sb strings.Builder
	sb.WriteString("📚 <b>GoUltroid Help</b>\n")
	sb.WriteString(fmt.Sprintf("<i>%d commands across %d modules.</i>\n\n", len(all), len(catNames)))

	for _, cat := range catNames {
		cmds := categories[cat]
		sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })

		var names []string
		for _, cmd := range cmds {
			names = append(names, fmt.Sprintf("<code>%s%s</code>", prefix, cmd.Name))
		}
		sb.WriteString(fmt.Sprintf("📂 <b>%s</b> <code>(%d)</code>\n", cat, len(cmds)))
		sb.WriteString(strings.Join(names, "  "))
		sb.WriteString("\n\n")
	}

	sb.WriteString(fmt.Sprintf(
		"💡 <i>Use <code>%shelp &lt;module&gt;</code> or <code>%shelp &lt;command&gt;</code> for details.</i>",
		prefix, prefix,
	))
	return strings.TrimSpace(sb.String())
}

func (p *Plugin) renderInteractiveOverview(prefix string, totalCmds, totalModules int) string {
	return fmt.Sprintf(
		"📚 <b>GoUltroid Help</b>\n\n<i>%d commands across %d modules.</i>\n\n<i>Select a module below to browse its commands:</i>\n\n💡 <i>Use <code>%shelp &lt;module&gt;</code> or <code>%shelp &lt;command&gt;</code> for details.</i>",
		totalCmds, totalModules, prefix, prefix,
	)
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
		row = append(row, ui.NewCallbackButton("📂 "+cat, callback.EncodeCallbackData("help", "cat", oid)))
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

func (p *Plugin) HandleCallback(ctx *callback.CallbackContext) error {
	switch ctx.Action {
	case "close":
		return ctx.DisableButtons("✅ Help menu closed.")

	case "home":
		prefix := p.router.Prefix()
		categories, catNames := p.getCategoryNames(execution.SourceUserbot)
		overviewText := p.renderOverviewWithCategories(prefix, categories, catNames, execution.SourceUserbot)
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

		matchedCat, catCmds := p.getCategoryCommands(state.Category, execution.SourceUserbot)
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
