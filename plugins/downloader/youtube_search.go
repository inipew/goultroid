package downloader

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/download"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/ui"
)

const youtubeInlineSearchTimeout = 3 * time.Second

func matchDownloaderKeyword(query, keyword string) ([]string, bool) {
	trimmed := strings.TrimSpace(query)
	if strings.EqualFold(trimmed, keyword) {
		return nil, true
	}
	if len(trimmed) <= len(keyword) || !strings.EqualFold(trimmed[:len(keyword)], keyword) {
		return nil, false
	}
	if trimmed[len(keyword)] != ' ' && trimmed[len(keyword)] != '\t' {
		return nil, false
	}
	return strings.Fields(strings.TrimSpace(trimmed[len(keyword):])), true
}

func isYouTubeSearchQuery(raw string) bool {
	_, ok := matchDownloaderKeyword(raw, "yt")
	return ok
}

func (p *Plugin) searchYouTube(ctx context.Context, query string) ([]download.SearchResult, error) {
	if p == nil {
		return nil, core.ErrInvalidArgs
	}
	query = strings.TrimSpace(query)
	if query == "" || len(query) > download.MaxSearchQueryBytes {
		return nil, fmt.Errorf("%w: YouTube search query must contain 1..%d bytes", core.ErrInvalidArgs, download.MaxSearchQueryBytes)
	}
	p.ensureRegistry()
	if p.registry == nil {
		return nil, fmt.Errorf("%w: downloader registry unavailable", core.ErrUnavailable)
	}
	if p.tasks == nil {
		return nil, fmt.Errorf("%w: downloader TaskEngine client is not configured", core.ErrUnavailable)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var (
		results   []download.SearchResult
		searchErr error
	)
	ticket, err := p.tasks.Submit(ctx, tasks.WorkSpec{
		ID:               p.nextTaskID("youtube-search"),
		QuotaOwner:       tasks.OwnerID("plugin:downloader"),
		Pool:             tasks.PoolID("download"),
		Class:            tasks.PriorityInteractive,
		ExecutionTimeout: youtubeInlineSearchTimeout,
		Resources:        []tasks.ResourceRequirement{{Name: "process", Amount: 1}},
		Handler: func(taskCtx context.Context) error {
			results, searchErr = p.registry.Search(taskCtx, "extractor", query, download.SearchOptions{
				Limit:   download.MaxSearchLimit,
				Timeout: youtubeInlineSearchTimeout,
			})
			return searchErr
		},
	})
	if err != nil {
		return nil, fmt.Errorf("submit YouTube search task: %w", err)
	}

	taskResult, waitErr := ticket.Wait(ctx)
	if waitErr != nil {
		cause := tasks.CauseUserCancel
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cause = tasks.CauseTimeout
		}
		_, _ = p.tasks.Cancel(ticket.TaskID(), cause)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("wait YouTube search task: %w", waitErr)
	}
	if !taskResult.IsSuccess() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if searchErr != nil {
			return nil, searchErr
		}
		return nil, fmt.Errorf("%w: %s", download.ErrSearchFailed, taskResult.Failure.Message)
	}
	if searchErr != nil {
		return nil, searchErr
	}
	return results, nil
}

func (h *interactiveInlineHandler) handleYouTubeSearch(ctx *inlineservice.InlineContext) (*inlineservice.InlineResponse, error) {
	query := strings.TrimSpace(strings.Join(ctx.Args, " "))
	if query == "" {
		return interactiveInlineResponse([]inlineservice.InlineResult{{
			ID:          "youtube-search-help",
			Type:        inlineservice.ResultArticle,
			Title:       "YouTube search",
			Description: "Type: yt <search query>",
			Text:        "<b>YouTube search</b>\nType <code>yt &lt;search query&gt;</code> in inline mode.",
		}}), nil
	}
	if len(query) > download.MaxSearchQueryBytes {
		return nil, fmt.Errorf("%w: YouTube search query exceeds %d bytes", core.ErrInvalidArgs, download.MaxSearchQueryBytes)
	}

	results, err := h.plugin.searchYouTube(ctx.Ctx, query)
	if err != nil {
		switch {
		case errors.Is(err, download.ErrSearchNoResults):
			return interactiveInlineResponse([]inlineservice.InlineResult{{
				ID:          "youtube-search-empty",
				Type:        inlineservice.ResultArticle,
				Title:       "No YouTube results",
				Description: "Try different keywords",
				Text:        "<b>No YouTube results found.</b>\nTry a shorter or more specific query.",
			}}), nil
		case errors.Is(err, download.ErrExtractorUnavailable):
			return interactiveInlineResponse([]inlineservice.InlineResult{{
				ID:          "youtube-search-unavailable",
				Type:        inlineservice.ResultArticle,
				Title:       "YouTube search unavailable",
				Description: "yt-dlp is not available",
				Text:        "<b>YouTube search is unavailable.</b>\nThe media extractor is not installed or cannot be started.",
			}}), nil
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, core.ErrTimeout):
			return interactiveInlineResponse([]inlineservice.InlineResult{{
				ID:          "youtube-search-timeout",
				Type:        inlineservice.ResultArticle,
				Title:       "YouTube search timed out",
				Description: "Try a shorter or more specific query",
				Text:        "<b>YouTube search timed out.</b>\nTry a shorter or more specific query.",
			}}), nil
		default:
			return nil, err
		}
	}

	inlineResults := make([]inlineservice.InlineResult, 0, len(results))
	for _, result := range results {
		inlineResult, ok := h.plugin.youtubeInlineResult(result)
		if ok {
			inlineResults = append(inlineResults, inlineResult)
		}
	}
	if len(inlineResults) == 0 {
		return interactiveInlineResponse([]inlineservice.InlineResult{{
			ID:          "youtube-search-empty",
			Type:        inlineservice.ResultArticle,
			Title:       "No YouTube results",
			Description: "Try different keywords",
			Text:        "<b>No usable YouTube results found.</b>\nTry different keywords.",
		}}), nil
	}
	return interactiveInlineResponse(inlineResults), nil
}

func (p *Plugin) youtubeInlineResult(result download.SearchResult) (inlineservice.InlineResult, bool) {
	if result.Provider != "extractor" || result.Source != "youtube" || strings.TrimSpace(result.SourceID) == "" {
		return inlineservice.InlineResult{}, false
	}
	normalized, err := normalizeInteractiveURL(result.URL)
	if err != nil {
		return inlineservice.InlineResult{}, false
	}
	p.ensureRegistry()
	if p.registry == nil {
		return inlineservice.InlineResult{}, false
	}
	provider := p.registry.Resolve(normalized)
	if provider == nil || provider.Name() != "extractor" {
		return inlineservice.InlineResult{}, false
	}

	state := interactiveState{URL: normalized, Provider: "extractor", Phase: phaseChoose}
	encoded, err := encodeInteractiveState(state)
	if err != nil {
		return inlineservice.InlineResult{}, false
	}
	view := youtubeSearchChoiceView(result)
	markup := ui.NewMarkup(ui.ButtonRow{
		ui.NewSwitchInlineButton("🔎 Search Again", "yt ", true),
	})
	return inlineservice.InlineResult{
		ID:               "yt_" + result.SourceID,
		Type:             inlineservice.ResultArticle,
		Title:            result.Title,
		Description:      youtubeSearchDescription(result),
		Text:             view.Text,
		Markup:           &markup,
		ThumbURL:         result.Thumbnail,
		URL:              normalized,
		ActionRows:       view.Rows,
		InteractionState: encoded,
		InteractionTTL:   interactiveTTL,
	}, true
}

func youtubeSearchChoiceView(result download.SearchResult) presentation.View {
	lines := []string{"<b>" + core.EscapeHTML(result.Title) + "</b>"}
	if channel := strings.TrimSpace(result.Channel); channel != "" {
		lines = append(lines, "<b>Channel:</b> "+core.EscapeHTML(channel))
	}
	if result.DurationSeconds > 0 {
		lines = append(lines, "<b>Duration:</b> <code>"+formatSearchDuration(result.DurationSeconds)+"</code>")
	}
	if result.Views > 0 {
		lines = append(lines, fmt.Sprintf("<b>Views:</b> <code>%d</code>", result.Views))
	}
	lines = append(lines, "", "Choose media type:")
	return presentation.View{
		Text: strings.Join(lines, "\n"),
		Rows: extractorChoiceView(result.URL).Rows,
	}
}

func youtubeSearchDescription(result download.SearchResult) string {
	parts := make([]string, 0, 3)
	if channel := strings.TrimSpace(result.Channel); channel != "" {
		parts = append(parts, channel)
	}
	if result.DurationSeconds > 0 {
		parts = append(parts, formatSearchDuration(result.DurationSeconds))
	}
	if result.Views > 0 {
		parts = append(parts, fmt.Sprintf("%d views", result.Views))
	}
	if len(parts) == 0 {
		return "YouTube result"
	}
	return strings.Join(parts, " • ")
}

func formatSearchDuration(seconds int64) string {
	if seconds <= 0 {
		return "0:00"
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%d:%02d", minutes, secs)
}
