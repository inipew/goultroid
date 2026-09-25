package downloader

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/services/download"
)

const (
	downloaderProgressInterval    = 2 * time.Second
	downloaderProgressEditTimeout = 8 * time.Second
	downloaderProgressBarWidth    = 12
)

type downloadProgressReporter struct {
	ctx        context.Context
	cancel     context.CancelFunc
	stopParent func() bool
	done       chan struct{}
	edit       func(context.Context, string) error
	provider   string
	mode       download.MediaMode
	format     download.MediaFormat
	maxHeight  int
	started    time.Time
	downloaded atomic.Int64
	total      atomic.Int64
	revision   atomic.Uint64
}

func newDownloadProgressReporter(
	parent context.Context,
	edit func(context.Context, string) error,
	provider string,
	mode download.MediaMode,
	format download.MediaFormat,
	maxHeight int,
) *downloadProgressReporter {
	if edit == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}

	// Deliberately start from Background so progress Telegram RPCs do not inherit
	// TaskEngine download/process resource markers from the physical download.
	ctx, cancel := context.WithCancel(context.Background())
	reporter := &downloadProgressReporter{
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		edit:     edit,
		provider: provider,
		mode:     mode,
		format:    format,
		maxHeight: maxHeight,
		started:   time.Now(),
	}
	reporter.stopParent = context.AfterFunc(parent, cancel)
	go reporter.loop()
	return reporter
}

func (r *downloadProgressReporter) Callback(downloaded, total int64) {
	if r == nil {
		return
	}
	if downloaded < 0 {
		downloaded = 0
	}
	if total < 0 {
		total = 0
	}
	r.downloaded.Store(downloaded)
	r.total.Store(total)
	r.revision.Add(1)
}

func (r *downloadProgressReporter) Close() {
	if r == nil {
		return
	}
	if r.stopParent != nil {
		r.stopParent()
	}
	r.cancel()
	<-r.done
}

func (r *downloadProgressReporter) loop() {
	defer close(r.done)
	ticker := time.NewTicker(downloaderProgressInterval)
	defer ticker.Stop()

	var (
		lastRevision uint64
		lastBytes    int64
		lastAt       = r.started
		smoothedBPS  float64
	)

	for {
		select {
		case <-r.ctx.Done():
			return
		case now := <-ticker.C:
			revision := r.revision.Load()
			if revision == 0 || revision == lastRevision {
				continue
			}
			downloaded := r.downloaded.Load()
			total := r.total.Load()
			if downloaded <= 0 && total <= 0 {
				lastRevision = revision
				continue
			}

			elapsed := now.Sub(r.started)
			if downloaded < lastBytes {
				// Extractor-backed downloads can start a second physical stream
				// (for example audio after video). Reset rate sampling cleanly.
				lastBytes = downloaded
				lastAt = now
				smoothedBPS = 0
			} else if seconds := now.Sub(lastAt).Seconds(); seconds > 0 {
				instant := float64(downloaded-lastBytes) / seconds
				if instant > 0 {
					if smoothedBPS == 0 {
						smoothedBPS = instant
					} else {
						smoothedBPS = 0.7*smoothedBPS + 0.3*instant
					}
				}
				lastBytes = downloaded
				lastAt = now
			}

			editCtx, cancel := context.WithTimeout(r.ctx, downloaderProgressEditTimeout)
			_ = r.edit(editCtx, downloadProgressText(
				r.provider,
				r.mode,
				r.format,
				r.maxHeight,
				downloaded,
				total,
				smoothedBPS,
				elapsed,
			))
			cancel()
			lastRevision = revision
		}
	}
}

func downloadProgressText(
	provider string,
	mode download.MediaMode,
	format download.MediaFormat,
	maxHeight int,
	downloaded, total int64,
	bytesPerSecond float64,
	elapsed time.Duration,
) string {
	var b strings.Builder
	b.WriteString("⬇️ <b>Downloading...</b>\n\n")

	if total > 0 {
		percent := float64(downloaded) * 100 / float64(total)
		if percent < 0 {
			percent = 0
		}
		if percent > 100 {
			percent = 100
		}
		filled := int(percent * downloaderProgressBarWidth / 100)
		if filled < 0 {
			filled = 0
		}
		if filled > downloaderProgressBarWidth {
			filled = downloaderProgressBarWidth
		}
		bar := strings.Repeat("█", filled) + strings.Repeat("░", downloaderProgressBarWidth-filled)
		fmt.Fprintf(&b, "<code>[%s] %.1f%%</code>\n", bar, percent)
		fmt.Fprintf(&b, "📦 <b>Size:</b> <code>%s / %s</code>\n", formatBytes(downloaded), formatBytes(total))
	} else {
		fmt.Fprintf(&b, "📦 <b>Downloaded:</b> <code>%s</code>\n", formatBytes(downloaded))
	}

	fmt.Fprintf(&b, "🚀 <b>Speed:</b> <code>%s</code>\n", formatTransferRate(bytesPerSecond))
	if total > downloaded && bytesPerSecond > 0 {
		eta := time.Duration(float64(time.Second) * (float64(total-downloaded) / bytesPerSecond))
		fmt.Fprintf(&b, "⏳ <b>ETA:</b> <code>%s</code>\n", formatProgressDuration(eta))
	} else {
		b.WriteString("⏳ <b>ETA:</b> <code>--</code>\n")
	}
	fmt.Fprintf(&b, "🕒 <b>Elapsed:</b> <code>%s</code>\n", formatProgressDuration(elapsed))
	fmt.Fprintf(&b, "🔧 <b>Source:</b> <code>%s</code>\n", progressProviderLabel(provider))
	fmt.Fprintf(&b, "🎞 <b>Format:</b> <code>%s</code>", progressFormatLabel(mode, format, maxHeight))
	return b.String()
}

func progressProviderLabel(provider string) string {
	switch provider {
	case "extractor":
		return "yt-dlp"
	case "http":
		return "HTTP"
	default:
		if strings.TrimSpace(provider) == "" {
			return "download"
		}
		return strings.TrimSpace(provider)
	}
}

func progressFormatLabel(mode download.MediaMode, format download.MediaFormat, maxHeight int) string {
	if mode == download.MediaModeDefault {
		return "default"
	}
	label := string(mode)
	if format != download.MediaFormatDefault {
		label += " / " + string(format)
	}
	if maxHeight > 0 {
		label += fmt.Sprintf(" / ≤%dp", maxHeight)
	}
	return label
}

func formatTransferRate(bytesPerSecond float64) string {
	if bytesPerSecond <= 0 {
		return "calculating..."
	}
	return formatBytes(int64(bytesPerSecond)) + "/s"
}

func formatProgressDuration(d time.Duration) string {
	if d < 0 {
		return "--"
	}
	d = d.Round(time.Second)
	if d < time.Second {
		return "<1s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %02ds", int(d/time.Minute), int(d/time.Second)%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d/time.Hour), int(d/time.Minute)%60)
}
