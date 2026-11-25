package downloader

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

type benchOKProvider struct {
	payload []byte
}

func (p benchOKProvider) Name() string      { return "http" }
func (p benchOKProvider) Match(string) bool { return true }
func (p benchOKProvider) Download(ctx context.Context, _ string, store storage.Storage, opts download.DownloadOptions) (*storage.Asset, error) {
	if opts.Progress != nil {
		opts.Progress(int64(len(p.payload)), int64(len(p.payload)))
	}
	return store.Put(ctx, bytes.NewReader(p.payload), storage.Metadata{Name: "bench.bin", MIME: "application/octet-stream"})
}

func BenchmarkP2C_ProgressReporter(b *testing.B) {
	b.Run("callback_burst", func(b *testing.B) {
		reporter := newDownloadProgressReporterWithInterval(
			context.Background(),
			func(context.Context, string) error { return nil },
			"http",
			download.MediaModeDefault,
			download.MediaFormatDefault,
			0,
			time.Hour,
		)
		defer reporter.Close()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			reporter.Callback(int64(i+1), 1000000)
		}
	})

	b.Run("format_progress_text", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = downloadProgressText("http", download.MediaModeVideo, download.MediaFormatMP4, 720, 512*1024, 1024*1024, 256*1024, 2*time.Second)
		}
	})
}

func BenchmarkP2C_URLPipelineAndDelivery(b *testing.B) {
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"download": {Concurrency: 1, ZeroIdle: true, IdleTimeout: 50 * time.Millisecond, BacklogLimit: 16, PayloadBudget: 1 << 20},
			"general":  {Concurrency: 1, ZeroIdle: true, IdleTimeout: 50 * time.Millisecond, BacklogLimit: 16, PayloadBudget: 1 << 20},
		},
		ResultCapacity: 32,
		ResourceCapacities: map[string]int64{
			"download": 1,
			"media":    1,
		},
	})
	if err := engine.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = engine.Stop(ctx)
	}()

	reg := download.NewRegistry()
	reg.Register(benchOKProvider{payload: bytes.Repeat([]byte("x"), 4096)})
	fs, err := storage.NewFileStorage(b.TempDir(), 100*1024*1024)
	if err != nil {
		b.Fatal(err)
	}
	p := New(reg, fs, engine)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		done := make(chan error, 1)
		if err := p.submitURLPipeline(context.Background(), urlDownloadRequest{
			URL: "https://example.com/bench.bin",
		}, urlPipelineHooks{
			Delivery: func(context.Context, presentation.Media) error {
				done <- nil
				return nil
			},
			DownloadFailure: func(context.Context) error {
				done <- context.Canceled
				return nil
			},
			DeliveryFailure: func(context.Context) error {
				done <- context.Canceled
				return nil
			},
		}); err != nil {
			b.Fatal(err)
		}
		if err := <-done; err != nil {
			b.Fatal(err)
		}
	}
}
