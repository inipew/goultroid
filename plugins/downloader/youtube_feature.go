package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/download"
)

var errNotYouTubeURL = errors.New("not a YouTube URL")

func looksLikeURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.ContainsAny(trimmed, " \t\r\n") {
		return false
	}
	if strings.Contains(trimmed, "://") || strings.HasPrefix(trimmed, "//") {
		return true
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "www.") {
		return true
	}
	return hasLikelyDomainHost(trimmed)
}

func hasLikelyDomainHost(raw string) bool {
	authority := raw
	if idx := strings.IndexAny(authority, "/?#"); idx >= 0 {
		authority = authority[:idx]
	}
	hostPart := authority
	if colon := strings.IndexByte(authority, ':'); colon >= 0 {
		hostPart = authority[:colon]
	}
	hostPart = strings.TrimSuffix(hostPart, ".")
	dot := strings.LastIndexByte(hostPart, '.')
	if dot <= 0 || dot >= len(hostPart)-1 {
		return false
	}
	tld := hostPart[dot+1:]
	if len(tld) < 2 {
		return false
	}
	for i := 0; i < len(tld); i++ {
		c := tld[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

func isYouTubeHost(host string) bool {
	normalized := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	switch normalized {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "youtu.be":
		return true
	default:
		return false
	}
}

func validateYouTubeURL(raw string) (string, error) {
	normalized, err := normalizeInteractiveURL(raw)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	if !isYouTubeHost(parsed.Hostname()) {
		return "", errNotYouTubeURL
	}
	return normalized, nil
}

func isYouTubeSearchResult(result download.SearchResult) bool {
	return result.Provider == "extractor" &&
		result.Source == "youtube" &&
		strings.TrimSpace(result.SourceID) != ""
}

func filterYouTubeResults(results []download.SearchResult) []download.SearchResult {
	filtered := make([]download.SearchResult, 0, len(results))
	for _, result := range results {
		if isYouTubeSearchResult(result) {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

func renderYouTubeSearchResults(query string, results []download.SearchResult) string {
	lines := []string{
		fmt.Sprintf("<b>YouTube results for:</b> <code>%s</code>", core.EscapeHTML(query)),
	}
	for i, result := range results {
		title := strings.TrimSpace(result.Title)
		if title == "" {
			title = result.SourceID
		}
		lines = append(lines, "", fmt.Sprintf("<b>%d. %s</b>", i+1, core.EscapeHTML(title)))
		if desc := youtubeSearchDescription(result); desc != "" {
			lines = append(lines, core.EscapeHTML(desc))
		}
		if targetURL := strings.TrimSpace(result.URL); targetURL != "" {
			lines = append(lines, fmt.Sprintf("<code>%s</code>", core.EscapeHTML(targetURL)))
		}
	}
	lines = append(lines, "", "Use <code>.yt &lt;url&gt;</code> to download a result.")
	return strings.Join(lines, "\n")
}

// handleYouTubeCommand handles `.yt <query|youtube-url>`:
//   - empty args -> usage help
//   - URL-like input -> strict YouTube host validation, then handleURLDownload
//     (interactive format selection when self-inline renderer is available, or
//     native default-format download via startNativeURLDownload when unavailable)
//   - query -> YouTube-only search via TaskEngine + Registry.Search("extractor")
func (p *Plugin) handleYouTubeCommand(ctx *core.Context) error {
	if ctx == nil {
		return core.ErrInvalidArgs
	}
	locale := p.contextLocale(ctx)
	input := strings.TrimSpace(strings.Join(ctx.Args, " "))
	if input == "" {
		return ctx.Status(downloaderTR(locale, "downloader.yt.usage"))
	}

	if looksLikeURL(input) {
		normalized, err := validateYouTubeURL(input)
		if err != nil {
			return ctx.Status("<b>Invalid or non-YouTube URL.</b>\n<code>.yt</code> only accepts YouTube links (<code>youtube.com</code> / <code>youtu.be</code>).\nUse <code>.dl &lt;url&gt;</code> for other platforms.")
		}
		return p.handleURLDownload(ctx, normalized)
	}

	if len(input) > download.MaxSearchQueryBytes {
		return ctx.Status(downloaderUserMessage(locale, core.ErrInvalidArgs))
	}

	results, err := p.searchYouTube(ctx.Ctx, input)
	if err != nil {
		switch {
		case errors.Is(err, download.ErrSearchNoResults):
			return ctx.Status(downloaderTR(locale, "downloader.yt.no_results"))
		case errors.Is(err, download.ErrExtractorUnavailable),
			errors.Is(err, context.DeadlineExceeded),
			errors.Is(err, core.ErrTimeout):
			return ctx.Status(downloaderUserMessage(locale, err))
		default:
			_ = ctx.Status(downloaderUserMessage(locale, err))
			return err
		}
	}

	filtered := filterYouTubeResults(results)
	if len(filtered) == 0 {
		return ctx.Status(downloaderTR(locale, "downloader.yt.no_usable_results"))
	}
	return ctx.Status(renderYouTubeSearchResults(input, filtered))
}
