package downloader

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

type mockTicket struct {
	id tasks.TaskID
}

func (t *mockTicket) TaskID() tasks.TaskID             { return t.id }
func (t *mockTicket) State() tasks.TaskState           { return tasks.StateRunning }
func (t *mockTicket) Done() <-chan struct{}            { return nil }
func (t *mockTicket) Result() (tasks.TaskResult, bool) { return tasks.TaskResult{}, false }
func (t *mockTicket) Wait(context.Context) (tasks.TaskResult, error) {
	return tasks.TaskResult{TaskID: t.id, Outcome: tasks.OutcomeCompleted}, nil
}

type capturedClient struct {
	tasks.Client
	mu    sync.Mutex
	specs []tasks.WorkSpec
}

func (c *capturedClient) Submit(_ context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.mu.Lock()
	c.specs = append(c.specs, spec)
	c.mu.Unlock()
	return &mockTicket{id: spec.ID}, nil
}

func (c *capturedClient) LastSpec() (tasks.WorkSpec, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.specs) == 0 {
		return tasks.WorkSpec{}, false
	}
	return c.specs[len(c.specs)-1], true
}

func (c *capturedClient) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.specs)
}

type mockTelegramService struct {
	core.TelegramServicer
	mu           sync.Mutex
	lastEdited   string
	replyMessage *tg.Message
}

func (m *mockTelegramService) SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error) {
	return &tg.Message{ID: 100}, nil
}

func (m *mockTelegramService) EditMessage(_ context.Context, _ tg.InputPeerClass, _ int, text string) error {
	m.mu.Lock()
	m.lastEdited = text
	m.mu.Unlock()
	return nil
}

func (m *mockTelegramService) GetMessage(ctx context.Context, _ tg.InputPeerClass, _ int) (*tg.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.replyMessage != nil {
		return m.replyMessage, nil
	}
	return nil, nil
}

func (m *mockTelegramService) DownloadFile(ctx context.Context, _ tg.InputFileLocationClass, dstPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.WriteFile(dstPath, []byte("dummy audio content"), 0600)
}

func hasResource(resources []tasks.ResourceRequirement, name string) bool {
	for _, requirement := range resources {
		if requirement.Name == name && requirement.Amount == 1 {
			return true
		}
	}
	return false
}

func TestDownloaderCommandDelegatesHeavyResourcesToContinuation(t *testing.T) {
	p := New()
	cmds := p.Commands()
	if len(cmds) == 0 {
		t.Fatal("expected at least one command")
	}
	cmd := cmds[0]
	if cmd.Name != "download" {
		t.Fatalf("command name=%q, want download", cmd.Name)
	}
	if len(cmd.Resources) != 0 {
		t.Fatalf("interactive planning command must not reserve heavy resources: %+v", cmd.Resources)
	}
}

func TestDownloaderURLResourcePlanning(t *testing.T) {
	client := &capturedClient{}
	p := New(client)
	p.registry = download.NewRegistry(
		download.NewExtractorProvider(nil, 500*1024*1024),
		download.NewDirectHTTPProvider(5*time.Minute, 500*1024*1024),
	)
	p.storage = storage.NewMemoryStorage()
	tgSvc := &mockTelegramService{}

	ctxHTTP := &core.Context{
		Ctx:     context.Background(),
		Svc:     tgSvc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true},
	}
	if err := p.handleURLDownload(ctxHTTP, "https://example.com/media/file.mp4"); err != nil {
		t.Fatal(err)
	}
	if got := client.Count(); got != 1 {
		t.Fatalf("submitted tasks=%d, want 1", got)
	}
	specHTTP, _ := client.LastSpec()
	if specHTTP.Pool != "download" || specHTTP.Class != tasks.PriorityNormal {
		t.Fatalf("unexpected direct HTTP spec: %+v", specHTTP)
	}
	if !hasResource(specHTTP.Resources, "download") || hasResource(specHTTP.Resources, "process") {
		t.Fatalf("direct HTTP resources=%+v, want download only", specHTTP.Resources)
	}

	ctxExtractor := &core.Context{
		Ctx:     context.Background(),
		Svc:     tgSvc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 2, IsOutgoing: true},
	}
	if err := p.handleURLDownload(ctxExtractor, "https://www.youtube.com/watch?v=dQw4w9WgXcQ"); err != nil {
		t.Fatal(err)
	}
	if got := client.Count(); got != 2 {
		t.Fatalf("submitted tasks=%d, want 2", got)
	}
	specExtractor, _ := client.LastSpec()
	if !hasResource(specExtractor.Resources, "download") || !hasResource(specExtractor.Resources, "process") {
		t.Fatalf("extractor resources=%+v, want download+process", specExtractor.Resources)
	}
}

func TestDownloaderRepliedMediaContinuationOutlivesCommandContext(t *testing.T) {
	client := &capturedClient{}
	p := New(client)

	tmpDir := t.TempDir()
	fs, err := storage.NewFileStorage(tmpDir, 100*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	p.storage = fs

	tgSvc := &mockTelegramService{
		replyMessage: &tg.Message{
			ID:      42,
			Message: "Music track",
			Media: &tg.MessageMediaDocument{
				Document: &tg.Document{
					ID:       999,
					MimeType: "audio/mpeg",
					Size:     1024,
					Attributes: []tg.DocumentAttributeClass{
						&tg.DocumentAttributeFilename{FileName: "training_season.mp3"},
					},
				},
			},
		},
	}

	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	ctx := &core.Context{
		Ctx:     cmdCtx,
		Svc:     tgSvc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, ReplyToID: 42, IsOutgoing: true, Text: ".download"},
	}
	if err := p.handleDownload(ctx); err != nil {
		t.Fatalf("handleDownload: %v", err)
	}
	cmdCancel()

	spec, ok := client.LastSpec()
	if !ok {
		t.Fatal("expected media continuation task")
	}
	if !hasResource(spec.Resources, "download") || hasResource(spec.Resources, "process") {
		t.Fatalf("media resources=%+v, want download only", spec.Resources)
	}
	if err := spec.Handler(context.Background()); err != nil {
		t.Fatalf("media continuation: %v", err)
	}

	tgSvc.mu.Lock()
	lastText := tgSvc.lastEdited
	tgSvc.mu.Unlock()
	if !strings.Contains(lastText, "Download Complete!") {
		t.Fatalf("completion edit=%q", lastText)
	}
}

func TestDownloaderRepliedURLFallbackSubmitsContinuation(t *testing.T) {
	client := &capturedClient{}
	p := New(client)
	p.storage = storage.NewMemoryStorage()

	tgSvc := &mockTelegramService{
		replyMessage: &tg.Message{
			ID:      55,
			Message: "Check out this song: https://example.com/audio/song.mp3",
			Entities: []tg.MessageEntityClass{
				&tg.MessageEntityURL{Offset: 21, Length: 33},
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
	if spec.Pool != "download" || !hasResource(spec.Resources, "download") {
		t.Fatalf("unexpected URL continuation: %+v", spec)
	}
}


func TestMarkHeldResourcesPropagatesCoreAndDownloadLeaseMarkers(t *testing.T) {
	ctx := markHeldResources(context.Background(), []tasks.ResourceRequirement{
		{Name: "download", Amount: 1},
		{Name: "process", Amount: 1},
	})
	for _, name := range []string{"download", "process"} {
		if !tasks.HasHeldResource(ctx, name) {
			t.Fatalf("core held-resource marker missing for %q", name)
		}
		if !download.HasResource(ctx, name) {
			t.Fatalf("download service marker missing for %q", name)
		}
	}
}
