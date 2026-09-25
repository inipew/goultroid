package downloader

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

type p5DownloaderProvider struct {
	attempts atomic.Int32
	started  chan int32
}

func (*p5DownloaderProvider) Name() string { return "extractor" }

func (*p5DownloaderProvider) Match(rawURL string) bool {
	return rawURL == "https://p5.example/media"
}

func (p *p5DownloaderProvider) Download(
	ctx context.Context,
	_ string,
	store storage.Storage,
	_ download.DownloadOptions,
) (*storage.Asset, error) {
	attempt := p.attempts.Add(1)
	p.started <- attempt
	if attempt == 1 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return store.Put(ctx, bytes.NewBufferString("p5-media"), storage.Metadata{
		Name: "p5.mp4",
		MIME: "video/mp4",
	})
}

func newP5DownloaderTaskEngine(t *testing.T) *taskengine.Engine {
	t.Helper()
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"download": {
				Concurrency:    1,
				MinConcurrency: 0,
				ZeroIdle:       true,
				IdleTimeout:    20 * time.Millisecond,
				BacklogLimit:   8,
				PayloadBudget:  1 << 20,
			},
			"general": {
				Concurrency:    1,
				MinConcurrency: 0,
				ZeroIdle:       true,
				IdleTimeout:    20 * time.Millisecond,
				BacklogLimit:   8,
				PayloadBudget:  1 << 20,
			},
		},
		ResultCapacity: 32,
		ResourceCapacities: map[string]int64{
			"download": 1,
			"process":  1,
			"media":    1,
		},
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("start P5 downloader TaskEngine: %v", err)
	}
	return engine
}

func waitP5DownloaderSettled(t *testing.T, engine *taskengine.Engine) taskengine.RuntimeStats {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		stats, err := engine.Stats(context.Background())
		if err != nil {
			t.Fatalf("P5 downloader TaskEngine stats: %v", err)
		}
		settled := true
		for _, name := range []string{"download", "process", "media"} {
			if resource := stats.Resources[name]; resource.Used != 0 {
				settled = false
			}
		}
		for _, pool := range stats.Pools {
			if pool.Workers != 0 || pool.Running != 0 || pool.Dispatching != 0 || pool.Waiting != 0 {
				settled = false
				break
			}
		}
		if settled {
			return stats
		}
		if time.Now().After(deadline) {
			t.Fatalf("P5 downloader did not settle: pools=%+v resources=%+v", stats.Pools, stats.Resources)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestP5DownloaderCancelRetryResourceAcceptance(t *testing.T) {
	ctx := context.Background()
	engine := newP5DownloaderTaskEngine(t)
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = engine.Stop(stopCtx)
		}
	})

	store, err := storage.NewFileStorage(t.TempDir(), 16*1024*1024)
	if err != nil {
		t.Fatalf("create P5 downloader storage: %v", err)
	}
	provider := &p5DownloaderProvider{started: make(chan int32, 2)}
	plugin := New(engine, store)
	plugin.registry = download.NewRegistry(provider)

	first := interactiveState{
		URL:      "https://p5.example/media",
		Provider: "extractor",
		Phase:    phaseVideoFormat,
		Mode:     download.MediaModeVideo,
		Format:   download.MediaFormatMP4,
		TaskRoot: "p5-downloader:first",
	}
	var unexpectedDelivery atomic.Int32
	if err := plugin.submitInteractivePipeline(
		ctx,
		first,
		download.MediaModeVideo,
		download.MediaFormatMP4,
		func(context.Context, presentation.Media) error {
			unexpectedDelivery.Add(1)
			return nil
		},
		nil,
		nil,
		nil,
		nil,
		nil,
		"inline",
	); err != nil {
		t.Fatalf("submit P5 cancellable downloader: %v", err)
	}

	select {
	case attempt := <-provider.started:
		if attempt != 1 {
			t.Fatalf("P5 first provider attempt=%d, want 1", attempt)
		}
	case <-time.After(time.Second):
		t.Fatal("P5 first downloader attempt did not start")
	}
	active, err := engine.Stats(ctx)
	if err != nil {
		t.Fatalf("P5 active downloader stats: %v", err)
	}
	if active.Resources["download"].Used != 1 || active.Resources["process"].Used != 1 || active.Resources["media"].Used != 0 {
		t.Fatalf("P5 active resources=%+v, want download+process only", active.Resources)
	}

	if err := plugin.cancelInteractivePipeline(first.TaskRoot); err != nil {
		t.Fatalf("P5 cancel downloader pipeline: %v", err)
	}
	waitP5DownloaderSettled(t, engine)
	if unexpectedDelivery.Load() != 0 {
		t.Fatalf("P5 cancelled attempt delivered media %d time(s)", unexpectedDelivery.Load())
	}

	second := first
	second.TaskRoot = "p5-downloader:retry"
	delivered := make(chan presentation.Media, 1)
	resourceObservation := make(chan [3]bool, 1)
	if err := plugin.submitInteractivePipeline(
		ctx,
		second,
		download.MediaModeVideo,
		download.MediaFormatMP4,
		func(deliveryCtx context.Context, media presentation.Media) error {
			resourceObservation <- [3]bool{
				tasks.HasHeldResource(deliveryCtx, "download"),
				tasks.HasHeldResource(deliveryCtx, "process"),
				tasks.HasHeldResource(deliveryCtx, "media"),
			}
			delivered <- media
			return nil
		},
		nil,
		nil,
		nil,
		nil,
		nil,
		"inline",
	); err != nil {
		t.Fatalf("submit P5 downloader retry: %v", err)
	}

	select {
	case attempt := <-provider.started:
		if attempt != 2 {
			t.Fatalf("P5 retry provider attempt=%d, want 2", attempt)
		}
	case <-time.After(time.Second):
		t.Fatal("P5 retry downloader attempt did not start")
	}

	select {
	case held := <-resourceObservation:
		if held[0] || held[1] || !held[2] {
			t.Fatalf("P5 delivery resource markers download/process/media=%v, want false/false/true", held)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("P5 retry delivery did not execute")
	}
	select {
	case media := <-delivered:
		if media.FileName != "p5.mp4" || media.MIMEType != "video/mp4" {
			t.Fatalf("P5 delivered media=%+v", media)
		}
	case <-time.After(time.Second):
		t.Fatal("P5 retry delivery result missing")
	}

	settled := waitP5DownloaderSettled(t, engine)
	if provider.attempts.Load() != 2 {
		t.Fatalf("P5 downloader attempts=%d, want exactly 2", provider.attempts.Load())
	}
	for _, name := range []string{"download", "process", "media"} {
		if settled.Resources[name].Used != 0 {
			t.Fatalf("P5 resource %s retained after retry: %+v", name, settled.Resources[name])
		}
	}

	stopCtx, stopCancel := context.WithTimeout(ctx, time.Second)
	if err := engine.Stop(stopCtx); err != nil {
		stopCancel()
		t.Fatalf("stop P5 downloader TaskEngine: %v", err)
	}
	stopCancel()
	stopped = true
}
