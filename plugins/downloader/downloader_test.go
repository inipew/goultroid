package downloader

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
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
func (t *mockTicket) Wait(ctx context.Context) (tasks.TaskResult, error) {
	return tasks.TaskResult{TaskID: t.id, Outcome: tasks.OutcomeCompleted}, nil
}

type capturedClient struct {
	tasks.Client
	mu    sync.Mutex
	specs []tasks.WorkSpec
}

func (c *capturedClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
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

type mockTelegramService struct {
	core.TelegramServicer
}

func (m *mockTelegramService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	return &tg.Message{ID: 100}, nil
}

func (m *mockTelegramService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	return nil
}

func TestDownloaderCommandDeclaresOnlyDownloadResource(t *testing.T) {
	p := New()
	cmds := p.Commands()
	if len(cmds) == 0 {
		t.Fatal("expected at least 1 command")
	}
	dlCmd := cmds[0]
	if dlCmd.Name != "download" {
		t.Fatalf("expected command name download, got %s", dlCmd.Name)
	}

	for _, req := range dlCmd.Resources {
		if req.Name == "process" {
			t.Fatal("command .download must NOT statically hold process resource")
		}
	}

	foundDownload := false
	for _, req := range dlCmd.Resources {
		if req.Name == "download" && req.Amount == 1 {
			foundDownload = true
			break
		}
	}
	if !foundDownload {
		t.Fatal("command .download must hold download resource with amount 1")
	}
}

func TestDownloaderURLResourcePlanning(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}
	pump := jobs.NewPersistencePump(1, 4)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer pump.Stop(context.Background())

	client := &capturedClient{}
	jm := jobs.NewManager(client, jobsqlite.NewResourceStore(db.DB), pump)
	if err := jm.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer jm.Stop(context.Background())

	p := New()
	p.SetJobsManager(jm)
	p.registerJobHandlers()

	p.registry = download.NewRegistry(
		download.NewExtractorProvider(nil, 500*1024*1024),
		download.NewDirectHTTPProvider(5*time.Minute, 500*1024*1024),
	)
	p.storage = storage.NewMemoryStorage()

	tgSvc := &mockTelegramService{}

	// 1. Direct HTTP URL download
	ctxHTTP := &core.Context{
		Ctx:     context.Background(),
		Svc:     tgSvc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true, Text: ".download https://example.com/media/file.mp4"},
		Args:    []string{"https://example.com/media/file.mp4"},
	}
	if err := p.handleURLDownload(ctxHTTP, "https://example.com/media/file.mp4"); err != nil {
		t.Fatal(err)
	}

	client.mu.Lock()
	specsCount := len(client.specs)
	client.mu.Unlock()
	if specsCount != 1 {
		t.Fatalf("expected 1 task submitted to task client, got %d", specsCount)
	}

	specHTTP, _ := client.LastSpec()
	for _, r := range specHTTP.Resources {
		if r.Name == "process" {
			t.Errorf("direct HTTP work spec should NOT hold process resource, found: %+v", r)
		}
	}
	hasDownload := false
	for _, r := range specHTTP.Resources {
		if r.Name == "download" && r.Amount == 1 {
			hasDownload = true
		}
	}
	if !hasDownload {
		t.Errorf("direct HTTP work spec must hold download resource, found: %+v", specHTTP.Resources)
	}

	// 2. Extractor URL download (YouTube)
	ctxExtractor := &core.Context{
		Ctx:     context.Background(),
		Svc:     tgSvc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 2, IsOutgoing: true, Text: ".download https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		Args:    []string{"https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
	}
	if err := p.handleURLDownload(ctxExtractor, "https://www.youtube.com/watch?v=dQw4w9WgXcQ"); err != nil {
		t.Fatal(err)
	}

	client.mu.Lock()
	specsCount = len(client.specs)
	client.mu.Unlock()
	if specsCount != 2 {
		t.Fatalf("expected 2 tasks submitted to task client, got %d", specsCount)
	}

	specExtractor, _ := client.LastSpec()
	hasExtDownload := false
	hasExtProcess := false
	for _, r := range specExtractor.Resources {
		if r.Name == "download" && r.Amount == 1 {
			hasExtDownload = true
		}
		if r.Name == "process" && r.Amount == 1 {
			hasExtProcess = true
		}
	}
	if !hasExtDownload || !hasExtProcess {
		t.Errorf("extractor work spec must hold both download and process resources, found: %+v", specExtractor.Resources)
	}
}
