package downloader

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

const (
	maxCookieFileBytes    int64 = 1 << 20 // 1 MiB
	maxCookieLines              = 10000
	maxCookieLineBytes          = 16384 // 16 KiB
	youtubeCookiesTimeout       = 30 * time.Second
)

var (
	errCookieEmpty         = errors.New("cookies file is empty")
	errCookieTooLarge      = errors.New("cookies file exceeds maximum size")
	errCookieTooManyLines  = errors.New("cookies file exceeds maximum line count")
	errCookieLineTooLong   = errors.New("cookies file exceeds maximum line length")
	errCookieInvalidFormat = errors.New("invalid netscape cookies format")
	errCookieNoYouTubeAuth = errors.New("no usable youtube authentication cookies found")
)

type cookieIngestionSink interface {
	SaveValidatedCookies(ctx context.Context, raw []byte) error
}

// isAuthorizedCookieSelfChat verifies that the command is invoked by the owner
// inside Saved Messages (self-chat), using numeric IDs and peer types rather
// than FilterMiddleware's PrivateOnly (which is bypassed for outgoing commands)
// or ctx.Self (which is not populated in prepareCommandContext).
func isAuthorizedCookieSelfChat(ctx *core.Context) bool {
	if ctx == nil || ctx.Perms == nil || ctx.Perms.OwnerID == 0 {
		return false
	}
	if !ctx.IsOwner() || !ctx.IsPrivate() {
		return false
	}
	ownerID := ctx.Perms.OwnerID
	if ctx.ChatID() != ownerID {
		return false
	}
	switch peer := ctx.PeerID.(type) {
	case *tg.InputPeerSelf:
		return true
	case *tg.InputPeerUser:
		return peer.UserID == ownerID
	default:
		return false
	}
}

func (p *Plugin) handleYouTubeCookiesCommand(ctx *core.Context) error {
	locale := p.contextLocale(ctx)

	// 1. Self-chat numeric gate MUST run before GetReply, media inspection, or DownloadMedia.
	if !isAuthorizedCookieSelfChat(ctx) {
		p.getLogger().Warn("downloader: rejected unauthorized ytcookies access")
		if ctx != nil {
			_ = ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.unauthorized"))
		}
		return core.ErrPermissionDenied
	}

	subcmd := ""
	if len(ctx.Args) > 0 {
		subcmd = strings.ToLower(strings.TrimSpace(ctx.Args[0]))
	}
	switch subcmd {
	case "status", "delete":
		return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.management_pending"))
	case "", "set":
	default:
		return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.usage"))
	}

	if ctx.Message == nil {
		return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.usage"))
	}

	var (
		media          *core.MediaInfo
		msgIDsToDelete []int
	)

	// 2. Locate document attachment or verify replied message sender after self-chat gate.
	if ctx.Message.Media != nil && ctx.Message.Media.Location != nil {
		media = ctx.Message.Media
		msgIDsToDelete = uniquePositiveMessageIDs(ctx.Message.ID)
	} else if ctx.Message.ReplyToID != 0 {
		replied, err := ctx.GetReply()
		if err != nil || replied == nil {
			return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.usage"))
		}
		// Outgoing messages in Saved Messages may have SenderID == 0; allow only when IsOutgoing.
		// Incoming messages must prove SenderID == OwnerID.
		if !replied.IsOutgoing {
			if replied.SenderID == 0 || replied.SenderID != ctx.Perms.OwnerID {
				p.getLogger().Warn("downloader: rejected ytcookies reply from non-owner")
				_ = ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.unauthorized"))
				return core.ErrPermissionDenied
			}
		}
		media = replied.Media
		msgIDsToDelete = uniquePositiveMessageIDs(ctx.Message.ID, replied.ID)
	} else {
		return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.usage"))
	}

	// 3. Validate media type and declared size BEFORE DownloadMedia.
	if media == nil || media.Location == nil || media.Type != "document" {
		return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.wrong_media"))
	}
	if media.Size > maxCookieFileBytes {
		return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.too_large"))
	}
	if p.files == nil {
		return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.storage_unavailable"))
	}

	// 4. Download into scoped temp dir with unconditional defer cleanup.
	tmpDir, err := p.files.CreateTempDir("ytcookies-ingest-*")
	if err != nil {
		p.getLogger().Warn("downloader: create ytcookies temp dir failed", zap.Error(err))
		return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.storage_unavailable"))
	}
	defer func() {
		if rmErr := p.files.RemoveTempDir(tmpDir); rmErr != nil {
			p.getLogger().Warn("downloader: remove ytcookies temp dir failed", zap.Error(rmErr))
		}
	}()

	downloadedPath, err := ctx.DownloadMedia(tmpDir)
	if err != nil {
		p.getLogger().Warn("downloader: download ytcookies document failed", zap.Error(err))
		// Do not delete upload source on transient download failure so the owner can retry.
		return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.download_failed"))
	}

	raw, readErr := readBoundedCookieFile(downloadedPath)
	if readErr != nil {
		deleted := p.deleteCookieMessages(ctx, msgIDsToDelete)
		key := cookieValidationErrorKey(readErr)
		return replyCookieResult(ctx, locale, key, deleted)
	}

	if valErr := parseAndValidateYouTubeCookies(raw, time.Now()); valErr != nil {
		p.getLogger().Warn("downloader: ytcookies validation failed", zap.String("reason", valErr.Error()))
		deleted := p.deleteCookieMessages(ctx, msgIDsToDelete)
		key := cookieValidationErrorKey(valErr)
		return replyCookieResult(ctx, locale, key, deleted)
	}

	// 5. Fail-closed: storage sink MUST exist and succeed BEFORE deleting upload messages.
	if p.cookieSink == nil {
		p.getLogger().Warn("downloader: ytcookies sink unavailable")
		return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.storage_unavailable"))
	}

	saveCtx := ctx.Ctx
	if saveCtx == nil {
		saveCtx = context.Background()
	}
	if err := p.cookieSink.SaveValidatedCookies(saveCtx, raw); err != nil {
		p.getLogger().Warn("downloader: save validated cookies failed", zap.Error(err))
		return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.storage_unavailable"))
	}

	// 6. Only after durable save succeeds, attempt Telegram message deletion.
	deleted := p.deleteCookieMessages(ctx, msgIDsToDelete)
	if deleted {
		return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.accepted"))
	}
	return ctx.EditOrReply(downloaderTR(locale, "downloader.cookies.accepted_delete_unconfirmed"))
}

func replyCookieResult(ctx *core.Context, locale, key string, deleted bool) error {
	msg := downloaderTR(locale, key)
	if !deleted {
		msg += downloaderTR(locale, "downloader.cookies.delete_unconfirmed_suffix")
	}
	return ctx.EditOrReply(msg)
}

func (p *Plugin) deleteCookieMessages(ctx *core.Context, msgIDs []int) bool {
	if ctx == nil || ctx.PeerID == nil || len(msgIDs) == 0 || ctx.Telegram.MessageActions == nil {
		return false
	}
	delCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ctx.Telegram.MessageActions.DeleteMessage(delCtx, ctx.PeerID, msgIDs); err != nil {
		p.getLogger().Warn("downloader: failed to delete uploaded cookie messages", zap.Int("count", len(msgIDs)), zap.Error(err))
		return false
	}
	return true
}

func uniquePositiveMessageIDs(ids ...int) []int {
	out := make([]int, 0, len(ids))
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func readBoundedCookieFile(path string) ([]byte, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if stat.Size() == 0 {
		return nil, errCookieEmpty
	}
	if stat.Size() > maxCookieFileBytes {
		return nil, errCookieTooLarge
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxCookieFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errCookieEmpty
	}
	if int64(len(data)) > maxCookieFileBytes {
		return nil, errCookieTooLarge
	}
	return data, nil
}

func cookieValidationErrorKey(err error) string {
	switch {
	case errors.Is(err, errCookieEmpty):
		return "downloader.cookies.empty"
	case errors.Is(err, errCookieTooLarge):
		return "downloader.cookies.too_large"
	case errors.Is(err, errCookieNoYouTubeAuth):
		return "downloader.cookies.no_youtube_auth"
	case errors.Is(err, errCookieTooManyLines), errors.Is(err, errCookieLineTooLong), errors.Is(err, errCookieInvalidFormat):
		return "downloader.cookies.invalid_format"
	default:
		return "downloader.cookies.storage_unavailable"
	}
}

func parseAndValidateYouTubeCookies(raw []byte, now time.Time) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return errCookieEmpty
	}
	if int64(len(raw)) > maxCookieFileBytes {
		return errCookieTooLarge
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return errCookieInvalidFormat
	}
	if trimmed[0] == '{' || trimmed[0] == '[' || trimmed[0] == '<' {
		return errCookieInvalidFormat
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	buf := make([]byte, 4096)
	scanner.Buffer(buf, maxCookieLineBytes+1)

	var (
		lineCount   int
		recordCount int
		hasYTAuth   bool
	)
	nowUnix := now.Unix()

	for scanner.Scan() {
		lineCount++
		if lineCount > maxCookieLines {
			return errCookieTooManyLines
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if len(line) > maxCookieLineBytes {
			return errCookieLineTooLong
		}
		if strings.TrimSpace(line) == "" {
			continue
		}

		if strings.HasPrefix(line, "#HttpOnly_") {
			line = strings.TrimPrefix(line, "#HttpOnly_")
		} else if strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) != 7 {
			return errCookieInvalidFormat
		}

		rawDomain := fields[0]
		flag := fields[1]
		cookiePath := fields[2]
		secure := fields[3]
		expStr := fields[4]
		name := fields[5]
		value := fields[6]

		if !isValidNetscapeBool(flag) || !isValidNetscapeBool(secure) {
			return errCookieInvalidFormat
		}
		if !strings.HasPrefix(cookiePath, "/") {
			return errCookieInvalidFormat
		}
		exp, err := strconv.ParseInt(expStr, 10, 64)
		if err != nil || exp < 0 {
			return errCookieInvalidFormat
		}
		if strings.TrimSpace(name) == "" {
			return errCookieInvalidFormat
		}

		normDomain, domainCookie, ok := normalizeNetscapeDomainScope(rawDomain, flag)
		if !ok {
			return errCookieInvalidFormat
		}
		recordCount++

		if value != "" && (exp == 0 || exp > nowUnix) && isYouTubeAuthCookiePair(name, normDomain, domainCookie) {
			hasYTAuth = true
		}
	}

	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return errCookieLineTooLong
		}
		return fmt.Errorf("%w: scan error", errCookieInvalidFormat)
	}
	if recordCount == 0 {
		return errCookieInvalidFormat
	}
	if !hasYTAuth {
		return errCookieNoYouTubeAuth
	}
	return nil
}

func isValidNetscapeBool(s string) bool {
	return s == "TRUE" || s == "FALSE"
}

// normalizeNetscapeDomainScope validates consistency between the Netscape domain
// prefix (leading dot = domain cookie, no leading dot = host-only cookie) and
// the includeSubdomains flag (TRUE for domain cookies, FALSE for host-only).
func normalizeNetscapeDomainScope(raw, includeSubdomains string) (norm string, domainCookie bool, ok bool) {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return "", false, false
	}
	hasLeadingDot := strings.HasPrefix(raw, ".")
	flagTrue := includeSubdomains == "TRUE"
	if hasLeadingDot != flagTrue {
		return "", false, false
	}
	norm = strings.ToLower(strings.TrimPrefix(raw, "."))
	if norm == "" || strings.HasPrefix(norm, ".") || strings.HasSuffix(norm, ".") || !strings.Contains(norm, ".") {
		return "", false, false
	}
	return norm, hasLeadingDot, true
}

// cookieCoversHost checks whether a cookie with normalized domain normDomain and
// domainCookie scope covers targetHost:
//   - domainCookie=true (.domain, includeSubdomains=TRUE): matches targetHost
//     when targetHost == normDomain or targetHost has suffix "."+normDomain.
//   - domainCookie=false (host-only, includeSubdomains=FALSE): matches ONLY
//     when normDomain == targetHost (no suffix matching).
func cookieCoversHost(normDomain, targetHost string, domainCookie bool) bool {
	if normDomain == "" || targetHost == "" {
		return false
	}
	if !domainCookie {
		return normDomain == targetHost
	}
	return targetHost == normDomain || strings.HasSuffix(targetHost, "."+normDomain)
}

// isYouTubeAuthCookiePair validates (cookie-name, domain, domainCookie) pairs
// together rather than two separate allowlists. Host-only cookies only apply to
// their exact host (so m.youtube.com or accounts.google.com host-only cookies
// are not treated as YouTube auth for youtube.com / www.youtube.com).
func isYouTubeAuthCookiePair(name, normDomain string, domainCookie bool) bool {
	coversYouTube := cookieCoversHost(normDomain, "www.youtube.com", domainCookie) ||
		cookieCoversHost(normDomain, "youtube.com", domainCookie)
	switch name {
	case "SID", "HSID", "SSID", "APISID", "SAPISID",
		"__Secure-1PSID", "__Secure-3PSID",
		"__Secure-1PAPISID", "__Secure-3PAPISID",
		"__Secure-1PSIDTS", "__Secure-3PSIDTS",
		"__Secure-1PSIDCC", "__Secure-3PSIDCC":
		return coversYouTube || cookieCoversHost(normDomain, "google.com", domainCookie)
	case "LOGIN_INFO":
		return coversYouTube
	default:
		return false
	}
}
