package downloader

import (
	"context"
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
		720,
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
		"Format:</b> <code>video / mp4 / ≤720p</code>",
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
		0,
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

func TestP4DownloadProgressCoalescesBurstAndSkipsIdleTicks(t *testing.T) {
	edits := make(chan string, 8)
	reporter := newDownloadProgressReporterWithInterval(
		context.Background(),
		func(_ context.Context, text string) error {
			edits <- text
			return nil
		},
		"http",
		download.MediaModeDefault,
		download.MediaFormatDefault,
		0,
		20*time.Millisecond,
	)
	if reporter == nil {
		t.Fatal("progress reporter is nil")
	}

	reporter.Callback(10, 100)
	reporter.Callback(20, 100)
	reporter.Callback(30, 100)

	select {
	case text := <-edits:
		if !strings.Contains(text, "30.0%") {
			t.Fatalf("coalesced progress=%q, want latest 30%% sample", text)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for coalesced progress edit")
	}

	select {
	case text := <-edits:
		t.Fatalf("idle tick emitted duplicate progress edit: %q", text)
	case <-time.After(60 * time.Millisecond):
	}

	reporter.Callback(40, 100)
	select {
	case text := <-edits:
		if !strings.Contains(text, "40.0%") {
			t.Fatalf("next progress=%q, want 40%%", text)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for next progress edit")
	}

	reporter.Close()
	reporter.Callback(50, 100)
	select {
	case text := <-edits:
		t.Fatalf("closed reporter emitted progress edit: %q", text)
	case <-time.After(60 * time.Millisecond):
	}
}
