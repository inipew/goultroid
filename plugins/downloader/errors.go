package downloader

import (
	"context"
	"errors"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/localization"
	"github.com/inipew/goultroid/internal/tasks"
)

type downloadErrorCategory string

const (
	errCatInvalidInput      downloadErrorCategory = "invalid_input"
	errCatUnsupportedSource downloadErrorCategory = "unsupported_source"
	errCatBlockedURL        downloadErrorCategory = "blocked_url"
	errCatUnavailable       downloadErrorCategory = "unavailable"
	errCatTimeout           downloadErrorCategory = "timeout"
	errCatTooLarge          downloadErrorCategory = "too_large"
	errCatDownloadFailure   downloadErrorCategory = "download_failure"
	errCatDeliveryFailure   downloadErrorCategory = "delivery_failure"
	errCatCancelled         downloadErrorCategory = "cancelled"
	errCatInvalidSession    downloadErrorCategory = "invalid_session"
	errCatInternalFailure   downloadErrorCategory = "internal_failure"
)

var (
	errDeliveryFailure      = errors.New("downloader delivery failed")
	errInvalidSession       = errors.New("downloader session or action is invalid")
	errRetainedRegistration = errors.New("downloader retained asset registration failed")
)

func classifyDownloaderError(err error) downloadErrorCategory {
	if err == nil {
		return errCatInternalFailure
	}
	switch {
	case errors.Is(err, errDeliveryFailure):
		return errCatDeliveryFailure
	case errors.Is(err, errInvalidSession):
		return errCatInvalidSession
	case errors.Is(err, download.ErrBlockedSSRF):
		return errCatBlockedURL
	case errors.Is(err, download.ErrInvalidScheme), errors.Is(err, core.ErrInvalidArgs):
		return errCatInvalidInput
	case errors.Is(err, download.ErrNoMatchingProvider),
		errors.Is(err, download.ErrSearchUnsupported),
		errors.Is(err, download.ErrProbeUnsupported):
		return errCatUnsupportedSource
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, core.ErrTimeout):
		return errCatTimeout
	case errors.Is(err, context.Canceled), errors.Is(err, tasks.ErrScopeClosed):
		return errCatCancelled
	case errors.Is(err, core.ErrResourceLimit), errors.Is(err, core.ErrMediaTooLarge):
		return errCatTooLarge
	case errors.Is(err, download.ErrExtractorUnavailable), errors.Is(err, core.ErrUnavailable):
		return errCatUnavailable
	case errors.Is(err, download.ErrDownloadFailed),
		errors.Is(err, download.ErrSearchFailed),
		errors.Is(err, download.ErrProbeFailed),
		errors.Is(err, download.ErrTooManyRedirects):
		return errCatDownloadFailure
	default:
		return errCatInternalFailure
	}
}

func downloaderErrorKey(cat downloadErrorCategory) string {
	switch cat {
	case errCatInvalidInput:
		return "downloader.err.invalid_input"
	case errCatUnsupportedSource:
		return "downloader.err.unsupported_source"
	case errCatBlockedURL:
		return "downloader.err.blocked_url"
	case errCatUnavailable:
		return "downloader.err.unavailable"
	case errCatTimeout:
		return "downloader.err.timeout"
	case errCatTooLarge:
		return "downloader.err.too_large"
	case errCatDeliveryFailure:
		return "downloader.delivery_failed"
	case errCatCancelled:
		return "downloader.cancelled"
	case errCatInvalidSession:
		return "downloader.session_invalid"
	case errCatDownloadFailure:
		return "downloader.err.download_failed"
	default:
		return "downloader.err.internal"
	}
}

func downloaderUserMessage(locale string, err error) string {
	key := downloaderErrorKey(classifyDownloaderError(err))
	return downloaderTR(locale, key)
}

func isRetryableDownloaderError(err error) bool {
	if errors.Is(err, download.ErrTooManyRedirects) || errors.Is(err, errRetainedRegistration) {
		return false
	}
	switch classifyDownloaderError(err) {
	case errCatDownloadFailure, errCatTimeout, errCatUnavailable, errCatDeliveryFailure, errCatInternalFailure:
		return true
	default:
		return false
	}
}

func (p *Plugin) contextLocale(ctx *core.Context) string {
	if ctx == nil || ctx.Ctx == nil {
		return localization.DefaultLocale
	}
	userID := ctx.SenderID()
	if userID == 0 && ctx.Message != nil {
		userID = ctx.Message.SenderID
	}
	return p.resolveInteractiveLocale(ctx.Ctx, userID, ctx.ChatID())
}
