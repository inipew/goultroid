package shell

import (
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/feature"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/ui"
)

// InlineBindings binds the shell's canonical InteractionInline declarations to
// their stateless implementations. Plugin lifecycle owns registration/removal.
func (f *Feature) InlineBindings() []inlineservice.Binding {
	if f == nil {
		return nil
	}
	return []inlineservice.Binding{
		{InteractionID: InteractionInlineRoot, Handler: &inlineRootHandler{startTime: f.startTime}},
		{InteractionID: InteractionInlineHelp, Handler: &inlineHelpHandler{catalog: f.inlineCatalog}},
		{InteractionID: InteractionInlinePing, Handler: &inlinePingHandler{startTime: f.startTime}},
	}
}

type inlineRootHandler struct {
	startTime time.Time
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

func (h *inlineRootHandler) HandleInlineV2(*inlineservice.InlineContext) (*inlineservice.InlineResponse, error) {
	uptime := inlineUptime(h.startTime)
	text := fmt.Sprintf(
		"⚡ <b>GoUltroid Inline Assistant</b>\n\n• <b>Status:</b> Active\n• <b>Uptime:</b> %s\n\nType <code>@bot help</code> to search feature commands or <code>@bot ping</code> for status.",
		ui.EscapeHTML(uptime),
	)
	markup := ui.NewMarkup(ui.ButtonRow{
		ui.NewSwitchInlineButton("🔍 Search Help", "help ", false),
		ui.NewSwitchInlineButton("🏓 Ping Status", "ping", false),
	})
	return &inlineservice.InlineResponse{
		Results: []inlineservice.InlineResult{{
			ID:          "default_menu",
			Title:       "GoUltroid Assistant",
			Description: "Search feature commands and view runtime status",
			Text:        text,
			Markup:      &markup,
		}},
		Cache: inlineservice.CacheNone,
	}, nil
}

type inlinePingHandler struct {
	startTime time.Time
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

func (h *inlinePingHandler) HandleInlineV2(*inlineservice.InlineContext) (*inlineservice.InlineResponse, error) {
	uptime := inlineUptime(h.startTime)
	markup := ui.NewMarkup(ui.ButtonRow{
		ui.NewSwitchInlineButton("Help Menu", "help", false),
	})
	return &inlineservice.InlineResponse{
		Results: []inlineservice.InlineResult{{
			ID:          "ping_status",
			Title:       "GoUltroid Status",
			Description: "Online | Uptime " + uptime,
			Text:        fmt.Sprintf("🏓 <b>GoUltroid Status</b>\n\n• <b>Status:</b> Online\n• <b>Uptime:</b> %s", ui.EscapeHTML(uptime)),
			Markup:      &markup,
		}},
		Cache: inlineservice.CacheNone,
	}, nil
}

type inlineHelpHandler struct {
	catalog feature.Catalog
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
					description = "No description"
				}
				usage := strings.TrimSpace(command.Usage)
				if usage == "" {
					usage = command.Name
				}
				text := fmt.Sprintf(
					"<b>Feature:</b> %s\n<b>Command:</b> <code>%s</code>\n<b>Description:</b> %s\n<b>Usage:</b> <code>%s</code>",
					ui.EscapeHTML(entry.Spec.Name),
					ui.EscapeHTML(command.Name),
					ui.EscapeHTML(description),
					ui.EscapeHTML(usage),
				)
				markup := ui.NewMarkup(ui.ButtonRow{
					ui.NewSwitchInlineButton("Search More", "help ", false),
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
		message := "No commands matched the current feature catalog."
		if h.catalog == nil {
			message = "Feature catalog is unavailable."
		}
		results = append(results, inlineservice.InlineResult{
			ID:          "help_empty",
			Title:       "GoUltroid Help",
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
