package downloader

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestYTZZRetainedDeliveryUsesMediaResourceOnlyAndKeepsAsset(t *testing.T) {
	store, err := storage.NewFileStorage(t.TempDir(), 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.Put(context.Background(), bytes.NewBufferString("media"), storage.Metadata{
		Name: "sample.mp4",
		MIME: "video/mp4",
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
	if len(spec.Resources) != 1 || spec.Resources[0].Name != "media" || spec.Resources[0].Amount != 1 {
		t.Fatalf("delivery resources=%+v, want media=1 only", spec.Resources)
	}
	if !sawMedia || sawDownload || sawProcess {
		t.Fatalf("held resources media=%v download=%v process=%v", sawMedia, sawDownload, sawProcess)
	}
	if delivered.Path != asset.Path || delivered.FileName != asset.Name || delivered.MIMEType != asset.MIME || delivered.Type != "video" {
		t.Fatalf("delivered media=%+v", delivered)
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
