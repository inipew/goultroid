package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

func TestParseHTML(t *testing.T) {
	helpText := "📚 <b>GoUltroid Help</b>\n" +
		"<i>80 commands across 15 modules.</i>\n\n" +
		"📂 <b>AFK</b> <code>(1)</code>\n" +
		"<code>.afk</code>\n\n" +
		"💡 <i>Use <code>.help &lt;module&gt;</code> or <code>.help &lt;command&gt;</code> for details.</i>"

	plain, ents := parseHTML(helpText)
	if strings.Contains(plain, "<b>") || strings.Contains(plain, "</b>") || strings.Contains(plain, "<code>") {
		t.Errorf("plain text still contains raw HTML tags: %s", plain)
	}
	if len(ents) == 0 {
		t.Errorf("expected entities from HTML string, got 0")
	}

	// Config usage with escaped entities
	configUsage := "⚙️ <b>GoUltroid CLI Configuration Subsystem</b>\n\n" +
		"<b>Usage:</b>\n" +
		"• <code>.config get &lt;namespace:key&gt;</code>\n" +
		"• <code>.config set &lt;namespace:key&gt; &lt;value&gt;</code>\n"
	p3, ents3 := parseHTML(configUsage)
	if !strings.Contains(p3, ".config get <namespace:key>") {
		t.Errorf("expected '.config get <namespace:key>', got: %s", p3)
	}
	if !strings.Contains(p3, ".config set <namespace:key> <value>") {
		t.Errorf("expected '.config set <namespace:key> <value>', got: %s", p3)
	}
	if len(ents3) == 0 {
		t.Errorf("expected entities for config usage, got 0")
	}
}

// Ensure Service implements core.TelegramServicer.
var _ core.TelegramServicer = (*Service)(nil)

func TestExtractMessageFromUpdates(t *testing.T) {
	// 1. tg.Updates with UpdateNewMessage
	msg := &tg.Message{ID: 101, Message: "hello"}
	upd := &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewMessage{Message: msg},
		},
	}
	extracted := extractMessageFromUpdates(upd)
	if extracted == nil || extracted.ID != 101 {
		t.Errorf("expected extracted message ID 101, got %v", extracted)
	}

	// 2. tg.Updates with UpdateNewChannelMessage
	chMsg := &tg.Message{ID: 202, Message: "channel msg"}
	updCh := &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewChannelMessage{Message: chMsg},
		},
	}
	extractedCh := extractMessageFromUpdates(updCh)
	if extractedCh == nil || extractedCh.ID != 202 {
		t.Errorf("expected extracted channel message ID 202, got %v", extractedCh)
	}

	// 3. tg.UpdateShortSentMessage
	shortSent := &tg.UpdateShortSentMessage{ID: 303, Date: 12345}
	extractedShort := extractMessageFromUpdates(shortSent)
	if extractedShort == nil || extractedShort.ID != 303 {
		t.Errorf("expected extracted short sent message ID 303, got %v", extractedShort)
	}

	// 4. tg.UpdateShortMessage
	shortMsg := &tg.UpdateShortMessage{ID: 404, Message: "short", Date: 67890}
	extractedShortMsg := extractMessageFromUpdates(shortMsg)
	if extractedShortMsg == nil || extractedShortMsg.ID != 404 || extractedShortMsg.Message != "short" {
		t.Errorf("expected extracted short message ID 404, got %v", extractedShortMsg)
	}

	// 5. Unknown update class
	if extractMessageFromUpdates(&tg.UpdatesTooLong{}) != nil {
		t.Errorf("expected nil for non-message update")
	}
}

func TestDeleteMessage_EmptyIDs(t *testing.T) {
	s := &Service{}
	err := s.DeleteMessage(context.Background(), &tg.InputPeerSelf{}, nil)
	if err != nil {
		t.Errorf("expected no error when deleting empty message IDs: %v", err)
	}
}

func TestCheckRestartState_FileHandling(t *testing.T) {
	// 1. Missing file -> no-op, no panic
	checkRestartState(context.Background(), nil, nil)

	// 2. Self peer state
	_ = os.MkdirAll("data", 0755)
	defer os.RemoveAll("data")

	selfJSON := []byte(`{"peer_type":"self","chat_id":0,"msg_id":99,"time":1700000000}`)
	_ = os.WriteFile("data/restart.json", selfJSON, 0644)

	// Calls checkRestartState with nil sender; svc.EditMessage will fail safely and remove file
	checkRestartState(context.Background(), &Service{}, nil)

	if _, err := os.Stat("data/restart.json"); !os.IsNotExist(err) {
		t.Errorf("expected data/restart.json to be removed by checkRestartState")
	}

	// 3. User peer with access hash
	userJSON := []byte(`{"peer_type":"user","chat_id":12345,"access_hash":67890,"msg_id":101,"time":1700000000}`)
	_ = os.WriteFile("data/restart.json", userJSON, 0644)
	checkRestartState(context.Background(), &Service{}, nil)

	if _, err := os.Stat("data/restart.json"); !os.IsNotExist(err) {
		t.Errorf("expected data/restart.json to be removed")
	}

	// 4. Legacy format fallback
	legacyJSON := []byte(`{"chat_id":777,"is_channel":true,"access_hash":888,"msg_id":102,"time":1700000000}`)
	_ = os.WriteFile("data/restart.json", legacyJSON, 0644)
	checkRestartState(context.Background(), &Service{}, nil)

	if _, err := os.Stat("data/restart.json"); !os.IsNotExist(err) {
		t.Errorf("expected legacy data/restart.json to be removed")
	}
}

func TestUnbanUser_UnsupportedPeer(t *testing.T) {
	// Mock tg client with nil api
	svc := &Service{api: &tg.Client{}}

	// Calling UnbanUser with *tg.InputPeerChat or *tg.InputPeerUser should return an explicit error
	err := svc.UnbanUser(context.Background(), &tg.InputPeerChat{ChatID: 12345}, &tg.InputPeerUser{UserID: 67890})
	if err == nil {
		t.Fatalf("expected error for unsupported peer type in UnbanUser, got nil")
	}
}

func TestMapTelegramError(t *testing.T) {
	// 1. Nil error
	if err := mapTelegramError(nil); err != nil {
		t.Errorf("expected nil for nil error, got %v", err)
	}

	// 2. Flood wait error
	floodErr := tgerr.New(420, "FLOOD_WAIT_10")
	mappedFlood := mapTelegramError(floodErr)
	if !errors.Is(mappedFlood, core.ErrRateLimited) {
		t.Errorf("expected mappedFlood to match ErrRateLimited, got %v", mappedFlood)
	}
	var rle *core.RateLimitError
	if !errors.As(mappedFlood, &rle) || rle.Wait != 10*time.Second {
		t.Errorf("expected RateLimitError with wait 10s, got %+v", rle)
	}

	// 3. Not found errors
	chatInvalidErr := tgerr.New(400, "CHAT_ID_INVALID")
	mappedChat := mapTelegramError(chatInvalidErr)
	if !errors.Is(mappedChat, core.ErrNotFound) {
		t.Errorf("expected mappedChat to match ErrNotFound, got %v", mappedChat)
	}
	if !errors.Is(mappedChat, chatInvalidErr) {
		t.Errorf("expected mappedChat to preserve raw Telegram error cause, got %v", mappedChat)
	}

	// 4. Permission denied errors
	adminReqErr := tgerr.New(400, "CHAT_ADMIN_REQUIRED")
	mappedAdmin := mapTelegramError(adminReqErr)
	if !errors.Is(mappedAdmin, core.ErrPermissionDenied) {
		t.Errorf("expected mappedAdmin to match ErrPermissionDenied, got %v", mappedAdmin)
	}
	if !errors.Is(mappedAdmin, adminReqErr) {
		t.Errorf("expected mappedAdmin to preserve raw Telegram error cause, got %v", mappedAdmin)
	}

	// 4b. Idempotent success errors (RIGHTS_NOT_MODIFIED, CHAT_NOT_MODIFIED)
	rightsNotModErr := tgerr.New(400, "RIGHTS_NOT_MODIFIED")
	if err := mapTelegramError(rightsNotModErr); err != nil {
		t.Errorf("expected nil for RIGHTS_NOT_MODIFIED, got %v", err)
	}
	chatNotModErr := tgerr.New(400, "CHAT_NOT_MODIFIED")
	if err := mapTelegramError(chatNotModErr); err != nil {
		t.Errorf("expected nil for CHAT_NOT_MODIFIED, got %v", err)
	}

	// 5. Generic telegram error
	genErr := tgerr.New(500, "INTERNAL_SERVER_ERROR")
	mappedGen := mapTelegramError(genErr)
	if !errors.Is(mappedGen, core.ErrTelegram) {
		t.Errorf("expected mappedGen to match ErrTelegram, got %v", mappedGen)
	}
	if !errors.Is(mappedGen, genErr) {
		t.Errorf("expected mappedGen to preserve raw Telegram error cause, got %v", mappedGen)
	}
}

func TestEditChatDefaultBannedRights_UnsupportedPeer(t *testing.T) {
	svc := &Service{api: &tg.Client{}}
	ctx := context.Background()

	// 1. InputPeerUser is unsupported for chat default banned rights
	err := svc.EditChatDefaultBannedRights(ctx, &tg.InputPeerUser{UserID: 123}, tg.ChatBannedRights{})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported for InputPeerUser, got %v", err)
	}

	// 2. InputPeerSelf is unsupported
	err = svc.EditChatDefaultBannedRights(ctx, &tg.InputPeerSelf{}, tg.ChatBannedRights{})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported for InputPeerSelf, got %v", err)
	}
}

func TestPurgeMessages_NilAPI(t *testing.T) {
	svc := &Service{api: nil}
	_, err := svc.PurgeMessages(context.Background(), &tg.InputPeerChat{ChatID: 123}, 0, 10, 20)
	if err == nil {
		t.Errorf("expected error when api is nil, got nil")
	}
}

func TestBoundedWriter_LimitEnforcement(t *testing.T) {
	var buf strings.Builder
	bw := &boundedWriter{
		writer: &buf,
		limit:  100,
	}

	// 1. Write within limit
	n, err := bw.Write([]byte(strings.Repeat("a", 80)))
	if err != nil || n != 80 {
		t.Fatalf("expected 80 bytes written, got %d, err: %v", n, err)
	}

	// 2. Write exceeding limit
	_, err = bw.Write([]byte(strings.Repeat("b", 30)))
	if err == nil || !errors.Is(err, core.ErrMediaTooLarge) {
		t.Errorf("expected ErrMediaTooLarge when exceeding limit, got %v", err)
	}
}

func TestSendMedia_UploadSizeLimit(t *testing.T) {
	tmpDir := t.TempDir()
	largeFile := tmpDir + "/large.bin"
	f, err := os.Create(largeFile)
	if err != nil {
		t.Fatalf("failed to create large test file: %v", err)
	}
	// Truncate to 501 MB (exceeds DefaultMaxUploadSize 500MB)
	if err := f.Truncate(core.DefaultMaxUploadSize + 1024); err != nil {
		f.Close()
		t.Fatalf("failed to truncate large test file: %v", err)
	}
	f.Close()

	svc := &Service{sender: &message.Sender{}}
	_, err = svc.SendMedia(context.Background(), &tg.InputPeerSelf{}, "file", largeFile, "")
	if err == nil || !errors.Is(err, core.ErrMediaTooLarge) {
		t.Errorf("expected ErrMediaTooLarge for oversized upload file, got %v", err)
	}
}

func TestService_MarkupAndAnswerMethods_NilInit(t *testing.T) {
	svc := &Service{}
	ctx := context.Background()

	_, err := svc.SendMessageWithMarkup(ctx, &tg.InputPeerSelf{}, "hello", nil)
	if err == nil || !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal for uninitialized sender, got %v", err)
	}

	err = svc.EditMessageMarkup(ctx, &tg.InputPeerSelf{}, 1, "hello", nil)
	if err == nil || !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal for uninitialized api, got %v", err)
	}

	err = svc.AnswerCallbackQuery(ctx, 123, "toast", false)
	if err == nil || !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal for uninitialized api, got %v", err)
	}

	err = svc.AnswerInlineQuery(ctx, 123, nil, "", 0)
	if err == nil || !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal for uninitialized api, got %v", err)
	}
}

type mockInvalidatingStorage struct {
	invalidated []peers.Key
}

func (m *mockInvalidatingStorage) Save(ctx context.Context, key peers.Key, value peers.Value) error {
	return nil
}

func (m *mockInvalidatingStorage) Find(ctx context.Context, key peers.Key) (peers.Value, bool, error) {
	return peers.Value{}, false, nil
}

func (m *mockInvalidatingStorage) SavePhone(ctx context.Context, phone string, key peers.Key) error {
	return nil
}

func (m *mockInvalidatingStorage) FindPhone(ctx context.Context, phone string) (peers.Key, peers.Value, bool, error) {
	return peers.Key{}, peers.Value{}, false, nil
}

func (m *mockInvalidatingStorage) GetContactsHash(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockInvalidatingStorage) SaveContactsHash(ctx context.Context, hash int64) error {
	return nil
}

func (m *mockInvalidatingStorage) Invalidate(key peers.Key) error {
	m.invalidated = append(m.invalidated, key)
	return nil
}

func (m *mockInvalidatingStorage) InvalidateContext(_ context.Context, key peers.Key) error {
	return m.Invalidate(key)
}

func TestService_InvalidatePeerOnInvalid(t *testing.T) {
	svc := NewService(nil)
	storage := &mockInvalidatingStorage{}
	svc.SetStorage(storage)

	peer := &tg.InputPeerChannel{ChannelID: 12345, AccessHash: 9999}
	svc.checkPeerError(context.Background(), tgerr.New(400, "CHANNEL_INVALID"), peer)

	if len(storage.invalidated) != 1 {
		t.Fatalf("expected 1 invalidated key, got %d", len(storage.invalidated))
	}
	if storage.invalidated[0].Prefix != "channel" || storage.invalidated[0].ID != 12345 {
		t.Errorf("unexpected invalidated key: %+v", storage.invalidated[0])
	}
}

type rotatingPeerStorage struct {
	hash  int64
	calls int
}

func (m *rotatingPeerStorage) Save(context.Context, peers.Key, peers.Value) error { return nil }
func (m *rotatingPeerStorage) Find(context.Context, peers.Key) (peers.Value, bool, error) {
	return peers.Value{AccessHash: m.hash}, true, nil
}
func (m *rotatingPeerStorage) SavePhone(context.Context, string, peers.Key) error { return nil }
func (m *rotatingPeerStorage) FindPhone(context.Context, string) (peers.Key, peers.Value, bool, error) {
	return peers.Key{}, peers.Value{}, false, nil
}
func (m *rotatingPeerStorage) GetContactsHash(context.Context) (int64, error) { return 0, nil }
func (m *rotatingPeerStorage) SaveContactsHash(context.Context, int64) error  { return nil }

func TestService_PeerAwareRetryReloadsAccessHashPerAttempt(t *testing.T) {
	storage := &rotatingPeerStorage{hash: 111}
	svc := NewService(nil)
	svc.SetStorage(storage)
	exec := newTestExecutor(nil, NewFakeClock(time.Now()), &FakeSleeper{}, nil)
	svc.SetExecutor(exec)

	var seen []int64
	err := svc.execIdempotentPeer(context.Background(), "messages.editMessage", &tg.InputPeerUser{UserID: 42, AccessHash: 111}, func(_ context.Context, current tg.InputPeerClass) error {
		u := current.(*tg.InputPeerUser)
		seen = append(seen, u.AccessHash)
		storage.calls++
		if storage.calls == 1 {
			storage.hash = 222
			return tgerr.New(500, "RPC_CALL_FAIL")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected peer-aware retry error: %v", err)
	}
	if len(seen) != 2 || seen[0] != 111 || seen[1] != 222 {
		t.Fatalf("expected access hashes [111 222], got %v", seen)
	}
}

func TestService_NonIdempotentMutationNoRetryOnTransient(t *testing.T) {
	svc := NewService(nil)
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)
	svc.SetExecutor(exec)

	var attempts int
	err := svc.execNonIdempotent(context.Background(), "messages.sendMessage", func(opCtx context.Context) error {
		attempts++
		return tgerr.New(500, "RPC_CALL_FAIL")
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("expected exactly 1 attempt for non-idempotent mutation, got %d", attempts)
	}
}

func TestService_ContextPropagationToExecutor(t *testing.T) {
	svc := NewService(nil)
	clock := NewFakeClock(time.Now())
	sleeper := &FakeSleeper{}
	exec := newTestExecutor(nil, clock, sleeper, nil)
	svc.SetExecutor(exec)

	var hasDeadline bool
	err := svc.execReadOnly(context.Background(), "users.getMe", func(opCtx context.Context) error {
		_, hasDeadline = opCtx.Deadline()
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasDeadline {
		t.Fatal("expected opCtx passed to operation to have a deadline derived from executor")
	}
}

func TestServiceMissingExecutorFailsClosed(t *testing.T) {
	svc := &Service{}
	called := false
	_, err := executeServiceRPC(context.Background(), svc, RPCMeta{
		Method: "users.getMe",
		Kind:   RPCReadOnly,
	}, func(context.Context) (struct{}, error) {
		called = true
		return struct{}{}, nil
	})
	if err == nil || !errors.Is(err, core.ErrInternal) {
		t.Fatalf("expected missing-executor internal error, got %v", err)
	}
	if called {
		t.Fatal("physical operation ran without RPC executor")
	}
}

func TestStandaloneServiceUsesBoundedExecutor(t *testing.T) {
	svc := NewService(nil)
	if svc.getExecutor() == nil {
		t.Fatal("standalone service did not construct an executor")
	}
	if _, ok := svc.getExecutor().limiter.(*HierarchicalRPCLimiter); !ok {
		t.Fatalf("standalone limiter type=%T, want *HierarchicalRPCLimiter", svc.getExecutor().limiter)
	}
}

func TestService_SetExecutor(t *testing.T) {
	svc := NewService(nil)
	exec, err := NewRPCExecutor(RPCExecutorConfig{
		DefaultPolicy: DefaultExecutorPolicy,
	})
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}
	svc.SetExecutor(exec)
	if svc.getExecutor() != exec {
		t.Fatal("expected configured executor to be returned")
	}
}

func TestService_StalePeerAutoRefreshRecovery(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(fmt.Sprintf("file:stale_recovery_test_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	storage := NewPeerStorage(db)
	_ = storage.Save(ctx, peers.Key{Prefix: "user", ID: 12345}, peers.Value{AccessHash: 88888})
	_ = storage.SaveEntity(ctx, "user", 12345, "target", "", "", "", "")

	svc := NewService(nil)
	exec, err := NewRPCExecutor(RPCExecutorConfig{
		DefaultPolicy: RetryPolicy{
			MaxAttempts: 3,
			BaseDelay:   10 * time.Millisecond,
			MaxDelay:    50 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}
	svc.SetExecutor(exec)

	resolver := NewResolver(nil, nil)
	resolver.SetStorage(storage)
	svc.SetResolver(resolver)
	svc.SetStorage(storage)

	// Populate cache with stale hash for @target
	resolver.cache.Set("user", "target", "user", 12345, 99999)

	// Request starts with stale access hash 99999
	req := &tg.InputPeerUser{UserID: 12345, AccessHash: 99999}
	var calls int
	var executedHashes []int64

	meta := RPCMeta{
		Method:  "users.getFullUser",
		Kind:    RPCReadOnly,
		PeerKey: "@target",
		RefreshPeer: func(context.Context) error {
			// This fixture models the case where persistence already observed a
			// newer access hash than the request-local peer. The production
			// default refresh path intentionally forces network refresh when the
			// persistent hash itself may be stale.
			resolver.cache.Invalidate("user", "target")
			return nil
		},
	}

	res, err := executeServiceRPC(context.Background(), svc, meta, func(opCtx context.Context) (string, error) {
		calls++
		// Re-evaluate peer access hash from service on retry attempts (stale recovery flow)
		if calls > 1 {
			refreshed := svc.RefreshPeerAccessHash(opCtx, req)
			if u, ok := refreshed.(*tg.InputPeerUser); ok {
				req.AccessHash = u.AccessHash
			}
		}
		executedHashes = append(executedHashes, req.AccessHash)

		// The mock Telegram RPC server strictly rejects the stale hash 99999
		if req.AccessHash != 88888 {
			return "", tgerr.New(400, "PEER_ID_INVALID")
		}
		return "recovered", nil
	})

	if err != nil {
		t.Fatalf("expected successful recovery, got: %v", err)
	}
	if res != "recovered" {
		t.Fatalf("expected result 'recovered', got %q", res)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 calls (fail then retry), got %d", calls)
	}
	// Verify that first attempt executed with 99999 and second attempt executed with updated fresh hash 88888
	if len(executedHashes) != 2 || executedHashes[0] != 99999 || executedHashes[1] != 88888 {
		t.Fatalf("expected executed hashes [99999, 88888], got %v", executedHashes)
	}
	// Check that cache was invalidated and repopulated during refresh
	if entry, hit := resolver.cache.Get("user", "target"); hit && entry.AccessHash == 99999 {
		t.Fatalf("expected cached entry with old hash to be invalidated, but still found: %+v", entry)
	}
}

func TestService_BotSentTrackingIsPeerScoped(t *testing.T) {
	svc := NewService(nil)
	svc.recordBotSent(&tg.InputPeerChat{ChatID: 10}, 77)

	if !svc.IsBotSentForPeer(&tg.PeerChat{ChatID: 10}, 77, 0) {
		t.Fatal("expected exact peer/message pair to be recognized")
	}
	if svc.IsBotSentForPeer(&tg.PeerChat{ChatID: 11}, 77, 0) {
		t.Fatal("same message id in another chat must not be classified as bot-sent")
	}
}

func TestService_BotSentTrackingSupportsSavedMessages(t *testing.T) {
	svc := NewService(nil)
	svc.recordBotSent(&tg.InputPeerSelf{}, 88)

	if !svc.IsBotSentForPeer(&tg.PeerUser{UserID: 1234}, 88, 1234) {
		t.Fatal("expected InputPeerSelf record to match current self peer")
	}
	if svc.IsBotSentForPeer(&tg.PeerUser{UserID: 9999}, 88, 1234) {
		t.Fatal("InputPeerSelf record must not match a different user peer")
	}
}

type captureDimensionsLimiter struct {
	dimensions []LimitKey
}

func (l *captureDimensionsLimiter) Reserve(_ time.Time, dimensions []LimitKey, _ int) Reservation {
	l.dimensions = append(l.dimensions[:0], dimensions...)
	return Reservation{Allowed: true}
}

func (*captureDimensionsLimiter) Penalize(time.Time, []LimitKey, time.Duration) {}

func TestServiceSinglePeerWrapperUsesTypedLimiterIdentity(t *testing.T) {
	limiter := &captureDimensionsLimiter{}
	exec, err := NewRPCExecutor(RPCExecutorConfig{
		Limiter: limiter,
		DefaultPolicy: RetryPolicy{
			MaxAttempts: 1,
			MaxElapsed:  time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewServiceWithExecutor(nil, exec)
	peer := &tg.InputPeerUser{UserID: 42, AccessHash: 99}

	err = svc.execReadOnlyPeer(context.Background(), "users.getFullUser", peer, func(_ context.Context, current tg.InputPeerClass) error {
		if current != peer {
			t.Fatalf("unchanged peer should preserve pointer identity: got %p want %p", current, peer)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("single-peer rpc failed: %v", err)
	}

	var peerDimension *LimitKey
	for i := range limiter.dimensions {
		if limiter.dimensions[i].Scope == "peer" {
			peerDimension = &limiter.dimensions[i]
			break
		}
	}
	if peerDimension == nil {
		t.Fatalf("missing peer limiter dimension: %+v", limiter.dimensions)
	}
	if peerDimension.Key != "user" || peerDimension.ID != 42 {
		t.Fatalf("unexpected typed peer dimension: %+v", *peerDimension)
	}
	if peerDimension.Key == "user:42" {
		t.Fatalf("peer limiter identity regressed to formatted string: %+v", *peerDimension)
	}
}

func TestServiceRefreshPeerAccessHashReusesUnchangedPeer(t *testing.T) {
	storage := &rotatingPeerStorage{hash: 777}
	svc := NewService(nil)
	svc.SetStorage(storage)

	user := &tg.InputPeerUser{UserID: 123, AccessHash: 777}
	if got := svc.RefreshPeerAccessHash(context.Background(), user); got != user {
		t.Fatalf("unchanged user hash allocated/replaced peer: got %p want %p", got, user)
	}

	channelStorage := &rotatingPeerStorage{hash: 888}
	svc.SetStorage(channelStorage)
	channel := &tg.InputPeerChannel{ChannelID: 456, AccessHash: 888}
	if got := svc.RefreshPeerAccessHash(context.Background(), channel); got != channel {
		t.Fatalf("unchanged channel hash allocated/replaced peer: got %p want %p", got, channel)
	}
}

func TestServiceRefreshPeerAccessHashReplacesChangedPeer(t *testing.T) {
	storage := &rotatingPeerStorage{hash: 222}
	svc := NewService(nil)
	svc.SetStorage(storage)

	original := &tg.InputPeerUser{UserID: 42, AccessHash: 111}
	got := svc.RefreshPeerAccessHash(context.Background(), original)
	refreshed, ok := got.(*tg.InputPeerUser)
	if !ok {
		t.Fatalf("refreshed peer type=%T", got)
	}
	if refreshed == original {
		t.Fatal("changed access hash must return refreshed peer")
	}
	if refreshed.UserID != original.UserID || refreshed.AccessHash != 222 {
		t.Fatalf("unexpected refreshed peer: %+v", refreshed)
	}
	if original.AccessHash != 111 {
		t.Fatalf("original peer was mutated: %+v", original)
	}
}
