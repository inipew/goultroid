package downloader

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

type recordingDownloadProvider struct {
	name        string
	gotURL      string
	sawProcess  bool
	sawDownload bool
}

func (p *recordingDownloadProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "recording"
}
func (p *recordingDownloadProvider) Match(string) bool { return true }
func (p *recordingDownloadProvider) Download(ctx context.Context, rawURL string, _ storage.Storage, _ download.DownloadOptions) (*storage.Asset, error) {
	p.gotURL = rawURL
	p.sawProcess = tasks.HasHeldResource(ctx, "process")
	p.sawDownload = tasks.HasHeldResource(ctx, "download")
	return &storage.Asset{Name: "song.mp3", Size: 1, Path: "memory://song.mp3"}, nil
}

type replyFailureTelegramService struct {
	core.TelegramServicer
	err error
}

func (s *replyFailureTelegramService) GetMessage(context.Context, tg.InputPeerClass, int) (*tg.Message, error) {
	return nil, s.err
}

type rejectingTaskClient struct {
	tasks.Client
	err error
}

func (c *rejectingTaskClient) Submit(context.Context, tasks.WorkSpec) (tasks.Ticket, error) {
	return nil, c.err
}

func TestDownloaderRepliedURLFallbackPreservesUTF16EntityURL(t *testing.T) {
	client := &capturedClient{}
	p := New(client)
	provider := &recordingDownloadProvider{}
	p.registry = download.NewRegistry(provider)
	p.storage = storage.NewMemoryStorage()

	const targetURL = "https://example.com/audio/song.mp3"
	tgSvc := &mockTelegramService{
		replyMessage: &tg.Message{
			ID:      55,
			Message: "🎵 Check: " + targetURL,
			Entities: []tg.MessageEntityClass{
				// Telegram entity offsets are UTF-16 code units. The emoji consumes two units.
				&tg.MessageEntityURL{Offset: 10, Length: 34},
			},
		},
	}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     tgSvc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 2, ReplyToID: 55, IsOutgoing: true, Text: ".download"},
	}
	if err := p.handleDownload(ctx); err != nil {
		t.Fatalf("handleDownload: %v", err)
	}

	spec, ok := client.LastSpec()
	if !ok {
		t.Fatal("expected URL continuation task")
	}
	input, ok := spec.Input.([]byte)
	if !ok {
		t.Fatalf("task input type=%T, want []byte", spec.Input)
	}
	if string(input) != targetURL {
		t.Fatalf("task input=%q, want %q", input, targetURL)
	}
	if err := spec.Handler(context.Background()); err != nil {
		t.Fatalf("URL continuation: %v", err)
	}
	if provider.gotURL != targetURL {
		t.Fatalf("provider URL=%q, want %q", provider.gotURL, targetURL)
	}
	if !provider.sawDownload {
		t.Fatal("continuation did not propagate its held download resource marker")
	}

	tgSvc.mu.Lock()
	lastText := tgSvc.lastEdited
	tgSvc.mu.Unlock()
	if !strings.Contains(lastText, "URL Download Complete!") {
		t.Fatalf("completion edit=%q", lastText)
	}
}

func TestDownloaderExtractorContinuationMarksBothHeldResources(t *testing.T) {
	client := &capturedClient{}
	p := New(client)
	provider := &recordingDownloadProvider{name: "extractor"}
	p.registry = download.NewRegistry(provider)
	p.storage = storage.NewMemoryStorage()

	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     &mockTelegramService{},
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 7, IsOutgoing: true},
	}
	const targetURL = "https://example.com/extractor-target"
	if err := p.handleURLDownload(ctx, targetURL); err != nil {
		t.Fatal(err)
	}
	spec, _ := client.LastSpec()
	if !hasResource(spec.Resources, "download") || !hasResource(spec.Resources, "process") {
		t.Fatalf("extractor resources=%+v", spec.Resources)
	}
	if err := spec.Handler(context.Background()); err != nil {
		t.Fatalf("extractor continuation: %v", err)
	}
	if !provider.sawDownload || !provider.sawProcess {
		t.Fatalf("held resource markers: download=%v process=%v", provider.sawDownload, provider.sawProcess)
	}
}

func TestDownloaderPropagatesReplyLookupError(t *testing.T) {
	lookupErr := errors.New("mtproto get message failed")
	ctx := &core.Context{
		Ctx:    context.Background(),
		Svc:    &replyFailureTelegramService{err: lookupErr},
		PeerID: &tg.InputPeerSelf{},
		Message: &core.Message{
			ID:         1,
			ReplyToID:  42,
			IsOutgoing: true,
			Text:       ".download",
		},
	}

	err := New().handleDownload(ctx)
	if err == nil {
		t.Fatal("expected reply lookup failure")
	}
	if !errors.Is(err, lookupErr) {
		t.Fatalf("expected original lookup error, got %v", err)
	}
	if !strings.Contains(err.Error(), "resolve replied message") {
		t.Fatalf("missing downloader error context: %v", err)
	}
}

func TestDownloaderFailsClosedWhenContinuationAdmissionFails(t *testing.T) {
	admissionErr := errors.New("task admission closed")
	p := New(&rejectingTaskClient{err: admissionErr})
	provider := &recordingDownloadProvider{name: "extractor"}
	p.registry = download.NewRegistry(provider)
	p.storage = storage.NewMemoryStorage()

	const targetURL = "https://example.com/extractor-target"
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     &mockTelegramService{},
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 7, IsOutgoing: true},
	}
	err := p.handleURLDownload(ctx, targetURL)
	if err == nil {
		t.Fatal("expected continuation admission failure")
	}
	if !errors.Is(err, admissionErr) {
		t.Fatalf("expected original admission error, got %v", err)
	}
	if !strings.Contains(err.Error(), "submit url download task") {
		t.Fatalf("missing admission error context: %v", err)
	}
	if provider.gotURL != "" {
		t.Fatalf("provider executed despite failed admission: %q", provider.gotURL)
	}
}
