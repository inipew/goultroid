package wikipedia

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/url"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/plugin"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
)

const (
	inlineInteractionID       = "lookup"
	maxLookupQueryBytes       = 256
	maxInlineResults          = 5
	inlineCacheTimeSeconds    = 60
	maxInlineExcerptRunes     = 700
	maxInlineDescriptionRunes = 180
)

type Plugin struct {
	http *network.Client
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
	p.http = svc.ForOwner("wikipedia")
}

func (p *Plugin) Shutdown() error { return nil }

func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{{
		ID:          "wikipedia",
		Name:        "Wikipedia",
		Description: "Wikipedia article search and summaries",
		Category:    "Information",
		Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant | execution.SurfaceInline,
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

func (p *Plugin) FeatureSpec() feature.Spec {
	return feature.Spec{
		ID:          p.Name(),
		Name:        "Wikipedia",
		Description: p.Description(),
		Category:    "Information",
		Interactions: []feature.Interaction{{
			ID:          inlineInteractionID,
			Kind:        feature.InteractionInline,
			Description: "Rich Wikipedia article lookup",
			Surfaces:    execution.SurfaceInline,
			Policy:      feature.PublicPolicy(execution.SurfaceInline),
		}},
	}
}

func (p *Plugin) InlineBindings() []inlineservice.Binding {
	return []inlineservice.Binding{{
		InteractionID: inlineInteractionID,
		Handler:       &inlineHandler{lookup: p},
		Priority:      10,
	}}
}

type searchThumbnail struct {
	URL string `json:"url"`
}

type searchPage struct {
	Key         string           `json:"key"`
	Title       string           `json:"title"`
	Excerpt     string           `json:"excerpt"`
	Description string           `json:"description"`
	Thumbnail   *searchThumbnail `json:"thumbnail"`
}

type searchResponse struct {
	Pages []searchPage `json:"pages"`
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
	_ = ctx.Progress("Searching Wikipedia for <code>" + core.EscapeHTML(query) + "</code>...")
	page, err := p.search(ctx.Ctx, query)
	if err != nil {
		return ctx.Error(fmt.Sprintf("Wikipedia search failed: %v", err))
	}
	if page == "" {
		return ctx.EditOrReply("ℹ️ No Wikipedia article found for that query.")
	}
	summary, err := p.summary(ctx.Ctx, page)
	if err != nil {
		return ctx.Error(fmt.Sprintf("Wikipedia lookup failed: %v", err))
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
	return ctx.Result(out)
}

func (p *Plugin) search(ctx context.Context, q string) (string, error) {
	pages, err := p.searchPages(ctx, q, 1)
	if err != nil {
		return "", err
	}
	if len(pages) == 0 {
		return "", nil
	}
	return pages[0].Key, nil
}

func (p *Plugin) searchPages(ctx context.Context, q string, limit int) ([]searchPage, error) {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > maxLookupQueryBytes {
		return nil, fmt.Errorf("%w: Wikipedia query must contain 1..%d bytes", core.ErrInvalidArgs, maxLookupQueryBytes)
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > maxInlineResults {
		limit = maxInlineResults
	}
	var out searchResponse
	u := fmt.Sprintf("https://en.wikipedia.org/w/rest.php/v1/search/page?q=%s&limit=%d", url.QueryEscape(q), limit)
	if err := p.getJSON(ctx, u, &out); err != nil {
		return nil, err
	}
	if len(out.Pages) > limit {
		out.Pages = out.Pages[:limit]
	}
	return out.Pages, nil
}

func (p *Plugin) summary(ctx context.Context, title string) (summaryResponse, error) {
	var out summaryResponse
	u := "https://en.wikipedia.org/api/rest_v1/page/summary/" + url.PathEscape(title)
	return out, p.getJSON(ctx, u, &out)
}

func (p *Plugin) getJSON(ctx context.Context, endpoint string, out any) error {
	if p == nil || p.http == nil {
		return fmt.Errorf("%w: Wikipedia HTTP capability is unavailable", core.ErrUnavailable)
	}
	resp, err := p.http.Get(ctx, endpoint, map[string]string{
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

type pageLookup interface {
	searchPages(context.Context, string, int) ([]searchPage, error)
}

type inlineHandler struct {
	lookup pageLookup
}

type wikiMatcher struct{}

func (wikiMatcher) Match(query string) ([]string, bool) {
	trimmed := strings.TrimSpace(query)
	if strings.EqualFold(trimmed, "wiki") {
		return nil, true
	}
	if len(trimmed) <= len("wiki") || !strings.EqualFold(trimmed[:len("wiki")], "wiki") {
		return nil, false
	}
	if trimmed[len("wiki")] != ' ' && trimmed[len("wiki")] != '\t' {
		return nil, false
	}
	return strings.Fields(strings.TrimSpace(trimmed[len("wiki"):])), true
}

func (*inlineHandler) Pattern() string     { return "wiki" }
func (*inlineHandler) Description() string { return "Search Wikipedia articles" }
func (*inlineHandler) Matcher() inlineservice.InlineMatcher {
	return wikiMatcher{}
}
func (*inlineHandler) AccessPolicy() inlineservice.InlineAccessPolicy {
	return inlineservice.InlineAccessPolicy{}
}
func (*inlineHandler) CachePolicy() inlineservice.CachePolicy { return inlineservice.CacheGlobal }

func (h *inlineHandler) HandleInline(ctx *inlineservice.InlineContext) ([]inlineservice.InlineResult, error) {
	response, err := h.HandleInlineV2(ctx)
	if err != nil {
		return nil, err
	}
	return response.Results, nil
}

func (h *inlineHandler) HandleInlineV2(ctx *inlineservice.InlineContext) (*inlineservice.InlineResponse, error) {
	if ctx == nil || h == nil || h.lookup == nil {
		return nil, core.ErrInvalidArgs
	}
	query := strings.TrimSpace(strings.Join(ctx.Args, " "))
	if query == "" {
		return wikipediaInlineResponse([]inlineservice.InlineResult{{
			ID:          "wiki-help",
			Type:        inlineservice.ResultArticle,
			Title:       "Wikipedia search",
			Description: "Type: wiki <query>",
			Text:        "<b>Wikipedia search</b>\nType <code>wiki &lt;query&gt;</code> in inline mode.",
		}}), nil
	}
	if len(query) > maxLookupQueryBytes {
		return nil, fmt.Errorf("%w: Wikipedia query exceeds %d bytes", core.ErrInvalidArgs, maxLookupQueryBytes)
	}
	pages, err := h.lookup.searchPages(ctx.Ctx, query, maxInlineResults)
	if err != nil {
		return nil, err
	}
	if len(pages) > maxInlineResults {
		pages = pages[:maxInlineResults]
	}
	results := make([]inlineservice.InlineResult, 0, len(pages))
	for _, page := range pages {
		if result, ok := wikipediaInlineResult(page, len(results)); ok {
			results = append(results, result)
		}
	}
	if len(results) == 0 {
		results = append(results, inlineservice.InlineResult{
			ID:          "wiki-empty",
			Type:        inlineservice.ResultArticle,
			Title:       "No Wikipedia results",
			Description: "Try a different search query",
			Text:        "ℹ️ No Wikipedia article matched <code>" + core.EscapeHTML(query) + "</code>.",
		})
	}
	return wikipediaInlineResponse(results), nil
}

func wikipediaInlineResponse(results []inlineservice.InlineResult) *inlineservice.InlineResponse {
	return &inlineservice.InlineResponse{
		Results:   results,
		Cache:     inlineservice.CacheGlobal,
		CacheTime: inlineCacheTimeSeconds,
		Private:   false,
	}
}

func wikipediaInlineResult(page searchPage, index int) (inlineservice.InlineResult, bool) {
	key := strings.TrimSpace(page.Key)
	title := strings.TrimSpace(page.Title)
	if key == "" {
		key = title
	}
	if title == "" {
		title = strings.ReplaceAll(key, "_", " ")
	}
	if key == "" || title == "" {
		return inlineservice.InlineResult{}, false
	}
	description := cleanSearchText(page.Description, maxInlineDescriptionRunes)
	excerpt := cleanSearchText(page.Excerpt, maxInlineExcerptRunes)
	if description == "" {
		description = truncateRunes(excerpt, maxInlineDescriptionRunes)
	}
	articleURL := "https://en.wikipedia.org/wiki/" + url.PathEscape(key)
	text := "<b>Wikipedia: " + core.EscapeHTML(title) + "</b>"
	if description != "" {
		text += "\n<i>" + core.EscapeHTML(description) + "</i>"
	}
	if excerpt != "" {
		text += "\n\n" + core.EscapeHTML(excerpt)
	}
	text += "\n\n<a href=\"" + core.EscapeHTML(articleURL) + "\">Read on Wikipedia</a>"

	thumbURL := ""
	if page.Thumbnail != nil {
		thumbURL = normalizeThumbnailURL(page.Thumbnail.URL)
	}
	return inlineservice.InlineResult{
		ID:          fmt.Sprintf("wiki-%d", index+1),
		Type:        inlineservice.ResultArticle,
		Title:       title,
		Description: description,
		Text:        text,
		ThumbURL:    thumbURL,
		URL:         articleURL,
	}, true
}

func cleanSearchText(raw string, maxRunes int) string {
	var b strings.Builder
	b.Grow(len(raw))
	inTag := false
	for _, r := range raw {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	return truncateRunes(strings.Join(strings.Fields(html.UnescapeString(b.String())), " "), maxRunes)
}

func truncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

func normalizeThumbnailURL(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	if strings.HasPrefix(value, "https://") {
		return value
	}
	return ""
}

var _ inlineservice.InlineHandlerV2 = (*inlineHandler)(nil)
