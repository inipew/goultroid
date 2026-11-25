package downloader

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
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

type panicProvider struct{}

func (panicProvider) Name() string      { return "http" }
func (panicProvider) Match(string) bool { return true }
func (panicProvider) Download(_ context.Context, _ string, _ storage.Storage, opts download.DownloadOptions) (*storage.Asset, error) {
	if opts.Progress != nil {
		opts.Progress(10, 100)
	}
	panic("simulated download panic")
}

func TestDownloadProgressReporter_CloseIsIdempotentAndJoins(t *testing.T) {
	reporter := newDownloadProgressReporterWithInterval(
		context.Background(),
		func(context.Context, string) error { return nil },
		"http",
		download.MediaModeDefault,
		download.MediaFormatDefault,
		0,
		10*time.Millisecond,
	)
	if reporter == nil {
		t.Fatal("progress reporter is nil")
	}
	reporter.Close()
	reporter.Close()
	reporter.Close()
	select {
	case <-reporter.done:
	default:
		t.Fatal("expected reporter done channel to be closed after Close()")
	}
}

func TestURLPipeline_ClosesReporterSynchronouslyOnDownloadPanic(t *testing.T) {
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"download": {Concurrency: 1, ZeroIdle: true, IdleTimeout: 20 * time.Millisecond, BacklogLimit: 8, PayloadBudget: 1 << 20},
			"general":  {Concurrency: 1, ZeroIdle: true, IdleTimeout: 20 * time.Millisecond, BacklogLimit: 8, PayloadBudget: 1 << 20},
		},
		ResultCapacity: 16,
		ResourceCapacities: map[string]int64{
			"download": 1,
			"media":    1,
		},
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = engine.Stop(ctx)
	})

	reg := download.NewRegistry()
	reg.Register(panicProvider{})
	p := New(reg, storage.NewMemoryStorage(), engine)

	runtime.GC()
	baseGoroutines := runtime.NumGoroutine()

	failed := make(chan struct{}, 1)
	if err := p.submitURLPipeline(context.Background(), urlDownloadRequest{
		URL: "https://example.com/panic.bin",
	}, urlPipelineHooks{
		Delivery: func(context.Context, presentation.Media) error { return nil },
		ProgressEdit: func(context.Context, string) error {
			return nil
		},
		DownloadFailure: func(context.Context) error {
			select {
			case failed <- struct{}{}:
			default:
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("submitURLPipeline() error = %v", err)
	}

	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for DownloadFailure after provider panic")
	}

	// Allow TaskEngine ZeroIdle worker to retire and verify no reporter goroutine leaked.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseGoroutines+2 {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > baseGoroutines+2 {
		t.Fatalf("goroutine count did not return near baseline after panic: base=%d got=%d", baseGoroutines, got)
	}
}
