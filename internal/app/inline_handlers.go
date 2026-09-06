package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/ui"
)

type defaultHelpInlineHandler struct {
	router *core.Router
}

func (h *defaultHelpInlineHandler) Pattern() string {
	return "help"
}

func (h *defaultHelpInlineHandler) Description() string {
	return "Search and view userbot commands"
}

func (h *defaultHelpInlineHandler) HandleInline(ctx *inline.InlineContext) ([]inline.InlineResult, error) {
	if h.router == nil {
		return nil, nil
	}

	search := ""
	if len(ctx.Args) > 0 {
		search = strings.ToLower(ctx.Args[0])
	}

	var results []inline.InlineResult
	for _, cmd := range h.router.All() {
		if search != "" && !strings.Contains(strings.ToLower(cmd.Name), search) && !strings.Contains(strings.ToLower(cmd.Description), search) {
			continue
		}

		resText := fmt.Sprintf("<b>Command:</b> <code>.%s</code>\n<b>Description:</b> %s\n<b>Usage:</b> <code>%s</code>",
			cmd.Name, cmd.Description, cmd.Usage)

		markup := ui.NewMarkup(ui.ButtonRow{
			ui.NewSwitchInlineButton("Search More", "help ", false),
		})

		results = append(results, inline.InlineResult{
			ID:          fmt.Sprintf("help_%s", cmd.Name),
			Title:       fmt.Sprintf(".%s", cmd.Name),
			Description: cmd.Description,
			Text:        resText,
			Markup:      &markup,
		})
	}

	return results, nil
}

type defaultPingInlineHandler struct {
	startTime time.Time
}

func (p *defaultPingInlineHandler) Pattern() string {
	return "ping"
}

func (p *defaultPingInlineHandler) Description() string {
	return "Check userbot uptime and latency"
}

func (p *defaultPingInlineHandler) HandleInline(ctx *inline.InlineContext) ([]inline.InlineResult, error) {
	uptime := time.Since(p.startTime).Truncate(time.Second)

	text := fmt.Sprintf("🏓 <b>GoUltroid Userbot Status</b>\n\n• <b>Status:</b> Online\n• <b>Uptime:</b> %s\n• <b>Core:</b> Parity Phase 1 Active", uptime)
	markup := ui.NewMarkup(ui.ButtonRow{
		ui.NewSwitchInlineButton("Help Menu", "help", false),
	})

	return []inline.InlineResult{
		{
			ID:          "ping_status",
			Title:       "GoUltroid Status",
			Description: fmt.Sprintf("Online | Uptime %s", uptime),
			Text:        text,
			Markup:      &markup,
		},
	}, nil
}

type defaultCatchAllInlineHandler struct {
	router    *core.Router
	startTime time.Time
}

func (c *defaultCatchAllInlineHandler) Pattern() string {
	return ""
}

func (c *defaultCatchAllInlineHandler) Description() string {
	return "GoUltroid interactive inline assistant"
}

func (c *defaultCatchAllInlineHandler) HandleInline(ctx *inline.InlineContext) ([]inline.InlineResult, error) {
	uptime := time.Since(c.startTime).Truncate(time.Second)
	text := fmt.Sprintf("⚡ <b>GoUltroid Inline Assistant</b>\n\n• <b>Status:</b> Active\n• <b>Uptime:</b> %s\n\nType <code>@bot help</code> to search commands or <code>@bot ping</code> for status.", uptime)

	markup := ui.NewMarkup(ui.ButtonRow{
		ui.NewSwitchInlineButton("🔍 Search Help", "help ", false),
		ui.NewSwitchInlineButton("🏓 Ping Status", "ping", false),
	})

	return []inline.InlineResult{
		{
			ID:          "default_menu",
			Title:       "GoUltroid Assistant",
			Description: "Search commands, view uptime, and manage userbot",
			Text:        text,
			Markup:      &markup,
		},
	}, nil
}
