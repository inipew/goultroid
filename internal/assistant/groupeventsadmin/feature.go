package groupeventsadmin

import (
	"fmt"
	"strings"

	"github.com/inipew/goultroid/internal/assistant/groupevents"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
)

const FeatureID = "assistant_group_events"

type Feature struct {
	service *groupevents.Service
}

func New(service *groupevents.Service) *Feature {
	return &Feature{service: service}
}

func (*Feature) Name() string { return FeatureID }
func (*Feature) Init() error  { return nil }

func (*Feature) FeatureSpec() feature.Spec {
	return feature.Spec{
		ID:          FeatureID,
		Name:        "Assistant Group Events",
		Description: "Chat-scoped welcome and goodbye service-message automation.",
		Category:    "Assistant",
	}
}

func (f *Feature) Commands() []core.Command {
	policy := core.InvocationPolicy{Assistant: core.InvocationAnyone}
	auth := core.GroupAuthorizationRequirement{Level: core.GroupAuthorizationAdministrator}
	return []core.Command{
		{
			Name:               "welcome",
			Description:        "Configure Assistant welcome messages for this group",
			Usage:              "/welcome [status|on [template]|off|set <template>|reset]",
			Category:           "Assistant",
			Permission:         core.PermissionEveryone,
			Invocation:         policy,
			Surfaces:           execution.SurfaceAssistant,
			GroupOnly:          true,
			GroupAuthorization: auth,
			Handler: func(ctx *core.Context) error {
				return f.handle(ctx, core.GroupServiceMemberJoined, "welcome")
			},
		},
		{
			Name:               "goodbye",
			Aliases:            []string{"farewell"},
			Description:        "Configure Assistant goodbye messages for this group",
			Usage:              "/goodbye [status|on [template]|off|set <template>|reset]",
			Category:           "Assistant",
			Permission:         core.PermissionEveryone,
			Invocation:         policy,
			Surfaces:           execution.SurfaceAssistant,
			GroupOnly:          true,
			GroupAuthorization: auth,
			Handler: func(ctx *core.Context) error {
				return f.handle(ctx, core.GroupServiceMemberLeft, "goodbye")
			},
		},
	}
}

func titleLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

func (f *Feature) handle(ctx *core.Context, kind core.GroupServiceKind, label string) error {
	if f == nil || f.service == nil {
		return groupevents.ErrUnavailable
	}
	if ctx == nil || ctx.Chat == nil {
		return core.ErrGroupOnly
	}

	if len(ctx.Args) == 0 || strings.EqualFold(ctx.Args[0], "status") {
		return f.replyStatus(ctx, kind, label)
	}

	switch strings.ToLower(strings.TrimSpace(ctx.Args[0])) {
	case "on", "enable", "enabled":
		template := strings.TrimSpace(strings.Join(ctx.Args[1:], " "))
		state, err := f.service.Configure(ctx, kind, true, template)
		if err != nil {
			return err
		}
		return ctx.Reply(renderConfigured(label, state))
	case "off", "disable", "disabled":
		state, err := f.service.Configure(ctx, kind, false, "")
		if err != nil {
			return err
		}
		return ctx.Reply(renderConfigured(label, state))
	case "set":
		template := strings.TrimSpace(strings.Join(ctx.Args[1:], " "))
		if template == "" {
			return ctx.Reply(fmt.Sprintf(
				"⚠️ Usage: <code>/%s set &lt;template&gt;</code>",
				label,
			))
		}
		state, err := f.service.Configure(ctx, kind, true, template)
		if err != nil {
			return err
		}
		return ctx.Reply(renderConfigured(label, state))
	case "reset":
		state, err := f.service.Reset(ctx, kind)
		if err != nil {
			return err
		}
		return ctx.Reply(renderConfigured(label, state))
	default:
		return ctx.Reply(fmt.Sprintf(
			"⚠️ <b>%s usage</b>\n"+
				"<code>/%s</code> — status\n"+
				"<code>/%s on [template]</code> — enable\n"+
				"<code>/%s off</code> — disable\n"+
				"<code>/%s set &lt;template&gt;</code> — set text and enable\n"+
				"<code>/%s reset</code> — restore default template\n\n"+
				"Variables: <code>{user}</code>, <code>{user_id}</code>, <code>{chat}</code>, <code>{count}</code>",
			titleLabel(label),
			label,
			label,
			label,
			label,
			label,
		))
	}
}

func (f *Feature) replyStatus(ctx *core.Context, kind core.GroupServiceKind, label string) error {
	state, err := f.service.StateContext(ctx.Ctx, ctx.Chat.ID, kind)
	if err != nil {
		return err
	}
	status := "disabled"
	if state.Config.Enabled {
		status = "enabled"
	}
	return ctx.Reply(fmt.Sprintf(
		"👋 <b>%s</b>\n\n"+
			"• <b>Status:</b> <code>%s</code>\n"+
			"• <b>Revision:</b> <code>%d</code>\n"+
			"• <b>Template:</b> %s\n\n"+
			"Variables: <code>{user}</code>, <code>{user_id}</code>, <code>{chat}</code>, <code>{count}</code>",
		core.EscapeHTML(titleLabel(label)),
		status,
		state.Revision,
		core.EscapeHTML(state.Config.Template),
	))
}
func renderConfigured(label string, state groupevents.State) string {
	status := "disabled"
	if state.Config.Enabled {
		status = "enabled"
	}
	return fmt.Sprintf(
		"✅ <b>%s updated</b>\n"+
			"• <b>Status:</b> <code>%s</code>\n"+
			"• <b>Revision:</b> <code>%d</code>\n"+
			"• <b>Template:</b> %s",
		core.EscapeHTML(titleLabel(label)),
		status,
		state.Revision,
		core.EscapeHTML(state.Config.Template),
	)
}
var _ interface{ FeatureSpec() feature.Spec } = (*Feature)(nil)
