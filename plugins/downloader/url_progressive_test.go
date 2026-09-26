package downloader

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

type p3URLObservation struct {
	options  download.DownloadOptions
	download bool
	process  bool
	media    bool
	assetID  string
}

type p3URLProvider struct {
	name     string
	url      string
	observed chan<- p3URLObservation
}

func (p *p3URLProvider) Name() string { return p.name }

func (p *p3URLProvider) Match(rawURL string) bool { return rawURL == p.url }

func (p *p3URLProvider) Download(
	ctx context.Context,
	_ string,
	store storage.Storage,
	opts download.DownloadOptions,
) (*storage.Asset, error) {
	asset, err := store.Put(ctx, bytes.NewBufferString("p3-media"), storage.Metadata{
		Name: "p3.mp4",
		MIME: "video/mp4",
	})
	if err != nil {
		return nil, err
	}
	if p.observed != nil {
		p.observed <- p3URLObservation{
			options:  opts,
			download: tasks.HasHeldResource(ctx, "download"),
			process:  tasks.HasHeldResource(ctx, "process"),
			media:    tasks.HasHeldResource(ctx, "media"),
			assetID:  asset.ID,
		}
	}
	return asset, nil
}

type p3CommandService struct {
	*mockTelegramService
	deleteCalls int
}

func newP3CommandService() *p3CommandService {
	return &p3CommandService{mockTelegramService: &mockTelegramService{}}
}

func (s *p3CommandService) DeleteMessage(_ context.Context, _ tg.InputPeerClass, _ []int) error {
	s.deleteCalls++
	return nil
}

func TestP3NativeURLFallbackUsesDefaultSharedPipelineAndResourceHandoff(t *testing.T) {
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
	const rawURL = "https://www.youtube.com/watch?v=abcdefghijk"
	observed := make(chan p3URLObservation, 1)
	provider := &p3URLProvider{name: "extractor", url: rawURL, observed: observed}
	p := New(engine, store)
	p.registry = download.NewRegistry(provider)

	svc := newP3CommandService()
	svc.mediaSends = make(chan core.MessageSendContext, 1)
	commandCtx, cancelCommand := context.WithCancel(context.Background())
	ctx := &core.Context{
		Ctx:    commandCtx,
		Svc:    svc,
		PeerID: &tg.InputPeerSelf{},
		Message: &core.Message{
			ID:         77,
			IsOutgoing: true,
			Text:       ".download " + rawURL,
		},
	}
	if err := p.handleURLDownload(ctx, rawURL); err != nil {
		t.Fatalf("handleURLDownload() error=%v", err)
	}
	cancelCommand()

	var physical p3URLObservation
	select {
	case physical = <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("native URL download stage did not run")
	}
	if physical.options.Mode != download.MediaModeDefault ||
		physical.options.Format != download.MediaFormatDefault ||
		physical.options.MaxHeight != 0 {
		t.Fatalf("native options=%+v, want default/default/0", physical.options)
	}
	if !physical.download || !physical.process || physical.media {
		t.Fatalf("download-stage resources download/process/media=%v/%v/%v", physical.download, physical.process, physical.media)
	}

	select {
	case send := <-svc.mediaSends:
		if send.ReplyToID != 77 {
			t.Fatalf("native delivery reply_to=%d, want 77", send.ReplyToID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("native retained delivery did not reach Telegram media boundary")
	}

	svc.mu.Lock()
	sawMedia := svc.sawMedia
	sawDownload := svc.sawDownload
	sawProcess := svc.sawProcess
	svc.mu.Unlock()
	if !sawMedia || sawDownload || sawProcess {
		t.Fatalf("delivery-stage resources media/download/process=%v/%v/%v", sawMedia, sawDownload, sawProcess)
	}
	if physical.assetID == "" {
		t.Fatal("native URL pipeline produced no retained asset id")
	}
	if _, err := store.Stat(context.Background(), physical.assetID); err != nil {
		t.Fatalf("retained native URL asset missing after delivery: %v", err)
	}

	settled := waitP5DownloaderSettled(t, engine)
	for _, name := range []string{"download", "process", "media"} {
		if settled.Resources[name].Used != 0 {
			t.Fatalf("resource %s retained after native URL pipeline: %+v", name, settled.Resources[name])
		}
	}
	svc.mu.Lock()
	lastEdited := svc.lastEdited
	svc.mu.Unlock()
	if !strings.Contains(lastEdited, "Download delivered to Telegram") {
		t.Fatalf("native terminal status=%q", lastEdited)
	}
	if svc.deleteCalls != 0 {
		t.Fatalf("native fallback deleted command, calls=%d", svc.deleteCalls)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	if err := engine.Stop(stopCtx); err != nil {
		stopCancel()
		t.Fatalf("stop TaskEngine: %v", err)
	}
	stopCancel()
	stopped = true
}

func TestP3SafeSelfInlineFailureFallsBackToNativeURLPipeline(t *testing.T) {
	client := &capturedClient{}
	const rawURL = "https://p3.example/media"
	provider := &p3URLProvider{name: "extractor", url: rawURL}
	p := New(client, storage.NewMemoryStorage())
	p.registry = download.NewRegistry(provider)
	renderer := &downloaderFakeRenderer{err: &selfinline.RenderFailure{
		Stage: selfinline.RenderStageQuery,
		Err:   selfinline.ErrQueryFailed,
	}}
	p.SetSelfInlineRenderer(renderer)
	svc := newP3CommandService()
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 88, IsOutgoing: true},
	}

	if err := p.handleURLDownload(ctx, rawURL); err != nil {
		t.Fatalf("handleURLDownload() error=%v", err)
	}
	if renderer.calls != 1 {
		t.Fatalf("renderer calls=%d, want 1", renderer.calls)
	}
	if client.Count() != 1 {
		t.Fatalf("native fallback task count=%d, want 1", client.Count())
	}
	spec, ok := client.LastSpec()
	if !ok || !hasResource(spec.Resources, "download") || !hasResource(spec.Resources, "process") {
		t.Fatalf("native fallback resources=%+v", spec.Resources)
	}
	if svc.deleteCalls != 0 {
		t.Fatalf("safe fallback deleted command, calls=%d", svc.deleteCalls)
	}
	svc.mu.Lock()
	lastEdited := svc.lastEdited
	svc.mu.Unlock()
	if !strings.Contains(lastEdited, "Preparing URL download") {
		t.Fatalf("native fallback status=%q", lastEdited)
	}
}

func TestP3SendStageFailureDoesNotStartNativeDuplicate(t *testing.T) {
	client := &capturedClient{}
	const rawURL = "https://p3.example/media"
	provider := &p3URLProvider{name: "extractor", url: rawURL}
	p := New(client, storage.NewMemoryStorage())
	p.registry = download.NewRegistry(provider)
	p.SetSelfInlineRenderer(&downloaderFakeRenderer{err: &selfinline.RenderFailure{
		Stage:            selfinline.RenderStageSend,
		MayHaveCommitted: true,
		Err:              selfinline.ErrSendFailed,
	}})
	svc := newP3CommandService()
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 99, IsOutgoing: true},
	}

	if err := p.handleURLDownload(ctx, rawURL); err != nil {
		t.Fatalf("handleURLDownload() error=%v", err)
	}
	if client.Count() != 0 {
		t.Fatalf("ambiguous self-inline send started %d native task(s)", client.Count())
	}
	if svc.deleteCalls != 0 {
		t.Fatalf("ambiguous self-inline send deleted command, calls=%d", svc.deleteCalls)
	}
	svc.mu.Lock()
	lastEdited := svc.lastEdited
	svc.mu.Unlock()
	if !strings.Contains(lastEdited, "could not be confirmed") {
		t.Fatalf("ambiguous self-inline diagnostic=%q", lastEdited)
	}
}

func TestP3NativeDirectHTTPPlansDownloadWithoutProcessResource(t *testing.T) {
	client := &capturedClient{}
	const rawURL = "https://p3.example/file.bin"
	provider := &p3URLProvider{name: "http", url: rawURL}
	p := New(client, storage.NewMemoryStorage())
	p.registry = download.NewRegistry(provider)
	svc := newP3CommandService()
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 111, IsOutgoing: true},
	}

	if err := p.handleURLDownload(ctx, rawURL); err != nil {
		t.Fatalf("handleURLDownload() error=%v", err)
	}
	spec, ok := client.LastSpec()
	if !ok {
		t.Fatal("native direct HTTP task was not submitted")
	}
	if !hasResource(spec.Resources, "download") || hasResource(spec.Resources, "process") {
		t.Fatalf("native direct HTTP resources=%+v, want download only", spec.Resources)
	}
}
