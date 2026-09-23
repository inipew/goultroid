package shell

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/feature"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/ui"
)

// InlineBindings binds the shell's canonical InteractionInline declarations to
// their stateless implementations. Plugin lifecycle owns registration/removal.
func (f *Feature) InlineBindings() []inlineservice.Binding {
	if f == nil {
		return nil
	}
	return []inlineservice.Binding{
		{InteractionID: InteractionInlineRoot, Handler: &inlineRootHandler{startTime: f.startTime, settingsSvc: f.settingsSvc}},
		{InteractionID: InteractionInlineHelp, Handler: &inlineHelpHandler{catalog: f.inlineCatalog, settingsSvc: f.settingsSvc}},
		{InteractionID: InteractionInlinePing, Handler: &inlinePingHandler{startTime: f.startTime, settingsSvc: f.settingsSvc}},
	}
}

func inlineContextLocale(ctx *inlineservice.InlineContext, svc *settings.Service) string {
	if ctx == nil {
		return ResolveLocale(context.Background(), svc, 0, 0)
	}
	return ResolveLocale(ctx.Ctx, svc, ctx.UserID, 0)
}

type inlineRootHandler struct {
	startTime   time.Time
	settingsSvc *settings.Service
}

func (*inlineRootHandler) Pattern() string                      { return "" }
func (*inlineRootHandler) Description() string                  { return "GoUltroid interactive inline assistant" }
func (*inlineRootHandler) Matcher() inlineservice.InlineMatcher { return nil }
func (*inlineRootHandler) AccessPolicy() inlineservice.InlineAccessPolicy {
	return inlineservice.InlineAccessPolicy{}
}
func (*inlineRootHandler) CachePolicy() inlineservice.CachePolicy { return inlineservice.CacheNone }

func (h *inlineRootHandler) HandleInline(ctx *inlineservice.InlineContext) ([]inlineservice.InlineResult, error) {
	response, err := h.HandleInlineV2(ctx)
	if err != nil || response == nil {
		return nil, err
	}
	return response.Results, nil
}

func (h *inlineRootHandler) HandleInlineV2(ctx *inlineservice.InlineContext) (*inlineservice.InlineResponse, error) {
	locale := inlineContextLocale(ctx, h.settingsSvc)
	uptime := inlineUptime(h.startTime)
	text := fmt.Sprintf(
		"⚡ <b>%s</b>

• <b>%s:</b> %s
• <b>%s:</b> %s

%s",
		tr(locale, "assistant.inline.title"),
		tr(locale, "assistant.field.status"),
		tr(locale, "assistant.inline.active"),
		tr(locale, "assistant.field.uptime"),
		ui.EscapeHTML(uptime),
		tr(locale, "assistant.inline.hint"),
	)
	markup := ui.NewMarkup(ui.ButtonRow{
		ui.NewSwitchInlineButton(tr(locale, "assistant.inline.search_help"), "help ", false),
		ui.NewSwitchInlineButton(tr(locale, "assistant.inline.ping_status"), "ping", false),
	})
	return &inlineservice.InlineResponse{
		Results: []inlineservice.InlineResult{{
			ID:          "default_menu",
			Title:       tr(locale, "assistant.inline.title"),
			Description: tr(locale, "assistant.inline.description"),
			Text:        text,
			Markup:      &markup,
		}},
		Cache: inlineservice.CacheNone,
	}, nil
}

type inlinePingHandler struct {
	startTime   time.Time
	settingsSvc *settings.Service
}

func (*inlinePingHandler) Pattern() string                      { return "ping" }
func (*inlinePingHandler) Description() string                  { return "Check userbot uptime and liveness" }
func (*inlinePingHandler) Matcher() inlineservice.InlineMatcher { return nil }
func (*inlinePingHandler) AccessPolicy() inlineservice.InlineAccessPolicy {
	return inlineservice.InlineAccessPolicy{}
}
func (*inlinePingHandler) CachePolicy() inlineservice.CachePolicy { return inlineservice.CacheNone }

func (h *inlinePingHandler) HandleInline(ctx *inlineservice.InlineContext) ([]inlineservice.InlineResult, error) {
	response, err := h.HandleInlineV2(ctx)
	if err != nil || response == nil {
		return nil, err
	}
	return response.Results, nil
}

func (h *inlinePingHandler) HandleInlineV2(ctx *inlineservice.InlineContext) (*inlineservice.InlineResponse, error) {
	locale := inlineContextLocale(ctx, h.settingsSvc)
	uptime := inlineUptime(h.startTime)
	markup := ui.NewMarkup(ui.ButtonRow{
		ui.NewSwitchInlineButton(tr(locale, "assistant.inline.help_menu"), "help", false),
	})
	return &inlineservice.InlineResponse{
		Results: []inlineservice.InlineResult{{
			ID:          "ping_status",
			Title:       tr(locale, "assistant.inline.status_title"),
			Description: tr(locale, "assistant.inline.online_uptime", uptime),
			Text: fmt.Sprintf("🏓 <b>%s</b>

• <b>%s:</b> %s
• <b>%s:</b> %s",
				tr(locale, "assistant.inline.status_title"),
				tr(locale, "assistant.field.status"),
				tr(locale, "assistant.inline.online"),
				tr(locale, "assistant.field.uptime"),
				ui.EscapeHTML(uptime),
			),
			Markup: &markup,
		}},
		Cache: inlineservice.CacheNone,
	}, nil
}

type inlineHelpHandler struct {
	catalog     feature.Catalog
	settingsSvc *settings.Service
}

func (*inlineHelpHandler) Pattern() string                      { return "help" }
func (*inlineHelpHandler) Description() string                  { return "Search canonical feature commands" }
func (*inlineHelpHandler) Matcher() inlineservice.InlineMatcher { return nil }
func (*inlineHelpHandler) AccessPolicy() inlineservice.InlineAccessPolicy {
	return inlineservice.InlineAccessPolicy{}
}
func (*inlineHelpHandler) CachePolicy() inlineservice.CachePolicy { return inlineservice.CacheNone }

func (h *inlineHelpHandler) HandleInline(ctx *inlineservice.InlineContext) ([]inlineservice.InlineResult, error) {
	response, err := h.HandleInlineV2(ctx)
	if err != nil || response == nil {
		return nil, err
	}
	return response.Results, nil
}

func (h *inlineHelpHandler) HandleInlineV2(ctx *inlineservice.InlineContext) (*inlineservice.InlineResponse, error) {
	locale := inlineContextLocale(ctx, h.settingsSvc)
	search := ""
	if ctx != nil {
		search = strings.ToLower(strings.TrimSpace(strings.Join(ctx.Args, " ")))
	}

	results := make([]inlineservice.InlineResult, 0, 24)
	if h.catalog != nil {
		for _, entry := range h.catalog.All() {
			for _, command := range entry.Spec.Commands {
				if search != "" && !inlineHelpMatch(search, entry, command) {
					continue
				}
				description := strings.TrimSpace(command.Description)
				if description == "" {
					description = tr(locale, "assistant.inline.no_description")
				}
				usage := strings.TrimSpace(command.Usage)
				if usage == "" {
					usage = command.Name
				}
				text := fmt.Sprintf(
					"<b>%s:</b> %s
<b>%s:</b> <code>%s</code>
<b>%s:</b> %s
<b>%s:</b> <code>%s</code>",
					tr(locale, "assistant.inline.feature"),
					ui.EscapeHTML(entry.Spec.Name),
					tr(locale, "assistant.inline.command"),
					ui.EscapeHTML(command.Name),
					tr(locale, "assistant.inline.description_field"),
					ui.EscapeHTML(description),
					tr(locale, "assistant.help.usage"),
					ui.EscapeHTML(usage),
				)
				markup := ui.NewMarkup(ui.ButtonRow{
					ui.NewSwitchInlineButton(tr(locale, "assistant.inline.search_more"), "help ", false),
				})
				results = append(results, inlineservice.InlineResult{
					ID:          fmt.Sprintf("help_%d", len(results)),
					Title:       command.Name,
					Description: description,
					Text:        text,
					Markup:      &markup,
				})
				if len(results) == 50 {
					break
				}
			}
			if len(results) == 50 {
				break
			}
		}
	}

	if len(results) == 0 {
		message := tr(locale, "assistant.inline.no_match")
		if h.catalog == nil {
			message = tr(locale, "assistant.inline.catalog_unavailable")
		}
		results = append(results, inlineservice.InlineResult{
			ID:          "help_empty",
			Title:       tr(locale, "assistant.inline.help_title"),
			Description: message,
			Text:        ui.EscapeHTML(message),
		})
	}

	return &inlineservice.InlineResponse{
		Results: results,
		Cache:   inlineservice.CacheNone,
		Private: true,
	}, nil
}

func inlineHelpMatch(search string, entry feature.Entry, command feature.CommandSurface) bool {
	values := []string{
		entry.Spec.ID,
		entry.Spec.Name,
		entry.Spec.Description,
		command.Name,
		command.Description,
		command.Usage,
		command.Category,
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), search) {
			return true
		}
	}
	return false
}

func inlineUptime(start time.Time) string {
	if start.IsZero() {
		return "unknown"
	}
	return time.Since(start).Truncate(time.Second).String()
}
