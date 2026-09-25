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
	// ErrSearchUnsupported indicates that a registered provider does not expose search.
	ErrSearchUnsupported = errors.New("download: provider does not support search")
	// ErrSearchFailed indicates that provider-backed metadata search failed.
	ErrSearchFailed = errors.New("download: provider search failed")
	// ErrSearchNoResults indicates that search completed without usable results.
	ErrSearchNoResults = errors.New("download: search returned no usable results")
	// ErrProbeUnsupported indicates that a provider cannot inspect media formats.
	ErrProbeUnsupported = errors.New("download: provider does not support media probing")
	// ErrProbeFailed indicates that metadata/format inspection failed.
	ErrProbeFailed = errors.New("download: media probe failed")
)

const (
	DefaultSearchLimit   = 5
	MaxSearchLimit       = 5
	MaxSearchQueryBytes  = 256
	DefaultSearchTimeout = 15 * time.Second
	MaxSearchTimeout     = 30 * time.Second
	DefaultProbeTimeout  = 15 * time.Second
	MaxProbeTimeout      = 30 * time.Second
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
	MediaFormatOpus    MediaFormat = "opus"
)

// SearchOptions controls bounded provider metadata searches.
type SearchOptions struct {
	Limit   int
	Timeout time.Duration
}

// SearchResult is normalized provider metadata suitable for higher-level
// discovery surfaces. It intentionally excludes raw provider payloads.
type SearchResult struct {
	Provider        string
	Source          string
	SourceID        string
	URL             string
	Title           string
	Description     string
	Thumbnail       string
	Channel         string
	DurationSeconds int64
	Views           int64
	PublishedAt     string
}

// SearchProvider is an optional capability implemented only by providers that
// support metadata discovery. Provider remains download-only by default.
type SearchProvider interface {
	Name() string
	Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)
}

// ProbeOptions controls bounded metadata/format inspection without downloading media.
type ProbeOptions struct {
	Timeout time.Duration
}

// VideoQuality is one normalized, user-facing MP4 quality discovered from the
// provider. Size is an estimate and may be zero when the provider cannot know it.
type VideoQuality struct {
	Height int
	Size   int64
}

// ProbeResult is bounded metadata used to render a format chooser.
type ProbeResult struct {
	Title           string
	Performer       string
	DurationSeconds int64
	VideoQualities  []VideoQuality
}

// ProbeProvider is an optional capability for inspecting one media URL before
// the physical download begins.
type ProbeProvider interface {
	Name() string
	Probe(ctx context.Context, rawURL string, opts ProbeOptions) (ProbeResult, error)
}

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
	// MaxHeight bounds extractor-backed video selection. Zero means uncapped.
	// Providers must reject unsupported values rather than accepting arbitrary selectors.
	MaxHeight int
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
