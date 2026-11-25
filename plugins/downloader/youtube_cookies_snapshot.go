package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

const cookieSnapshotPattern = "ytdlp-cookies-*.txt"

func isYouTubeURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil {
		return false
	}
	return isYouTubeHost(parsed.Hostname())
}

// acquireYouTubeCookieSnapshot decrypts configured YouTube cookies into a
// unique 0600 per-operation temporary file under p.files.TempDir().
// If no cookies are configured (errCookieNotFound), it returns "", noop, nil so
// unauthenticated YouTube fallback continues to work.
// If cookies are configured but cannot be decrypted, it fails closed with
// core.ErrUnavailable without exposing key, path, or cookie material.
func (p *Plugin) acquireYouTubeCookieSnapshot(ctx context.Context) (string, func(), error) {
	noop := func() {}
	if p == nil || p.cookieStore == nil || p.files == nil {
		return "", noop, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	plaintext, err := p.cookieStore.LoadDecryptedCookies(ctx)
	if err != nil {
		if errors.Is(err, errCookieNotFound) {
			return "", noop, nil
		}
		p.getLogger().Warn("downloader: failed to load encrypted youtube cookies",
			zap.String("category", cookieSnapshotErrorCategory(err)),
		)
		return "", noop, fmt.Errorf("%w: youtube cookies unavailable", core.ErrUnavailable)
	}
	defer clear(plaintext)

	tmpFile, err := p.files.CreateTempFile(cookieSnapshotPattern)
	if err != nil {
		return "", noop, fmt.Errorf("%w: failed to create cookie snapshot", core.ErrUnavailable)
	}
	tmpPath := tmpFile.Name()
	removeFn := p.files.RemoveTempFile
	if p.removeCookieSnapshot != nil {
		removeFn = p.removeCookieSnapshot
	}
	cleanup := func() {
		if rmErr := removeFn(tmpPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			p.getLogger().Warn("downloader: failed to remove cookie snapshot",
				zap.String("category", "cleanup_failed"),
			)
		}
	}

	if err := tmpFile.Chmod(0600); err != nil {
		_ = tmpFile.Close()
		cleanup()
		return "", noop, fmt.Errorf("%w: failed to restrict cookie snapshot permissions", core.ErrUnavailable)
	}
	if _, err := tmpFile.Write(plaintext); err != nil {
		_ = tmpFile.Close()
		cleanup()
		return "", noop, fmt.Errorf("%w: failed to write cookie snapshot", core.ErrUnavailable)
	}
	if err := tmpFile.Close(); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("%w: failed to close cookie snapshot", core.ErrUnavailable)
	}

	return tmpPath, cleanup, nil
}

// cleanupStaleCookieSnapshots sweeps any leftover ephemeral cookie snapshot
// files from p.files.TempDir() (e.g. after an ungraceful process termination).
func (p *Plugin) cleanupStaleCookieSnapshots() {
	if p == nil || p.files == nil {
		return
	}
	tempDir, err := p.files.TempDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "ytdlp-cookies-") && strings.HasSuffix(name, ".txt") {
			_ = p.files.RemoveTempFile(filepath.Join(tempDir, name))
		}
	}
}

func cookieSnapshotErrorCategory(err error) string {
	switch {
	case errors.Is(err, errCookieKeyMissing):
		return "key_missing"
	case errors.Is(err, errCookieKeyInvalid):
		return "key_invalid"
	case errors.Is(err, errCookieDecryptFail):
		return "decrypt_failed"
	case errors.Is(err, errCookieCorrupt):
		return "corrupt"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "read_failed"
	}
}
