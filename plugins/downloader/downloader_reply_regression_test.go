package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
)

type recordingDownloadProvider struct {
	gotURL string
}

func (p *recordingDownloadProvider) Name() string      { return "recording" }
func (p *recordingDownloadProvider) Match(string) bool { return true }
func (p *recordingDownloadProvider) Download(_ context.Context, rawURL string, _ storage.Storage, _ download.DownloadOptions) (*storage.Asset, error) {
	p.gotURL = rawURL
	return &storage.Asset{Name: "song.mp3", Size: 1, Path: "memory://song.mp3"}, nil
}

type replyFailureTelegramService struct {
	core.TelegramServicer
	err error
}

func (s *replyFailureTelegramService) GetMessage(context.Context, tg.InputPeerClass, int) (*tg.Message, error) {
	return nil, s.err
}

func newDownloaderJobHarness(t *testing.T) (*Plugin, *capturedClient) {
	t.Helper()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobsqlite.InitSchema(context.Background(), db.DB); err != nil {
		db.Close()
		t.Fatal(err)
	}

	pump := jobs.NewPersistencePump(1, 4)
	if err := pump.Start(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}

	client := &capturedClient{}
	jm := jobs.NewManager(client, jobsqlite.NewResourceStore(db.DB), pump)
	if err := jm.Start(context.Background()); err != nil {
		_ = pump.Stop(context.Background())
		db.Close()
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = jm.Stop(context.Background())
		_ = pump.Stop(context.Background())
		db.Close()
	})

	p := New()
	p.SetJobsManager(jm)
	return p, client
}

func TestDownloaderRepliedURLFallbackPreservesUTF16EntityURL(t *testing.T) {
	p, client := newDownloaderJobHarness(t)
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
		t.Fatalf("handleDownload with reply URL returned error: %v", err)
	}

	lastSpec, ok := client.LastSpec()
	if !ok {
		t.Fatal("expected URL fallback to submit a task")
	}
	input, ok := lastSpec.Input.([]byte)
	if !ok {
		t.Fatalf("expected downloader task payload to be []byte, got %T", lastSpec.Input)
	}
	var payload downloadJobPayload
	if err := json.Unmarshal(input, &payload); err != nil {
		t.Fatalf("decode submitted downloader payload: %v", err)
	}
	if payload.URL != targetURL {
		t.Fatalf("expected exact URL %q in task payload, got %q", targetURL, payload.URL)
	}

	if err := lastSpec.Handler(context.Background()); err != nil {
		t.Fatalf("background URL task failed: %v", err)
	}
	if provider.gotURL != targetURL {
		t.Fatalf("expected provider to receive exact URL %q, got %q", targetURL, provider.gotURL)
	}

	tgSvc.mu.Lock()
	lastText := tgSvc.lastEdited
	tgSvc.mu.Unlock()
	if !strings.Contains(lastText, "URL Download Complete!") {
		t.Fatalf("expected successful URL download completion message, got %q", lastText)
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
		t.Fatal("expected reply lookup failure to be returned")
	}
	if !errors.Is(err, lookupErr) {
		t.Fatalf("expected original reply lookup error to be preserved, got %v", err)
	}
	if !strings.Contains(err.Error(), "resolve replied message") {
		t.Fatalf("expected downloader context in error, got %v", err)
	}
}
