package downloader

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/storage"
)

func TestP3NativeURLPipelineRegistersRetainedOwnership(t *testing.T) {
	engine := newP5DownloaderTaskEngine(t)
	stopped := false
	t.Cleanup(func() {
		if stopped {
			return
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = engine.Stop(stopCtx)
	})

	store, err := storage.NewFileStorage(t.TempDir(), 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	ownership, _ := newDownloaderRegistryTest(t)
	const rawURL = "https://p3.example/owned-media"
	observed := make(chan p3URLObservation, 1)
	provider := &p3URLProvider{name: "extractor", url: rawURL, observed: observed}
	p := New(engine, store, ownership)
	p.registry = download.NewRegistry(provider)

	svc := newP3CommandService()
	svc.mediaSends = make(chan core.MessageSendContext, 1)
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 121, IsOutgoing: true},
	}
	if err := p.handleURLDownload(ctx, rawURL); err != nil {
		t.Fatalf("handleURLDownload() error=%v", err)
	}

	var physical p3URLObservation
	select {
	case physical = <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("native URL download stage did not run")
	}
	if physical.assetID == "" {
		t.Fatal("native URL pipeline produced no retained asset id")
	}

	select {
	case <-svc.mediaSends:
	case <-time.After(2 * time.Second):
		t.Fatal("native retained delivery did not reach Telegram media boundary")
	}
	waitP5DownloaderSettled(t, engine)

	record, err := ownership.Asset(context.Background(), physical.assetID)
	if err != nil {
		t.Fatalf("retained ownership lookup: %v", err)
	}
	if record.Producer != "downloader.extractor" || record.Owner != downloaderRegistryOwner || record.Lifecycle != mediaregistry.LifecycleRetained {
		t.Fatalf("native URL retained ownership=%+v", record)
	}
	if _, err := store.Stat(context.Background(), physical.assetID); err != nil {
		t.Fatalf("retained native URL asset missing after delivery: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	if err := engine.Stop(stopCtx); err != nil {
		stopCancel()
		t.Fatalf("stop TaskEngine: %v", err)
	}
	stopCancel()
	stopped = true
}
