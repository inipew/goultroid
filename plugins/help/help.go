package help

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
	"github.com/inipew/goultroid/internal/ui"
)

const maxTelegramLen = 4096

type Plugin struct {
	router   *core.Router
	renderer selfinline.Renderer
}

func New(router *core.Router) *Plugin { return &Plugin{router: router} }

func (p *Plugin) SetSelfInlineRenderer(renderer selfinline.Renderer) {
	if p != nil {
		p.renderer = renderer
	}
}

func (p *Plugin) Name() string      { return "help" }
func (p *Plugin) Namespace() string { return "help" }

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

func groupAuthorizationLabel(requirement core.GroupAuthorizationRequirement) string {
	if !requirement.Required() {
		return ""
	}
	rights := make([]string, 0, 7)
	if requirement.Rights.ChangeInfo {
		rights = append(rights, "change_info")
	}
	if requirement.Rights.DeleteMessages {
		rights = append(rights, "delete_messages")
	}
	if requirement.Rights.BanUsers {
		rights = append(rights, "ban_users")
	}
	if requirement.Rights.InviteUsers {
		rights = append(rights, "invite_users")
	}
	if requirement.Rights.PinMessages {
		rights = append(rights, "pin_messages")
	}
	if requirement.Rights.AddAdmins {
		rights = append(rights, "add_admins")
	}
	if requirement.Rights.ManageTopics {
		rights = append(rights, "manage_topics")
	}
	if len(rights) == 0 {
		return requirement.Level.String()
	}
	return fmt.Sprintf("%s + %s", requirement.Level.String(), strings.Join(rights, ", "))
}

func (p *Plugin) handleHelp(ctx *core.Context) error {
	prefix := p.router.Prefix()
	source := ctx.Source.Surface()
	if source == execution.SourceUserbot {
		handled, err := p.openUserbotHelp(ctx, prefix)
		if err != nil || handled {
			return err
		}
	}
	return p.handleNativeHelp(ctx, prefix, source)
}

func (p *Plugin) handleNativeHelp(ctx *core.Context, prefix string, source execution.Source) error {
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

			card := ui.NewCard(ctx.T("help.command_title", prefix+cmd.Name)).WithIcon("📖")
			if cmd.Description != "" {
				card.WithHeader(cmd.Description)
			}
			card.AddField(ctx.T("help.field.category"), category).
				AddField(ctx.T("help.field.permission"), ui.Badge(cmd.Permission)).
				AddField(ctx.T("help.field.invocation"), ui.Code(cmd.EffectiveInvocation(ctx.Source).String())).
				AddField(ctx.T("help.field.usage"), ui.Code(usage)).
				AddField(ctx.T("help.field.aliases"), aliasesStr)
			if groupAuth := groupAuthorizationLabel(cmd.GroupAuthorization); groupAuth != "" {
				card.AddField(ctx.T("help.field.group_authorization"), ui.Code(groupAuth))
			}

			if cmd.Cooldown > 0 {
				card.AddField(ctx.T("help.field.cooldown"), cmd.Cooldown.String())
			}
			if cmd.Timeout > 0 {
				card.AddField(ctx.T("help.field.timeout"), cmd.Timeout.String())
			}

			var scope []string
			if cmd.GroupOnly {
				scope = append(scope, ctx.T("help.scope.groups_only"))
			}
			if cmd.PrivateOnly {
				scope = append(scope, ctx.T("help.scope.pm_only"))
			}
			if cmd.ReplyOnly {
				scope = append(scope, ctx.T("help.scope.requires_reply"))
			}
			if len(scope) > 0 {
				card.AddField(ctx.T("help.field.constraints"), strings.Join(scope, ", "))
			}
			card.WithFooter(ctx.T("help.run_with", prefix+cmd.Name))
			return sendResult(ctx, card.Render())
		}

		matchedCat, catCmds := p.getCategoryCommands(target, source)
		if matchedCat != "" {
			return sendResult(ctx, p.renderCategoryCard(ctx, matchedCat, catCmds, prefix))
		}

		return sendResult(ctx, ui.Error(ctx.T("help.not_found", ctx.Args[0])))
	}

	categories, catNames := p.getCategoryNames(source)
	return sendResult(ctx, p.renderOverviewWithCategories(ctx, prefix, categories, catNames, source))
}

func (p *Plugin) openUserbotHelp(ctx *core.Context, prefix string) (bool, error) {
	if ctx == nil || ctx.PeerID == nil {
		return true, core.ErrInvalidArgs
	}
	if p == nil || p.renderer == nil {
		return false, nil
	}
	query := "help"
	if len(ctx.Args) > 0 {
		target := strings.TrimSpace(strings.TrimPrefix(ctx.Args[0], prefix))
		if target != "" {
			query += " " + target
		}
	}
	request := selfinline.Request{Peer: ctx.PeerID, Query: query}
	if ctx.Message != nil {
		request.ReplyToID = ctx.Message.ReplyToID
		request.TopicID = ctx.Message.TopicID
	}
	if _, err := p.renderer.Render(ctx.Ctx, request); err != nil {
		if selfinline.FallbackSafe(err) {
			return false, nil
		}
		return true, ctx.Status(ctx.T("help.interactive_uncertain"))
	}
	if ctx.Message != nil && ctx.Message.ID > 0 {
		_ = ctx.Messages().Delete()
	}
	return true, nil
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

func (p *Plugin) renderCategoryCard(ctx *core.Context, cat string, cmds []core.Command, prefix string) string {
	var sb strings.Builder
	for _, c := range cmds {
		desc := c.Description
		if desc == "" {
			desc = ctx.T("common.no_description")
		}
		sb.WriteString(fmt.Sprintf("• <code>%s%s</code> — %s\n", prefix, c.Name, ui.EscapeHTML(desc)))
	}

	card := ui.NewCard(ctx.T("help.module_title", cat)).
		WithIcon("📂").
		WithHeader(ctx.T("help.commands_available", len(cmds))).
		WithRaw(sb.String()).
		WithFooter(ctx.T("help.tip_details", prefix))
	return card.Render()
}

func (p *Plugin) renderOverviewWithCategories(ctx *core.Context, prefix string, categories map[string][]core.Command, catNames []string, source execution.Source) string {
	all := p.commandsForSource(source)
	var sb strings.Builder
	sb.WriteString("📚 <b>" + ui.EscapeHTML(ctx.T("help.title")) + "</b>\n")
	sb.WriteString("<i>" + ui.EscapeHTML(ctx.T("help.summary", len(all), len(catNames))) + "</i>\n\n")

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

	sb.WriteString(ctx.T("help.tip_overview", prefix, prefix))
	return strings.TrimSpace(sb.String())
}
