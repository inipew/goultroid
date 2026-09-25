package downloader

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestYTZZRetainedDeliveryUsesMediaResourceOnlyAndKeepsAsset(t *testing.T) {
	store, err := storage.NewFileStorage(t.TempDir(), 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.Put(context.Background(), bytes.NewBufferString("media"), storage.Metadata{
		Name:      "sample.mp4",
		MIME:      "video/mp4",
		Title:     "Sample Video",
		Performer: "Sample Channel",
		Duration:  2 * time.Minute,
		Width:     1280,
		Height:    720,
	})
	if err != nil {
		t.Fatal(err)
	}

	client := &youtubeSearchTaskClient{}
	p := New(client, store)
	var delivered presentation.Media
	var sawMedia, sawDownload, sawProcess bool
	err = p.submitRetainedDelivery(
		context.Background(),
		store,
		asset,
		download.MediaModeVideo,
		download.MediaFormatMP4,
		func(ctx context.Context, media presentation.Media) error {
			delivered = media
			sawMedia = tasks.HasHeldResource(ctx, "media")
			sawDownload = tasks.HasHeldResource(ctx, "download")
			sawProcess = tasks.HasHeldResource(ctx, "process")
			return nil
		},
		nil,
		nil,
		"inline",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.specs) != 1 {
		t.Fatalf("delivery task count=%d, want 1", len(client.specs))
	}
	spec := client.specs[0]
	if spec.ExecutionTimeout != downloaderDeliveryTimeout {
		t.Fatalf("delivery timeout=%v, want %v", spec.ExecutionTimeout, downloaderDeliveryTimeout)
	}
	if spec.Pool != tasks.PoolID("general") {
		t.Fatalf("delivery pool=%q, want general", spec.Pool)
	}
	if len(spec.Resources) != 1 || spec.Resources[0].Name != "media" || spec.Resources[0].Amount != 1 {
		t.Fatalf("delivery resources=%+v, want media=1 only", spec.Resources)
	}
	if !sawMedia || sawDownload || sawProcess {
		t.Fatalf("held resources media=%v download=%v process=%v", sawMedia, sawDownload, sawProcess)
	}
	if delivered.Path != asset.Path || delivered.FileName != asset.Name || delivered.MIMEType != asset.MIME || delivered.Type != "video" {
		t.Fatalf("delivered media=%+v", delivered)
	}
	if delivered.Title != asset.Title || delivered.Performer != asset.Performer || delivered.Duration != asset.Duration || delivered.Width != 1280 || delivered.Height != 720 {
		t.Fatalf("delivered metadata=%+v, asset=%+v", delivered, asset)
	}
	if !strings.Contains(delivered.Caption, "<b>File:</b> <code>sample.mp4</code>") || !strings.Contains(delivered.Caption, "<b>Resolution:</b> <code>1280x720</code>") {
		t.Fatalf("delivery caption=%q", delivered.Caption)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("retained asset was removed after delivery: %v", err)
	}
}

func TestYTZZDeliveryFailureKeepsRetainedAsset(t *testing.T) {
	store, err := storage.NewFileStorage(t.TempDir(), 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.Put(context.Background(), bytes.NewBufferString("audio"), storage.Metadata{
		Name: "sample.m4a",
		MIME: "audio/mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &youtubeSearchTaskClient{}
	p := New(client, store)
	err = p.submitRetainedDelivery(
		context.Background(),
		store,
		asset,
		download.MediaModeAudio,
		download.MediaFormatM4A,
		func(context.Context, presentation.Media) error {
			return context.DeadlineExceeded
		},
		nil,
		nil,
		"inline",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("delivery failure lost retained asset: %v", err)
	}
}

func TestYTZZCallbackAdmissionDoesNotHoldPhysicalResources(t *testing.T) {
	if got := downloaderDeliveryTimeout; got < 10*time.Minute {
		t.Fatalf("delivery timeout=%v is unexpectedly short", got)
	}
}

type ytzPipelineObservation struct {
	download bool
	process  bool
	media    bool
	assetID  string
}

type ytzPipelineProvider struct {
	observed chan<- ytzPipelineObservation
	options  chan<- download.DownloadOptions
}

func (*ytzPipelineProvider) Name() string { return "extractor" }

func (*ytzPipelineProvider) Match(rawURL string) bool {
	return strings.HasPrefix(rawURL, "https://www.youtube.com/")
}

func (p *ytzPipelineProvider) Download(
	ctx context.Context,
	_ string,
	store storage.Storage,
	opts download.DownloadOptions,
) (*storage.Asset, error) {
	if p.options != nil {
		p.options <- opts
	}
	asset, err := store.Put(ctx, bytes.NewBufferString("pipeline-media"), storage.Metadata{
		Name: "pipeline.mp4",
		MIME: "video/mp4",
	})
	if err != nil {
		return nil, err
	}
	p.observed <- ytzPipelineObservation{
		download: tasks.HasHeldResource(ctx, "download"),
		process:  tasks.HasHeldResource(ctx, "process"),
		media:    tasks.HasHeldResource(ctx, "media"),
		assetID:  asset.ID,
	}
	return asset, nil
}

func TestYTZPipelineReleasesDownloadResourcesBeforeMediaDelivery(t *testing.T) {
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
		ResultCapacity: 16,
		ResourceCapacities: map[string]int64{
			"download": 1,
			"process":  1,
			"media":    1,
		},
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := engine.Stop(ctx); err != nil {
			t.Errorf("stop TaskEngine: %v", err)
		}
	})

	store, err := storage.NewFileStorage(t.TempDir(), 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	downloadObserved := make(chan ytzPipelineObservation, 1)
	deliveryObserved := make(chan ytzPipelineObservation, 1)
	optionsObserved := make(chan download.DownloadOptions, 1)
	provider := &ytzPipelineProvider{observed: downloadObserved, options: optionsObserved}
	p := New(engine, store)
	p.registry = download.NewRegistry(provider)

	state := interactiveState{
		URL:      "https://www.youtube.com/watch?v=abcdefghijk",
		Provider: "extractor",
		Phase:    phaseVideoFormat,
		Mode:      download.MediaModeVideo,
		Format:    download.MediaFormatMP4,
		MaxHeight: 720,
	}
	if err := p.submitInteractivePipeline(
		context.Background(),
		state,
		download.MediaModeVideo,
		download.MediaFormatMP4,
		func(ctx context.Context, media presentation.Media) error {
			deliveryObserved <- ytzPipelineObservation{
				download: tasks.HasHeldResource(ctx, "download"),
				process:  tasks.HasHeldResource(ctx, "process"),
				media:    tasks.HasHeldResource(ctx, "media"),
			}
			return nil
		},
		nil,
		nil,
		nil,
		nil,
		"inline",
	); err != nil {
		t.Fatal(err)
	}

	var physical ytzPipelineObservation
	select {
	case physical = <-downloadObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("physical download stage did not run")
	}
	if !physical.download || !physical.process || physical.media {
		t.Fatalf("download-stage resources=%+v, want download+process only", physical)
	}

	select {
	case opts := <-optionsObserved:
		if opts.Mode != download.MediaModeVideo || opts.Format != download.MediaFormatMP4 || opts.MaxHeight != 720 {
			t.Fatalf("download options=%+v, want video/mp4 maxHeight=720", opts)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("download options were not observed")
	}

	select {
	case delivered := <-deliveryObserved:
		if delivered.download || delivered.process || !delivered.media {
			t.Fatalf("delivery-stage resources=%+v, want media only", delivered)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("media delivery stage did not run")
	}

	if physical.assetID == "" {
		t.Fatal("download stage produced no retained asset id")
	}
	if _, err := store.Stat(context.Background(), physical.assetID); err != nil {
		t.Fatalf("retained asset missing after successful delivery: %v", err)
	}
}

func TestYTZLifecycleCancellationDoesNotEmitPostDisableFailureUI(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, tasks.ErrScopeClosed} {
		if !isDeliveryLifecycleCancellation(err) {
			t.Fatalf("lifecycle error %v was not classified", err)
		}
	}
	if isDeliveryLifecycleCancellation(errors.New("telegram unavailable")) {
		t.Fatal("ordinary delivery error classified as lifecycle cancellation")
	}
}

func TestYTZUserbotURLCommandOpensSelectionBeforePhysicalDownload(t *testing.T) {
	client := &capturedClient{}
	p := New(client, storage.NewMemoryStorage())
	p.registry = download.NewRegistry(download.NewExtractorProvider(nil, 500*1024*1024))
	renderer := &downloaderFakeRenderer{}
	p.SetSelfInlineRenderer(renderer)

	ctx := &core.Context{
		Ctx:    context.Background(),
		Svc:    &mockTelegramService{},
		PeerID: &tg.InputPeerSelf{},
		Message: &core.Message{
			ID:         77,
			ReplyToID:  71,
			TopicID:    70,
			IsOutgoing: true,
			Text:       ".download https://www.youtube.com/watch?v=abcdefghijk",
		},
	}
	const rawURL = "https://www.youtube.com/watch?v=abcdefghijk"
	if err := p.handleURLDownload(ctx, rawURL); err != nil {
		t.Fatal(err)
	}
	if client.Count() != 0 {
		t.Fatalf("userbot URL command submitted %d heavy tasks before format selection", client.Count())
	}
	if renderer.calls != 1 || renderer.request.Query != "dl "+rawURL || renderer.request.ResultID != "downloader" {
		t.Fatalf("renderer calls/request=%d/%+v", renderer.calls, renderer.request)
	}
	if renderer.request.ReplyToID != 71 || renderer.request.TopicID != 70 {
		t.Fatalf("renderer reply/topic=%d/%d, want 71/70", renderer.request.ReplyToID, renderer.request.TopicID)
	}
}

