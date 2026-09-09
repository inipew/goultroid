package wikipedia

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/plugin"
)

type Plugin struct {
	http *network.Service
}

func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) Name() string {
	return "wikipedia"
}

func (p *Plugin) Description() string {
	return "Search and summarize Wikipedia articles"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	svc, err := pctx.HTTP()
	if err != nil {
		return err
	}
	p.http = svc
	return nil
}

func (p *Plugin) SetHTTP(svc *network.Service) {
	p.http = svc
}

func (p *Plugin) Shutdown() error { return nil }

func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{{
		ID:          "wikipedia",
		Name:        "Wikipedia",
		Description: "Wikipedia article search and summaries",
		Category:    "Information",
		Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
	}}
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{{
		Name:        "wiki",
		Aliases:     []string{"wikipedia"},
		Description: "Search Wikipedia and show an article summary",
		Usage:       ".wiki <query>",
		Category:    "Information",
		Permission:  core.PermissionEveryone,
		Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		Handler:     p.handle,
	}}
}

type searchResponse struct {
	Pages []struct {
		Key string `json:"key"`
	} `json:"pages"`
}

type summaryResponse struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Extract     string `json:"extract"`
}

func (p *Plugin) handle(ctx *core.Context) error {
	query := strings.TrimSpace(ctx.RawArgs)
	if query == "" {
		return ctx.EditOrReply("ℹ️ Usage: .wiki <query>")
	}
	_ = ctx.EditOrReply("🔎 Searching Wikipedia for <code>" + core.EscapeHTML(query) + "</code>...")
	page, err := p.search(ctx.Ctx, query)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Wikipedia search failed: %v", err))
	}
	if page == "" {
		return ctx.EditOrReply("ℹ️ No Wikipedia article found for that query.")
	}
	summary, err := p.summary(ctx.Ctx, page)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Wikipedia lookup failed: %v", err))
	}
	text := strings.TrimSpace(summary.Extract)
	if text == "" {
		return ctx.EditOrReply("ℹ️ Wikipedia returned no article summary.")
	}
	if len([]rune(text)) > 3800 {
		text = string([]rune(text)[:3800]) + "…"
	}
	out := "<b>Wikipedia: " + core.EscapeHTML(summary.Title) + "</b>"
	if summary.Description != "" {
		out += "\n<i>" + core.EscapeHTML(summary.Description) + "</i>"
	}
	out += "\n\n" + core.EscapeHTML(text)
	return ctx.EditOrReply(out)
}

func (p *Plugin) search(ctx context.Context, q string) (string, error) {
	var out searchResponse
	u := "https://en.wikipedia.org/w/rest.php/v1/search/page?q=" + url.QueryEscape(q) + "&limit=1"
	if err := p.getJSON(ctx, u, &out); err != nil {
		return "", err
	}
	if len(out.Pages) == 0 {
		return "", nil
	}
	return out.Pages[0].Key, nil
}

func (p *Plugin) summary(ctx context.Context, title string) (summaryResponse, error) {
	var out summaryResponse
	u := "https://en.wikipedia.org/api/rest_v1/page/summary/" + url.PathEscape(title)
	return out, p.getJSON(ctx, u, &out)
}

func (p *Plugin) getJSON(ctx context.Context, endpoint string, out any) error {
	if p.http == nil {
		p.http = network.NewService(nil, nil)
	}
	resp, err := p.http.Get(ctx, "wikipedia", endpoint, map[string]string{
		"User-Agent": "Goultroid/1.0 (Wikipedia plugin)",
		"Accept":     "application/json",
	})
	if err != nil {
		return err
	}
	defer resp.Close()
	if resp.StatusCode != network.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}
