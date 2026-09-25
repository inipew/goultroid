package downloader

import (
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/services/download"
)

func TestDownloadProgressTextIncludesTransferDetails(t *testing.T) {
	got := downloadProgressText(
		"extractor",
		download.MediaModeVideo,
		download.MediaFormatMP4,
		50*1024*1024,
		100*1024*1024,
		5*1024*1024,
		10*time.Second,
	)
	for _, want := range []string{
		"50.0%",
		"50.00 MB / 100.00 MB",
		"5.00 MB/s",
		"ETA:</b> <code>10s</code>",
		"Elapsed:</b> <code>10s</code>",
		"Source:</b> <code>yt-dlp</code>",
		"Format:</b> <code>video / mp4</code>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress text missing %q:\n%s", want, got)
		}
	}
}

func TestDownloadProgressTextHandlesUnknownTotal(t *testing.T) {
	got := downloadProgressText(
		"http",
		download.MediaModeDefault,
		download.MediaFormatDefault,
		8*1024*1024,
		0,
		2*1024*1024,
		4*time.Second,
	)
	if !strings.Contains(got, "Downloaded:</b> <code>8.00 MB</code>") {
		t.Fatalf("unknown-total progress missing downloaded size:\n%s", got)
	}
	if strings.Contains(got, "%</code>") {
		t.Fatalf("unknown-total progress unexpectedly contains percentage:\n%s", got)
	}
	if !strings.Contains(got, "Source:</b> <code>HTTP</code>") {
		t.Fatalf("unknown-total progress missing HTTP source:\n%s", got)
	}
}
