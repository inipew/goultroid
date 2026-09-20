package broadcast_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	broadcastSvc "github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/plugins/broadcast"
	"go.uber.org/zap"
)

type mockTelegram struct {
	core.MockTelegramServicer
	sentText string
	edited   string
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sentText = text
	return &tg.Message{ID: 1, Message: text}, nil
}

func (m *mockTelegram) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.edited = text
	return nil
}

func (m *mockTelegram) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	if msgID == 77 {
		return &tg.Message{ID: 77, Message: "<b>literal reply</b>"}, nil
	}
	return nil, nil
}

func (m *mockTelegram) GetDialogs(ctx context.Context, limit int) ([]*core.Chat, error) {
	return []*core.Chat{
		{ID: 101, Title: "Group 1", Type: "group"},
		{ID: 102, Title: "Channel 1", Type: "channel"},
		{ID: 103, Title: "User 1", Type: "user"},
	}, nil
}

func newBroadcastPluginRuntime(t *testing.T, telegram core.TelegramServicer) (*broadcastSvc.Service, *taskengine.Engine) {
	t.Helper()
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"general":  {Concurrency: 2, BacklogLimit: 32, PayloadBudget: 1 << 20},
			"download": {Concurrency: 1, BacklogLimit: 8, PayloadBudget: 32 << 20},
		},
		ResourceCapacities: map[string]int64{"download": 1, "media": 1},
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("start task engine: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = engine.Stop(stopCtx)
	})
	svc := broadcastSvc.NewService(telegram, zap.NewNop())
	svc.SetTasks(engine)
	return svc, engine
}

func newBroadcastPluginService(t *testing.T, telegram core.TelegramServicer) *broadcastSvc.Service {
	t.Helper()
	svc, _ := newBroadcastPluginRuntime(t, telegram)
	return svc
}

func TestBroadcastPlugin(t *testing.T) {
	mockTG := &mockTelegram{}
	svc := newBroadcastPluginService(t, mockTG)
	p := broadcast.New(svc)

	if p.Name() != "broadcast" {
		t.Errorf("expected plugin name broadcast, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}

	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true},
		Args:    []string{"-users", "Hello", "Users"},
		RawArgs: "-users Hello Users",
	}

	// 1. Broadcast command
	if err := cmds[0].Handler(ctx); err != nil {
		t.Fatalf("handleBroadcast failed: %v", err)
	}

	if !strings.Contains(mockTG.edited, "Broadcast Completed") {
		t.Errorf("expected completion edit, got %s", mockTG.edited)
	}

	// 2. Cancel when no job running
	cancelCtx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 2, IsOutgoing: true},
	}
	if err := cmds[1].Handler(cancelCtx); err != nil {
		t.Fatalf("handleCancelBroadcast failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "No active broadcast") {
		t.Errorf("expected no active broadcast notice, got %s", mockTG.edited)
	}
}

func TestBroadcastPlugin_EmptyArgs(t *testing.T) {
	mockTG := &mockTelegram{}
	svc := broadcastSvc.NewService(mockTG, zap.NewNop())
	p := broadcast.New(svc)

	cmds := p.Commands()
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true},
	}

	if err := cmds[0].Handler(ctx); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Usage:") {
		t.Errorf("expected usage guide, got %s", mockTG.edited)
	}
}

func TestBroadcastPlugin_RepliedTextUsesPlainSavedResponse(t *testing.T) {
	mockTG := &mockTelegram{}
	svc := newBroadcastPluginService(t, mockTG)
	p := broadcast.New(svc)

	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 10, ReplyToID: 77, IsOutgoing: true},
	}
	if err := p.Commands()[0].Handler(ctx); err != nil {
		t.Fatal(err)
	}
	if mockTG.sentText != "&lt;b&gt;literal reply&lt;/b&gt;" {
		t.Fatalf("replied plain text broadcast=%q", mockTG.sentText)
	}
}

type recordingBroadcastStorage struct {
	storage.Storage
	lastAssetID string
}

func (s *recordingBroadcastStorage) Put(ctx context.Context, src io.Reader, meta storage.Metadata) (*storage.Asset, error) {
	asset, err := s.Storage.Put(ctx, src, meta)
	if err == nil && asset != nil {
		s.lastAssetID = asset.ID
	}
	return asset, err
}

type mediaCaptureTelegram struct {
	mockTelegram
	reply                *tg.Message
	downloadPayload      []byte
	getMessageCalls      int
	mediaCalls           int
	sawDownloadLease     bool
	sawMediaLease        bool
	sawSendDownloadLease bool
}

func (m *mediaCaptureTelegram) GetMessage(context.Context, tg.InputPeerClass, int) (*tg.Message, error) {
	m.getMessageCalls++
	if m.reply == nil {
		return nil, nil
	}
	cp := *m.reply
	return &cp, nil
}

func (m *mediaCaptureTelegram) DownloadFile(ctx context.Context, _ tg.InputFileLocationClass, dstPath string) error {
	m.sawDownloadLease = tasks.HasHeldResource(ctx, "download")
	return os.WriteFile(dstPath, m.downloadPayload, 0o600)
}

func (m *mediaCaptureTelegram) SendMedia(
	ctx context.Context,
	_ tg.InputPeerClass,
	mediaType, filePath, caption string,
) (*tg.Message, error) {
	if _, err := os.Stat(filePath); err != nil {
		return nil, err
	}
	if mediaType != "photo" {
		return nil, errors.New("unexpected media type")
	}
	m.mediaCalls++
	m.sawMediaLease = tasks.HasHeldResource(ctx, "media")
	m.sawSendDownloadLease = tasks.HasHeldResource(ctx, "download")
	return &tg.Message{ID: 500, Message: caption}, nil
}

func broadcastPhotoMessage(id int, caption string, size int) *tg.Message {
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

func TestBroadcastPlugin_RepliedMediaReleasesDownloadBeforeFanout(t *testing.T) {
	payload := []byte("broadcast-photo")
	telegram := &mediaCaptureTelegram{
		reply:           broadcastPhotoMessage(77, "photo caption", len(payload)),
		downloadPayload: payload,
	}
	svc, engine := newBroadcastPluginRuntime(t, telegram)

	baseStore := storage.NewMemoryStorage()
	store := &recordingBroadcastStorage{Storage: baseStore}
	manager, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	responses := savedresponse.NewService(store)
	responses.SetFiles(manager.ForOwner("broadcast-plugin-test"))
	svc.SetResponses(responses)

	p := broadcast.New(svc, responses)
	p.SetTaskClient(engine)
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     telegram,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 10, ReplyToID: 77, IsOutgoing: true},
		Sender:  &core.User{ID: 1001, FirstName: "Alice"},
		Chat:    &core.Chat{ID: 1, Title: "Broadcast Test"},
		Args:    []string{"-users"},
		RawArgs: "-users",
	}
	if err := p.Commands()[0].Handler(ctx); err != nil {
		t.Fatal(err)
	}

	if telegram.getMessageCalls != 1 {
		t.Fatalf("GetReply RPC calls=%d, want 1 memoized inspection", telegram.getMessageCalls)
	}
	if !telegram.sawDownloadLease {
		t.Fatal("physical media capture did not hold download resource")
	}
	if telegram.mediaCalls != 1 {
		t.Fatalf("media sends=%d, want 1 user target", telegram.mediaCalls)
	}
	if !telegram.sawMediaLease {
		t.Fatal("physical media delivery did not hold media resource")
	}
	if telegram.sawSendDownloadLease {
		t.Fatal("download resource leaked from capture into broadcast fan-out")
	}
	if store.lastAssetID == "" {
		t.Fatal("capture did not persist a transient asset")
	}
	if _, err := baseStore.Stat(context.Background(), store.lastAssetID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("transient broadcast asset survived completed run: %v", err)
	}
}
