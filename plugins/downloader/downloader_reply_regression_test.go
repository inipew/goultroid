package downloader

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/download"
)

type replyFailureTelegramService struct {
	core.TelegramServicer
	err error
}

func (s *replyFailureTelegramService) GetMessage(context.Context, tg.InputPeerClass, int) (*tg.Message, error) {
	return nil, s.err
}

func TestDownloaderRepliedURLFallbackPreservesUTF16EntityURL(t *testing.T) {
	client := &capturedClient{}
	p := New(client)
	p.registry = download.NewRegistry(download.NewDirectHTTPProvider(5*time.Minute, 500*1024*1024))
	renderer := &downloaderFakeRenderer{}
	p.SetSelfInlineRenderer(renderer)

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
	if client.Count() != 0 {
		t.Fatalf("URL fallback submitted %d heavy tasks before confirmation", client.Count())
	}
	if renderer.calls != 1 || renderer.request.Query != "dl "+targetURL || renderer.request.ReplyToID != 55 {
		t.Fatalf("renderer request=%+v calls=%d", renderer.request, renderer.calls)
	}
}

func TestDownloaderExtractorResourcePlanStillOwnsDownloadAndProcess(t *testing.T) {
	p := New()
	p.registry = download.NewRegistry(download.NewExtractorProvider(nil, 500*1024*1024))

	resources := p.urlResources("https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	if !hasResource(resources, "download") || !hasResource(resources, "process") {
		t.Fatalf("extractor resources=%+v, want download+process", resources)
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

func TestDownloaderSelfInlineFailureDoesNotFallBackToPhysicalDownload(t *testing.T) {
	client := &capturedClient{}
	p := New(client)
	p.registry = download.NewRegistry(download.NewExtractorProvider(nil, 500*1024*1024))
	renderErr := errors.New("self-inline unavailable")
	p.SetSelfInlineRenderer(&downloaderFakeRenderer{err: renderErr})

	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     &mockTelegramService{},
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 7, IsOutgoing: true},
	}
	err := p.handleURLDownload(ctx, "https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	if !errors.Is(err, renderErr) {
		t.Fatalf("render failure=%v, want %v", err, renderErr)
	}
	if client.Count() != 0 {
		t.Fatalf("render failure fell back to %d heavy tasks", client.Count())
	}
}
