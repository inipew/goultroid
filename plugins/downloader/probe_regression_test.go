package downloader

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/localization"
)

// TestProbeErrorClassification verifies that the full range of errors returned
// by Registry.Probe are classified into user-facing categories that do NOT map
// to errCatInternalFailure (i.e., "Something went wrong"), except for genuinely
// unknown errors.
func TestProbeErrorClassification(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantCat downloadErrorCategory
	}{
		// Core probe failures from yt-dlp non-zero exit, JSON parse, process task.
		{
			name:    "probe_failed_nonzero_exit",
			err:     fmt.Errorf("%w: yt-dlp probe exited with code 1: ERROR: video unavailable", download.ErrProbeFailed),
			wantCat: errCatDownloadFailure,
		},
		{
			name:    "probe_failed_json_parse",
			err:     fmt.Errorf("%w: decode yt-dlp probe output: invalid character", download.ErrProbeFailed),
			wantCat: errCatDownloadFailure,
		},
		{
			name:    "probe_failed_task_wait",
			err:     fmt.Errorf("%w: extractor probe process wait failed: pool shutdown", download.ErrProbeFailed),
			wantCat: errCatDownloadFailure,
		},
		{
			name:    "probe_failed_process_resource_alloc",
			err:     fmt.Errorf("%w: failed to allocate process resource: pool full", download.ErrProbeFailed),
			wantCat: errCatDownloadFailure,
		},
		{
			name:    "probe_failed_no_result",
			err:     fmt.Errorf("%w: yt-dlp probe returned no process result", download.ErrProbeFailed),
			wantCat: errCatDownloadFailure,
		},
		// yt-dlp binary not installed — classified as unavailable, not probe_failed.
		{
			name:    "extractor_unavailable",
			err:     fmt.Errorf("%w: please install yt-dlp", download.ErrExtractorUnavailable),
			wantCat: errCatUnavailable,
		},
		// Context timeout or cancellation wrapping probe.
		{
			name:    "probe_timeout",
			err:     fmt.Errorf("%w: %w: yt-dlp probe exited", download.ErrProbeFailed, context.DeadlineExceeded),
			wantCat: errCatTimeout,
		},
		{
			name:    "probe_cancelled",
			err:     fmt.Errorf("%w: %w: probe cancelled", download.ErrProbeFailed, context.Canceled),
			wantCat: errCatCancelled,
		},
		// Output exceeded limit — classified as too_large.
		{
			name:    "probe_output_too_large",
			err:     fmt.Errorf("%w: yt-dlp probe output exceeded 2097152 bytes", core.ErrResourceLimit),
			wantCat: errCatTooLarge,
		},
		// No provider matched.
		{
			name:    "no_provider",
			err:     fmt.Errorf("%w: youtube.com", download.ErrNoMatchingProvider),
			wantCat: errCatUnsupportedSource,
		},
		// Provider does not support probing.
		{
			name:    "probe_unsupported",
			err:     fmt.Errorf("%w: http", download.ErrProbeUnsupported),
			wantCat: errCatUnsupportedSource,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDownloaderError(tc.err)
			if got != tc.wantCat {
				t.Fatalf("classifyDownloaderError(%v) = %q, want %q", tc.err, got, tc.wantCat)
			}
			// Verify user-facing message is non-empty and not an untranslated key.
			for _, locale := range []string{localization.LocaleEnglish, localization.LocaleIndonesian} {
				msg := downloaderUserMessage(locale, tc.err)
				if msg == "" || strings.HasPrefix(msg, "downloader.") {
					t.Fatalf("downloaderUserMessage(%q, %v) = untranslated/empty %q", locale, tc.err, msg)
				}
			}
		})
	}
}

// TestProbeFailedNeverMapsToInternalFailure asserts that ErrProbeFailed
// (the sentinel wrapping all yt-dlp failures) never classifies to
// errCatInternalFailure, which previously produced "Something went wrong."
func TestProbeFailedNeverMapsToInternalFailure(t *testing.T) {
	probeErrors := []error{
		fmt.Errorf("%w: code 1", download.ErrProbeFailed),
		fmt.Errorf("%w: json", download.ErrProbeFailed),
		fmt.Errorf("%w: task", download.ErrProbeFailed),
		download.ErrProbeFailed,
	}

	for _, err := range probeErrors {
		cat := classifyDownloaderError(err)
		if cat == errCatInternalFailure {
			t.Fatalf("classifyDownloaderError(%v) = errCatInternalFailure, want errCatDownloadFailure\n"+
				"This would produce 'Something went wrong. Please try again.' instead of a specific probe failure message.", err)
		}
	}
}

// TestProbeFailedUserMessageIsRetryable asserts that ErrProbeFailed is
// classified as retryable (download failure can be transient for yt-dlp).
func TestProbeFailedUserMessageIsRetryable(t *testing.T) {
	err := fmt.Errorf("%w: yt-dlp exited 1", download.ErrProbeFailed)
	if !isRetryableDownloaderError(err) {
		t.Fatalf("isRetryableDownloaderError(ErrProbeFailed) = false, want true: probe failures should be retryable")
	}
}

// TestProbeErrorUserMessageDoesNotLeakInternals asserts that none of the
// probe error categories expose internal filesystem paths, error strings,
// or yt-dlp stderr in the user-facing message.
func TestProbeErrorUserMessageDoesNotLeakInternals(t *testing.T) {
	sensitiveFragments := []string{
		"/tmp", "/home", "/var", "/usr", "stderr:",
		"yt-dlp probe exited", "decode yt-dlp", "pool full",
		"task failed", "process result",
	}

	probeErrors := []error{
		fmt.Errorf("%w: yt-dlp probe exited with code 1: /tmp/yt-dlp-cache/secret.json", download.ErrProbeFailed),
		fmt.Errorf("%w: decode yt-dlp probe output: /home/user/.cache/yt-dlp", download.ErrProbeFailed),
		fmt.Errorf("%w: failed to allocate process resource: pool full /var/run/goultroid", download.ErrProbeFailed),
		fmt.Errorf("%w: extractor probe process wait failed: pool shutdown /usr/share", download.ErrProbeFailed),
	}

	for _, err := range probeErrors {
		for _, locale := range []string{localization.LocaleEnglish, localization.LocaleIndonesian} {
			msg := downloaderUserMessage(locale, err)
			for _, frag := range sensitiveFragments {
				if strings.Contains(msg, frag) {
					t.Fatalf("user message (%q) for locale=%q leaked sensitive fragment %q: msg=%q", err, locale, frag, msg)
				}
			}
		}
	}
}

// TestProbeFailedA2ViewMessage verifies that failedView with an ErrProbeFailed
// error produces the expected download-failure user message, NOT "Something went wrong."
func TestProbeFailedA2ViewMessage(t *testing.T) {
	probeErr := fmt.Errorf("%w: yt-dlp probe exited with code 1: ERROR: video unavailable", download.ErrProbeFailed)

	for _, locale := range []string{localization.LocaleEnglish, localization.LocaleIndonesian} {
		view := failedView(probeErr, locale)
		want := downloaderUserMessage(locale, probeErr)
		if view.Text != want {
			t.Fatalf("failedView(ErrProbeFailed, %q).Text = %q, want %q", locale, view.Text, want)
		}
		// Specifically assert it is NOT the internal failure message.
		internalMsg := downloaderUserMessage(locale, nil) // nil → errCatInternalFailure
		if view.Text == internalMsg {
			t.Fatalf("failedView(ErrProbeFailed, %q) produced generic internal failure message %q — probe errors must have distinct message", locale, internalMsg)
		}
	}
}

// TestProbeExtractorUnavailableGivesDistinctMessage verifies that when
// yt-dlp is not installed, the user sees "unavailable" rather than "probe failed".
func TestProbeExtractorUnavailableGivesDistinctMessage(t *testing.T) {
	unavailErr := fmt.Errorf("%w: please install yt-dlp on the host machine", download.ErrExtractorUnavailable)
	probeFailErr := fmt.Errorf("%w: yt-dlp probe exited with code 1", download.ErrProbeFailed)

	for _, locale := range []string{localization.LocaleEnglish, localization.LocaleIndonesian} {
		unavailMsg := downloaderUserMessage(locale, unavailErr)
		probeFailMsg := downloaderUserMessage(locale, probeFailErr)
		// Both must produce valid, non-internal messages.
		internalMsg := downloaderUserMessage(locale, nil)
		if unavailMsg == internalMsg {
			t.Fatalf("ErrExtractorUnavailable (%q) = internal failure message, want distinct unavailable message", locale)
		}
		if probeFailMsg == internalMsg {
			t.Fatalf("ErrProbeFailed (%q) = internal failure message, want distinct download failure message", locale)
		}
		// They should also be distinct from each other (unavailable vs download_failure).
		if unavailMsg == probeFailMsg {
			t.Fatalf("ErrExtractorUnavailable and ErrProbeFailed produce same message in %q = %q; they should be distinct", locale, unavailMsg)
		}
		_ = errors.Is // keep import used
	}
}
