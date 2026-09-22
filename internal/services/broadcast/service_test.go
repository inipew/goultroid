package broadcast_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type mockTelegram struct {
	core.MockTelegramServicer
	sentCount int32
	failCount int32
	sendDelay time.Duration
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if m.sendDelay > 0 {
		timer := time.NewTimer(m.sendDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if atomic.LoadInt32(&m.failCount) > 0 {
		atomic.AddInt32(&m.failCount, -1)
		return nil, errors.New("temporary error")
	}
	atomic.AddInt32(&m.sentCount, 1)
	return &tg.Message{ID: int(atomic.LoadInt32(&m.sentCount)), Message: text}, nil
}

func newBroadcastService(t *testing.T, telegram core.TelegramServicer) *broadcast.Service {
	t.Helper()
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"general": {Concurrency: 4, BacklogLimit: 128, PayloadBudget: 1 << 20},
		},
		ResourceCapacities: map[string]int64{"media": 2},
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("start task engine: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = engine.Stop(ctx)
	})
	svc := broadcast.NewService(telegram, zap.NewNop())
	svc.SetTasks(engine)
	return svc
}

func TestBroadcast_Success(t *testing.T) {
	mockTG := &mockTelegram{}
	svc := newBroadcastService(t, mockTG)

	targets := []tg.InputPeerClass{
		&tg.InputPeerUser{UserID: 1},
		&tg.InputPeerUser{UserID: 2},
		&tg.InputPeerUser{UserID: 3},
	}

	var progressReports int32
	var finalProgress broadcast.BroadcastReport
	req := broadcast.BroadcastRequest{
		Targets: targets,
		Text:    "Hello Broadcast!",
		Delay:   5 * time.Millisecond,
		Progress: func(report broadcast.BroadcastReport) {
			atomic.AddInt32(&progressReports, 1)
			finalProgress = report
		},
	}

	ctx := context.Background()
	rep, err := svc.Broadcast(ctx, req)
	if err != nil {
		t.Fatalf("Broadcast failed: %v", err)
	}

	if rep.Total != 3 || rep.Sent != 3 || rep.Failed != 0 {
		t.Errorf("unexpected report: %+v", rep)
	}
	if got := atomic.LoadInt32(&progressReports); got < 1 || got >= int32(len(targets)) {
		t.Errorf("expected coalesced progress callbacks, got %d for %d targets", got, len(targets))
	}
	if finalProgress.Sent != 3 || finalProgress.Failed != 0 || finalProgress.Total != 3 {
		t.Errorf("terminal progress snapshot=%+v", finalProgress)
	}
}

type rateLimitedTelegram struct {
	core.MockTelegramServicer
	calls int32
}

func (m *rateLimitedTelegram) SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error) {
	atomic.AddInt32(&m.calls, 1)
	return nil, core.NewRateLimitError(30*time.Second, errors.New("telegram flood wait"))
}

func TestBroadcast_RateLimitIsNotRetriedLocally(t *testing.T) {
	mockTG := &rateLimitedTelegram{}
	svc := newBroadcastService(t, mockTG)

	rep, err := svc.Broadcast(context.Background(), broadcast.BroadcastRequest{
		Targets: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 1}},
		Text:    "hello",
		Delay:   time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("broadcast should account for a per-target rate limit without failing the whole run: %v", err)
	}
	if got := atomic.LoadInt32(&mockTG.calls); got != 1 {
		t.Fatalf("expected exactly one SendMessage call and no local retry, got %d", got)
	}
	if rep == nil || rep.RateLimited != 1 || rep.Failed != 1 || rep.Sent != 0 {
		t.Fatalf("unexpected broadcast report: %+v", rep)
	}
}

func TestBroadcast_Validation(t *testing.T) {
	mockTG := &mockTelegram{}
	svc := newBroadcastService(t, mockTG)
	ctx := context.Background()

	// Empty targets
	_, err := svc.Broadcast(ctx, broadcast.BroadcastRequest{Text: "Test"})
	if !errors.Is(err, core.ErrInvalidArgs) {
		t.Errorf("expected ErrInvalidArgs on empty targets, got %v", err)
	}

	// Empty text
	_, err = svc.Broadcast(ctx, broadcast.BroadcastRequest{
		Targets: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 1}},
	})
	if !errors.Is(err, core.ErrInvalidArgs) {
		t.Errorf("expected ErrInvalidArgs on empty text, got %v", err)
	}
}

func TestBroadcast_CancelActive(t *testing.T) {
	mockTG := &mockTelegram{sendDelay: 50 * time.Millisecond}
	svc := newBroadcastService(t, mockTG)

	var targets []tg.InputPeerClass
	for i := 0; i < 20; i++ {
		targets = append(targets, &tg.InputPeerUser{UserID: int64(i + 1)})
	}

	req := broadcast.BroadcastRequest{
		Targets: targets,
		Text:    "Long broadcast",
	}

	ctx := context.Background()
	done := make(chan struct{})

	go func() {
		defer close(done)
		_, _ = svc.Broadcast(ctx, req)
	}()

	// Wait briefly for at least 1 message then cancel
	time.Sleep(20 * time.Millisecond)
	canceled := svc.CancelActive()
	if !canceled {
		t.Errorf("expected CancelActive to return true")
	}

	<-done
	sent := atomic.LoadInt32(&mockTG.sentCount)
	if sent >= 20 {
		t.Errorf("expected broadcast to cancel before sending all 20, sent: %d", sent)
	}
}

func TestBroadcast_BackpressuresInsteadOfDroppingOnTaskBacklog(t *testing.T) {
	mockTG := &mockTelegram{sendDelay: 10 * time.Millisecond}
	svc := newBroadcastService(t, mockTG)

	const targetCount = 200 // larger than the test engine backlog (128)
	targets := make([]tg.InputPeerClass, 0, targetCount)
	for i := 0; i < targetCount; i++ {
		targets = append(targets, &tg.InputPeerUser{UserID: int64(i + 1)})
	}

	rep, err := svc.Broadcast(context.Background(), broadcast.BroadcastRequest{
		Targets: targets,
		Text:    "bounded broadcast",
	})
	if err != nil {
		t.Fatalf("Broadcast failed: %v", err)
	}
	if rep.Sent != targetCount || rep.Failed != 0 {
		t.Fatalf("temporary TaskEngine backlog saturation dropped targets: %+v", rep)
	}
	if got := atomic.LoadInt32(&mockTG.sentCount); got != targetCount {
		t.Fatalf("sent count = %d, want %d", got, targetCount)
	}
}

type richBroadcastTelegram struct {
	core.MockTelegramServicer
	mediaCalls    int32
	textCalls     int32
	caption       string
	mediaPath     string
	sawMediaLease bool
}

func (m *richBroadcastTelegram) SendMessage(_ context.Context, _ tg.InputPeerClass, text string) (*tg.Message, error) {
	atomic.AddInt32(&m.textCalls, 1)
	return &tg.Message{ID: int(atomic.LoadInt32(&m.textCalls)), Message: text}, nil
}

func (m *richBroadcastTelegram) SendMedia(
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
	atomic.AddInt32(&m.mediaCalls, 1)
	m.caption = caption
	m.mediaPath = filePath
	m.sawMediaLease = tasks.HasHeldResource(ctx, "media")
	return &tg.Message{ID: 700}, nil
}

func TestBroadcast_RichSavedResponseUsesSharedDeliveryAndMediaLease(t *testing.T) {
	telegram := &richBroadcastTelegram{}
	svc := newBroadcastService(t, telegram)
	store := storage.NewMemoryStorage()
	manager, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	responses := savedresponse.NewService(store)
	responses.SetFiles(manager.ForOwner("broadcast-service-test"))
	svc.SetResponses(responses)

	asset, err := store.Put(context.Background(), strings.NewReader("broadcast-photo"), storage.Metadata{
		Name: "broadcast.jpg",
		MIME: "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := savedresponse.NewHTML("Hello {name}")
	response.Media = &savedresponse.MediaRef{
		AssetID: asset.ID, MediaType: "photo", Name: asset.Name, MIMEType: asset.MIME,
	}

	rep, err := svc.Broadcast(context.Background(), broadcast.BroadcastRequest{
		Targets:  []tg.InputPeerClass{&tg.InputPeerUser{UserID: 1}},
		Response: response,
		Vars:     savedresponse.TemplateVars{Name: "Alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sent != 1 || rep.Failed != 0 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if got := atomic.LoadInt32(&telegram.mediaCalls); got != 1 {
		t.Fatalf("media calls=%d, want 1", got)
	}
	if got := atomic.LoadInt32(&telegram.textCalls); got != 0 {
		t.Fatalf("text calls=%d, want 0 caption-only delivery", got)
	}
	if !telegram.sawMediaLease || telegram.caption != "Hello Alice" {
		t.Fatalf("rich delivery lease=%v caption=%q", telegram.sawMediaLease, telegram.caption)
	}
	if telegram.mediaPath == "" {
		t.Fatal("materialized media path was not observed")
	}
	if _, err := os.Stat(telegram.mediaPath); !os.IsNotExist(err) {
		t.Fatalf("materialized broadcast media survived delivery: %v", err)
	}
}

func TestBroadcast_RichSavedResponseCaptionOverflowFallsBackToText(t *testing.T) {
	telegram := &richBroadcastTelegram{}
	svc := newBroadcastService(t, telegram)
	store := storage.NewMemoryStorage()
	manager, err := filesystem.NewManager(filepath.Join(t.TempDir(), "files"), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	responses := savedresponse.NewService(store)
	responses.SetFiles(manager.ForOwner("broadcast-overflow-test"))
	svc.SetResponses(responses)

	asset, err := store.Put(context.Background(), strings.NewReader("broadcast-photo"), storage.Metadata{Name: "broadcast.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	response := savedresponse.NewPlainText(strings.Repeat("x", savedresponse.MaxCaptionRunes+20))
	response.Media = &savedresponse.MediaRef{AssetID: asset.ID, MediaType: "photo", Name: asset.Name}

	rep, err := svc.Broadcast(context.Background(), broadcast.BroadcastRequest{
		Targets:  []tg.InputPeerClass{&tg.InputPeerUser{UserID: 1}},
		Response: response,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sent != 1 || atomic.LoadInt32(&telegram.mediaCalls) != 1 || atomic.LoadInt32(&telegram.textCalls) != 1 {
		t.Fatalf("unexpected fallback delivery: report=%+v media=%d text=%d", rep, telegram.mediaCalls, telegram.textCalls)
	}
	if telegram.caption != "" {
		t.Fatalf("overflow caption=%q, want empty", telegram.caption)
	}
}


type pagedTargetSource struct {
	total int
	next  int
	calls int32
}

func (s *pagedTargetSource) Total() int { return s.total }

func (s *pagedTargetSource) Next(_ context.Context, limit int) ([]tg.InputPeerClass, bool, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.next >= s.total {
		return nil, true, nil
	}
	if limit <= 0 {
		limit = 1
	}
	end := s.next + limit
	if end > s.total {
		end = s.total
	}
	targets := make([]tg.InputPeerClass, 0, end-s.next)
	for i := s.next; i < end; i++ {
		targets = append(targets, &tg.InputPeerUser{UserID: int64(i + 1)})
	}
	s.next = end
	return targets, s.next >= s.total, nil
}

func TestBroadcast_TargetSourceStreamsThroughSameBoundedFanout(t *testing.T) {
	defaultTG := &mockTelegram{}
	sender := &mockTelegram{}
	svc := newBroadcastService(t, defaultTG)
	source := &pagedTargetSource{total: maxBroadcastInFlight*2 + 5}

	rep, err := svc.Broadcast(context.Background(), broadcast.BroadcastRequest{
		TargetSource: source,
		Sender:       sender,
		Text:         "audience",
	})
	if err != nil {
		t.Fatalf("Broadcast(TargetSource) error=%v", err)
	}
	if rep.Total != source.total || rep.Sent != source.total || rep.Failed != 0 {
		t.Fatalf("streamed report=%+v", rep)
	}
	if got := atomic.LoadInt32(&sender.sentCount); got != int32(source.total) {
		t.Fatalf("override sender calls=%d, want %d", got, source.total)
	}
	if got := atomic.LoadInt32(&defaultTG.sentCount); got != 0 {
		t.Fatalf("default transport received %d audience sends", got)
	}
	if got := atomic.LoadInt32(&source.calls); got < 3 {
		t.Fatalf("target source page calls=%d, want >=3", got)
	}
}

func TestBroadcast_TargetSourceAndSliceAreMutuallyExclusive(t *testing.T) {
	svc := newBroadcastService(t, &mockTelegram{})
	_, err := svc.Broadcast(context.Background(), broadcast.BroadcastRequest{
		Targets:      []tg.InputPeerClass{&tg.InputPeerUser{UserID: 1}},
		TargetSource: &pagedTargetSource{total: 1},
		Text:         "invalid",
	})
	if !errors.Is(err, core.ErrInvalidArgs) {
		t.Fatalf("Broadcast(mixed targets) error=%v, want %v", err, core.ErrInvalidArgs)
	}
}

type stalledTargetSource struct{}

func (stalledTargetSource) Total() int { return 1 }
func (stalledTargetSource) Next(context.Context, int) ([]tg.InputPeerClass, bool, error) {
	return nil, false, nil
}

func TestBroadcast_RejectsStalledTargetSource(t *testing.T) {
	svc := newBroadcastService(t, &mockTelegram{})
	_, err := svc.Broadcast(context.Background(), broadcast.BroadcastRequest{
		TargetSource: stalledTargetSource{},
		Text:         "stalled",
	})
	if !errors.Is(err, broadcast.ErrTargetSourceStalled) {
		t.Fatalf("Broadcast(stalled source) error=%v, want %v", err, broadcast.ErrTargetSourceStalled)
	}
}
