package download

import (
	"context"
	"errors"
	"time"

	"github.com/inipew/goultroid/internal/services/storage"
)

var (
	// ErrNoMatchingProvider indicates that no downloader provider matched the URL.
	ErrNoMatchingProvider = errors.New("download: no provider matched the given URL")
	// ErrExtractorUnavailable indicates that yt-dlp or the required external extractor is not installed.
	ErrExtractorUnavailable = errors.New("download: external extractor (yt-dlp) is not installed on the system")
	// ErrDownloadFailed indicates that the download operation failed.
	ErrDownloadFailed = errors.New("download: file download failed")
)

// MediaMode is a bounded semantic selection understood by extractor-backed
// providers. Direct HTTP providers intentionally ignore it.
type MediaMode string

const (
	MediaModeDefault MediaMode = ""
	MediaModeAudio   MediaMode = "audio"
	MediaModeVideo   MediaMode = "video"
)

// MediaFormat is the bounded output format vocabulary exposed by the shared
// downloader service. Providers reject unsupported mode/format combinations.
type MediaFormat string

const (
	MediaFormatDefault MediaFormat = ""
	MediaFormatBest    MediaFormat = "best"
	MediaFormatM4A     MediaFormat = "m4a"
	MediaFormatMP3     MediaFormat = "mp3"
	MediaFormatMP4     MediaFormat = "mp4"
)

// ProgressCallback is invoked periodically with the current download progress.
type ProgressCallback func(downloaded, total int64)

// DownloadOptions configures the download operation.
type DownloadOptions struct {
	MaxBytes       int64
	Timeout        time.Duration
	MaxAttempts    int
	RetryDelay     time.Duration
	TargetFilename string
	Progress       ProgressCallback
	Mode           MediaMode
	Format         MediaFormat
}

// Provider represents a source-specific media downloader.
type Provider interface {
	// Name returns the provider identifier (e.g. "http", "extractor").
	Name() string
	// Match returns true if this provider can handle the given URL.
	Match(rawURL string) bool
	// Download fetches the remote asset and persists it into storage.
	Download(ctx context.Context, rawURL string, store storage.Storage, opts DownloadOptions) (*storage.Asset, error)
}
