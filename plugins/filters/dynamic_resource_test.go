package filters

import (
	"context"
	"os"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

type filterPlanningTaskClient struct {
	tasks.Client
	execute bool
	specs   []tasks.WorkSpec
}

func (c *filterPlanningTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.specs = append(c.specs, spec)
	if c.execute && spec.Handler != nil {
		return nil, spec.Handler(tasks.WithHeldResources(ctx, spec.Resources))
	}
	return nil, nil
}

func (c *filterPlanningTaskClient) count() int {
	return len(c.specs)
}

func (c *filterPlanningTaskClient) lastSpec(t *testing.T) tasks.WorkSpec {
	t.Helper()
	if len(c.specs) == 0 {
		t.Fatal("expected submitted task")
	}
	return c.specs[len(c.specs)-1]
}

type filterCaptureService struct {
	core.MockTelegramServicer
	reply            *tg.Message
	downloadPayload  []byte
	getMessageCalls  int
	sawDownloadLease bool
}

func (s *filterCaptureService) GetMessage(context.Context, tg.InputPeerClass, int) (*tg.Message, error) {
	s.getMessageCalls++
	if s.reply == nil {
		return nil, nil
	}
	cp := *s.reply
	return &cp, nil
}

func (s *filterCaptureService) DownloadFile(ctx context.Context, _ tg.InputFileLocationClass, dstPath string) error {
	s.sawDownloadLease = tasks.HasHeldResource(ctx, "download")
	return os.WriteFile(dstPath, s.downloadPayload, 0o600)
}

func openFilterPlanningDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunFeatureMigrations(context.Background(), db, Module); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func newFilterResponseService(t *testing.T, db *database.DB, store storage.Storage) *savedresponse.Service {
	t.Helper()
	manager, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	responses := savedresponse.NewService(store, db)
	responses.SetFiles(manager.ForOwner("filters-dynamic-resource-test"))
	return responses
}

func newFilterCommandContext(svc core.TelegramServicer, chatID int64, replyToID int) *core.Context {
	return &core.Context{
		Ctx:     context.Background(),
		Sender:  &core.User{ID: 1001, FirstName: "Alice"},
		Chat:    &core.Chat{ID: chatID, Title: "Filters Test"},
		Message: &core.Message{ID: 10, ReplyToID: replyToID, IsOutgoing: true},
		Svc:     svc,
		PeerID:  &tg.InputPeerChat{ChatID: chatID},
	}
}

func filterPhotoMessage(id int, caption string, size int) *tg.Message {
	return &tg.Message{
		ID:      id,
		Message: caption,
		Media: &tg.MessageMediaPhoto{Photo: &tg.Photo{
			ID:            int64(id) * 100,
			AccessHash:    int64(id) * 1000,
			FileReference: []byte{1, 2, 3},
			DCID:          1,
			Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{
				Type: "x", W: 64, H: 64, Size: size,
			}},
		}},
	}
}

func hasFilterResource(resources []tasks.ResourceRequirement, name string) bool {
	for _, resource := range resources {
		if resource.Name == name && resource.Amount > 0 {
			return true
		}
	}
	return false
}

func TestFilterCommandPlansDownloadOnlyForRepliedMedia(t *testing.T) {
	db := openFilterPlanningDB(t)
	defer db.Close()
	repo := NewSQLiteRepository(db)
	telegram := &filterCaptureService{}
	client := &filterPlanningTaskClient{}
	p := New(repo, func() core.TelegramServicer { return telegram })
	p.tasks = client

	for _, command := range p.Commands() {
		if command.Name == "filter" && len(command.Resources) != 0 {
			t.Fatalf(".filter still declares static resources: %+v", command.Resources)
		}
	}

	authored := newFilterCommandContext(telegram, 101, 0)
	authored.Args = []string{"plain", "hello"}
	authored.RawArgs = "plain hello"
	if err := p.handleFilter(authored); err != nil {
		t.Fatal(err)
	}
	if client.count() != 0 {
		t.Fatalf("authored text submitted %d tasks", client.count())
	}

	telegram.reply = &tg.Message{ID: 77, Message: "replied text"}
	repliedText := newFilterCommandContext(telegram, 101, 77)
	repliedText.Args = []string{"reply"}
	repliedText.RawArgs = "reply"
	if err := p.handleFilter(repliedText); err != nil {
		t.Fatal(err)
	}
	if client.count() != 0 {
		t.Fatalf("replied text submitted %d tasks", client.count())
	}
	storedText, err := repo.GetFilter(context.Background(), 101, "reply")
	if err != nil {
		t.Fatal(err)
	}
	if storedText == nil || storedText.Response.Format != savedresponse.FormatPlain || storedText.Response.Text != "replied text" {
		t.Fatalf("unexpected replied-text response: %+v", storedText)
	}

	telegram.reply = filterPhotoMessage(88, "photo caption", len("photo-payload"))
	repliedMedia := newFilterCommandContext(telegram, 101, 88)
	repliedMedia.Args = []string{"photo"}
	repliedMedia.RawArgs = "photo"
	if err := p.handleFilter(repliedMedia); err != nil {
		t.Fatal(err)
	}
	if client.count() != 1 {
		t.Fatalf("replied media submitted %d tasks, want 1", client.count())
	}
	spec := client.lastSpec(t)
	if spec.Pool != tasks.PoolID("download") || !hasFilterResource(spec.Resources, "download") {
		t.Fatalf("media capture plan=%+v, want download pool/resource", spec)
	}
	if spec.ExecutionTimeout != filterCaptureTimeout {
		t.Fatalf("capture timeout=%v, want %v", spec.ExecutionTimeout, filterCaptureTimeout)
	}
}

func TestFilterRepliedMediaCaptureRunsUnderDownloadLease(t *testing.T) {
	db := openFilterPlanningDB(t)
	defer db.Close()
	repo := NewSQLiteRepository(db)
	store := storage.NewMemoryStorage()
	responses := newFilterResponseService(t, db, store)
	telegram := &filterCaptureService{
		reply:           filterPhotoMessage(99, "captured caption", len("captured-photo")),
		downloadPayload: []byte("captured-photo"),
	}
	client := &filterPlanningTaskClient{execute: true}
	p := New(repo, func() core.TelegramServicer { return telegram }, responses)
	p.tasks = client

	ctx := newFilterCommandContext(telegram, 202, 99)
	ctx.Args = []string{"photo"}
	ctx.RawArgs = "photo"
	if err := p.handleFilter(ctx); err != nil {
		t.Fatal(err)
	}
	if !telegram.sawDownloadLease {
		t.Fatal("physical filter media download did not hold download resource")
	}
	if telegram.getMessageCalls != 1 {
		t.Fatalf("GetReply RPC calls=%d, want 1 memoized inspection", telegram.getMessageCalls)
	}
	if client.count() != 1 {
		t.Fatalf("submitted tasks=%d, want 1", client.count())
	}

	stored, err := repo.GetFilter(context.Background(), 202, "photo")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || !stored.Response.HasMedia() || stored.Response.Text != "captured caption" {
		t.Fatalf("unexpected stored media filter: %+v", stored)
	}
	if _, err := store.Stat(context.Background(), stored.Response.MediaAssetID()); err != nil {
		t.Fatalf("captured filter media asset missing: %v", err)
	}
}
