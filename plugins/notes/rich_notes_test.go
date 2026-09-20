package notes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

type notesTaskClient struct {
	mu      sync.Mutex
	execute bool
	specs   []tasks.WorkSpec
}

func (c *notesTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.mu.Lock()
	c.specs = append(c.specs, spec)
	execute := c.execute
	c.mu.Unlock()
	if execute && spec.Handler != nil {
		return nil, spec.Handler(tasks.WithHeldResources(ctx, spec.Resources))
	}
	return nil, nil
}

func (c *notesTaskClient) Cancel(id tasks.TaskID, reason tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{TaskID: id, Accepted: false, Reason: reason}, tasks.ErrTaskNotFound
}

func (c *notesTaskClient) CancelScope(scope tasks.ScopeIdentity, reason tasks.Cause) int { return 0 }

func (c *notesTaskClient) Snapshot(id tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func (c *notesTaskClient) lastSpec(t *testing.T) tasks.WorkSpec {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.specs) == 0 {
		t.Fatal("expected submitted task")
	}
	return c.specs[len(c.specs)-1]
}

func (c *notesTaskClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.specs)
}

type richNotesService struct {
	core.MockTelegramServicer
	mu               sync.Mutex
	messages         []string
	mediaTypes       []string
	mediaCaptions    []string
	mediaPaths       []string
	sawMediaLease    bool
	sawDownloadLease bool
	reply            *tg.Message
	downloadPayload  []byte
}

func (s *richNotesService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, text)
	return &tg.Message{ID: len(s.messages) + 100, Message: text}, nil
}

func (s *richNotesService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, text)
	return nil
}

func (s *richNotesService) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reply == nil || s.reply.ID != msgID {
		return nil, nil
	}
	cp := *s.reply
	return &cp, nil
}

func (s *richNotesService) DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error {
	s.mu.Lock()
	s.sawDownloadLease = tasks.HasHeldResource(ctx, "download")
	payload := append([]byte(nil), s.downloadPayload...)
	s.mu.Unlock()
	return os.WriteFile(dstPath, payload, 0o600)
}

func (s *richNotesService) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType, filePath, caption string) (*tg.Message, error) {
	if _, err := os.Stat(filePath); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sawMediaLease = tasks.HasHeldResource(ctx, "media")
	s.mediaTypes = append(s.mediaTypes, mediaType)
	s.mediaPaths = append(s.mediaPaths, filePath)
	s.mediaCaptions = append(s.mediaCaptions, caption)
	return &tg.Message{ID: 500}, nil
}

func (s *richNotesService) snapshotMessages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.messages...)
}

func (s *richNotesService) resetMessages() {
	s.mu.Lock()
	s.messages = nil
	s.mu.Unlock()
}

type notesFailingDeleteStorage struct {
	storage.Storage
	failDelete bool
}

func (s *notesFailingDeleteStorage) Delete(ctx context.Context, id string) error {
	if s.failDelete {
		return errors.New("injected notes delete failure")
	}
	return s.Storage.Delete(ctx, id)
}

func openRichNotesDB(t *testing.T) *database.DB {
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

func newRichNotesResponseService(t *testing.T, db *database.DB, store storage.Storage, owner string) *savedresponse.Service {
	t.Helper()
	manager, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := savedresponse.NewService(store, db)
	svc.SetFiles(manager.ForOwner(owner))
	return svc
}

func newRichNotesContext(svc core.TelegramServicer, chatID int64) *core.Context {
	return &core.Context{
		Ctx:     context.Background(),
		Sender:  &core.User{ID: 1001, FirstName: "Alice"},
		Chat:    &core.Chat{ID: chatID, Title: "Notes Test"},
		Message: &core.Message{ID: 10, IsOutgoing: true},
		Svc:     svc,
		PeerID:  &tg.InputPeerChat{ChatID: chatID},
	}
}

func richNotesPhotoMessage(id int, caption string, size int) *tg.Message {
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

func putRichNoteAsset(t *testing.T, store storage.Storage, name, payload string) *storage.Asset {
	t.Helper()
	asset, err := store.Put(context.Background(), strings.NewReader(payload), storage.Metadata{Name: name, MIME: "image/jpeg"})
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func hasNoteResource(resources []tasks.ResourceRequirement, name string) bool {
	for _, resource := range resources {
		if resource.Name == name && resource.Amount > 0 {
			return true
		}
	}
	return false
}

func TestNotesCommandsPlanResourcesConditionally(t *testing.T) {
	p := New(nil)
	commands := p.Commands()
	for _, command := range commands {
		if (command.Name == "save" || command.Name == "get") && len(command.Resources) != 0 {
			t.Fatalf("%s still declares static resources: %+v", command.Name, command.Resources)
		}
	}

	client := &notesTaskClient{execute: false}
	p.SetTaskClient(client)
	ctx := newRichNotesContext(&richNotesService{}, 99)
	reply := &core.Message{
		Text: "photo caption",
		Media: &core.MediaInfo{
			Type:     "photo",
			FileName: "photo.jpg",
			Location: &tg.InputPhotoFileLocation{},
		},
	}
	if err := p.saveReply(ctx, 99, "photo", reply); err != nil {
		t.Fatal(err)
	}
	spec := client.lastSpec(t)
	if spec.Pool != tasks.PoolID("download") || !hasNoteResource(spec.Resources, "download") {
		t.Fatalf("media save plan=%+v, want download pool/resource", spec)
	}
}

func TestNotesTextOnlyPathsDoNotReserveMediaResources(t *testing.T) {
	db := openRichNotesDB(t)
	defer db.Close()
	repo := NewSQLiteRepository(db)
	telegram := &richNotesService{}
	client := &notesTaskClient{execute: false}
	p := New(repo)
	p.SetTaskClient(client)
	ctx := newRichNotesContext(telegram, 101)

	ctx.Args = []string{"plain", "hello"}
	ctx.RawArgs = "plain hello"
	if err := p.handleSave(ctx); err != nil {
		t.Fatal(err)
	}
	if client.count() != 0 {
		t.Fatalf("authored text save submitted %d tasks", client.count())
	}

	ctx.Args = []string{"plain"}
	ctx.RawArgs = "plain"
	telegram.resetMessages()
	if err := p.handleGet(ctx); err != nil {
		t.Fatal(err)
	}
	if client.count() != 0 {
		t.Fatalf("text-only get submitted %d tasks", client.count())
	}
	messages := telegram.snapshotMessages()
	if len(messages) == 0 || messages[len(messages)-1] != "hello" {
		t.Fatalf("unexpected text delivery: %+v", messages)
	}
}

func TestNotesListDeliveryIsChunkedToTelegramLimit(t *testing.T) {
	db := openRichNotesDB(t)
	defer db.Close()
	repo := NewSQLiteRepository(db)
	chatID := int64(202)
	for i := 0; i < 700; i++ {
		name := fmt.Sprintf("note-%04d-%s", i, strings.Repeat("x", 8))
		if err := repo.SaveNote(context.Background(), chatID, name, savedresponse.NewText("value")); err != nil {
			t.Fatal(err)
		}
	}

	telegram := &richNotesService{}
	p := New(repo)
	ctx := newRichNotesContext(telegram, chatID)
	if err := p.handleList(ctx); err != nil {
		t.Fatal(err)
	}
	messages := telegram.snapshotMessages()
	if len(messages) < 2 {
		t.Fatalf("expected chunked list delivery, got %d message", len(messages))
	}
	for i, message := range messages {
		if runes := utf8.RuneCountInString(message); runes > notesTelegramMessageRunes {
			t.Fatalf("chunk %d has %d runes, max %d", i, runes, notesTelegramMessageRunes)
		}
	}
}

func TestRichNotesMediaLifecycleAcrossReloadAndReplacement(t *testing.T) {
	db := openRichNotesDB(t)
	defer db.Close()
	repo := NewSQLiteRepository(db)
	store := storage.NewMemoryStorage()
	telegram := &richNotesService{}
	chatID := int64(303)

	responses := newRichNotesResponseService(t, db, store, "notes-first")
	p := New(repo, responses)
	p.SetTaskClient(&notesTaskClient{execute: true})

	// Save an actual replied Telegram photo. The lightweight command phase only
	// inspects the reply; CaptureReply and DownloadFile execute under download:1.
	telegram.reply = richNotesPhotoMessage(77, "first caption", len("first-photo"))
	telegram.downloadPayload = []byte("first-photo")
	saveCtx := newRichNotesContext(telegram, chatID)
	saveCtx.Message.ReplyToID = 77
	saveCtx.Args = []string{"photo"}
	saveCtx.RawArgs = "photo"
	if err := p.handleSave(saveCtx); err != nil {
		t.Fatal(err)
	}
	if !telegram.sawDownloadLease {
		t.Fatal("replied photo capture did not execute under download resource")
	}
	stored, err := repo.GetNote(context.Background(), chatID, "photo")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || !stored.Response.HasMedia() || stored.Response.Text != "first caption" {
		t.Fatalf("unexpected saved replied photo: %+v", stored)
	}
	firstAssetID := stored.Response.MediaAssetID()
	if _, err := store.Stat(context.Background(), firstAssetID); err != nil {
		t.Fatalf("captured photo asset missing: %v", err)
	}

	// Simulate process/plugin reload while keeping the durable DB and asset store.
	reloadedResponses := newRichNotesResponseService(t, db, store, "notes-reloaded")
	reloadedTasks := &notesTaskClient{execute: true}
	reloaded := New(repo, reloadedResponses)
	reloaded.SetTaskClient(reloadedTasks)
	telegram.resetMessages()
	getCtx := newRichNotesContext(telegram, chatID)
	getCtx.Args = []string{"photo"}
	if err := reloaded.handleGet(getCtx); err != nil {
		t.Fatal(err)
	}
	spec := reloadedTasks.lastSpec(t)
	if !hasNoteResource(spec.Resources, "media") {
		t.Fatalf("media get resources=%+v, want media", spec.Resources)
	}
	if !telegram.sawMediaLease || len(telegram.mediaTypes) != 1 || telegram.mediaTypes[0] != "photo" {
		t.Fatalf("media delivery did not execute under media lease: lease=%v types=%v", telegram.sawMediaLease, telegram.mediaTypes)
	}
	if telegram.mediaCaptions[0] != "first caption" {
		t.Fatalf("caption=%q", telegram.mediaCaptions[0])
	}

	// media -> text must retire the old persisted asset.
	if err := reloaded.saveResponse(newRichNotesContext(telegram, chatID), chatID, "photo", savedresponse.NewText("text only")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(context.Background(), firstAssetID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("old media survived media->text replacement: %v", err)
	}
	note, err := repo.GetNote(context.Background(), chatID, "photo")
	if err != nil {
		t.Fatal(err)
	}
	if note == nil || note.Response.HasMedia() || note.Response.Text != "text only" {
		t.Fatalf("unexpected text replacement: %+v", note)
	}

	// text -> media goes through the replied-photo capture path again.
	telegram.reply = richNotesPhotoMessage(88, "second caption", len("second-photo"))
	telegram.downloadPayload = []byte("second-photo")
	secondSaveCtx := newRichNotesContext(telegram, chatID)
	secondSaveCtx.Message.ReplyToID = 88
	secondSaveCtx.Args = []string{"photo"}
	secondSaveCtx.RawArgs = "photo"
	if err := reloaded.handleSave(secondSaveCtx); err != nil {
		t.Fatal(err)
	}
	note, err = repo.GetNote(context.Background(), chatID, "photo")
	if err != nil {
		t.Fatal(err)
	}
	if note == nil || !note.Response.HasMedia() || note.Response.Text != "second caption" {
		t.Fatalf("unexpected media replacement: %+v", note)
	}
	secondAssetID := note.Response.MediaAssetID()
	if _, err := store.Stat(context.Background(), secondAssetID); err != nil {
		t.Fatalf("replacement media asset missing: %v", err)
	}

	clearCtx := newRichNotesContext(telegram, chatID)
	clearCtx.Args = []string{"photo"}
	if err := reloaded.handleClear(clearCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(context.Background(), secondAssetID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("media survived note delete: %v", err)
	}
	note, err = repo.GetNote(context.Background(), chatID, "photo")
	if err != nil {
		t.Fatal(err)
	}
	if note != nil {
		t.Fatalf("note survived delete: %+v", note)
	}
}

func TestNotesDeleteCleanupFailureIsRetriedDurably(t *testing.T) {
	db := openRichNotesDB(t)
	defer db.Close()
	repo := NewSQLiteRepository(db)
	base := storage.NewMemoryStorage()
	store := &notesFailingDeleteStorage{Storage: base, failDelete: true}
	responses := newRichNotesResponseService(t, db, store, "notes-cleanup")
	p := New(repo, responses)
	chatID := int64(404)
	telegram := &richNotesService{}
	ctx := newRichNotesContext(telegram, chatID)

	asset := putRichNoteAsset(t, base, "cleanup.jpg", "cleanup-photo")
	response := savedresponse.NewPlainText("cleanup caption")
	response.Media = &savedresponse.MediaRef{AssetID: asset.ID, MediaType: "photo", Name: asset.Name, MIMEType: asset.MIME}
	if err := repo.SaveNote(context.Background(), chatID, "cleanup", response); err != nil {
		t.Fatal(err)
	}

	ctx.Args = []string{"cleanup"}
	if err := p.handleClear(ctx); err != nil {
		t.Fatalf("durable cleanup failure must not roll back committed delete: %v", err)
	}
	if note, err := repo.GetNote(context.Background(), chatID, "cleanup"); err != nil || note != nil {
		t.Fatalf("note still present after committed delete: note=%+v err=%v", note, err)
	}
	if _, err := base.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("asset should remain while deletion is failing: %v", err)
	}
	pending, err := responses.PendingCleanupCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending cleanup=%d, want 1", pending)
	}

	store.failDelete = false
	if _, err := db.Exec("UPDATE saved_response_media_cleanup SET next_attempt_at = ? WHERE asset_id = ?", time.Now().UTC().Add(-time.Second), asset.ID); err != nil {
		t.Fatal(err)
	}
	stats, err := responses.ReconcileCleanup(context.Background(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deleted != 1 || stats.Deferred != 0 {
		t.Fatalf("unexpected cleanup stats: %+v", stats)
	}
	if _, err := base.Stat(context.Background(), asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("asset survived successful cleanup retry: %v", err)
	}
}

func TestClearMissingNoteReportsUserFacingMessage(t *testing.T) {
	db := openRichNotesDB(t)
	defer db.Close()
	telegram := &richNotesService{}
	p := New(NewSQLiteRepository(db))
	ctx := newRichNotesContext(telegram, 505)
	ctx.Args = []string{"missing"}
	if err := p.handleClear(ctx); err == nil {
		t.Fatal("expected missing note error")
	}
	messages := telegram.snapshotMessages()
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1], "not found") {
		t.Fatalf("missing note did not produce user-facing message: %+v", messages)
	}
}

func TestNotesMediaManagementUX(t *testing.T) {
	db := openRichNotesDB(t)
	defer db.Close()
	repo := NewSQLiteRepository(db)
	chatID := int64(606)

	if err := repo.SaveNote(context.Background(), chatID, "plain", savedresponse.NewHTML("Hello {name}")); err != nil {
		t.Fatal(err)
	}
	media := savedresponse.NewPlainText("literal caption")
	media.Media = &savedresponse.MediaRef{
		AssetID: "asset-photo", MediaType: "photo", Name: "photo.jpg", MIMEType: "image/jpeg",
	}
	if err := repo.SaveNote(context.Background(), chatID, "photo", media); err != nil {
		t.Fatal(err)
	}

	telegram := &richNotesService{}
	p := New(repo)
	ctx := newRichNotesContext(telegram, chatID)
	if err := p.handleList(ctx); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(telegram.snapshotMessages(), "\n")
	if !strings.Contains(joined, "[text]") || !strings.Contains(joined, "[photo]") {
		t.Fatalf("note list missing media indicators: %q", joined)
	}
	if !strings.Contains(joined, ".noteinfo") {
		t.Fatalf("note list missing management hint: %q", joined)
	}

	telegram.resetMessages()
	ctx.Args = []string{"photo"}
	if err := p.handleInfo(ctx); err != nil {
		t.Fatal(err)
	}
	messages := telegram.snapshotMessages()
	info := messages[len(messages)-1]
	for _, want := range []string{"Note Info", "photo", "plain", "photo.jpg", "image/jpeg", "Template variables:", "none"} {
		if !strings.Contains(info, want) {
			t.Fatalf("note info missing %q: %s", want, info)
		}
	}

	telegram.resetMessages()
	ctx.Args = []string{"plain"}
	if err := p.handleInfo(ctx); err != nil {
		t.Fatal(err)
	}
	messages = telegram.snapshotMessages()
	info = messages[len(messages)-1]
	if !strings.Contains(info, "{name}") || !strings.Contains(info, "Type:</b> <code>text") {
		t.Fatalf("text note info missing template/type metadata: %s", info)
	}
}
