package telegram

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/core"
)

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

	// 4. Permission denied errors
	adminReqErr := tgerr.New(400, "CHAT_ADMIN_REQUIRED")
	mappedAdmin := mapTelegramError(adminReqErr)
	if !errors.Is(mappedAdmin, core.ErrPermissionDenied) {
		t.Errorf("expected mappedAdmin to match ErrPermissionDenied, got %v", mappedAdmin)
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
}

func TestRetryOnFloodWait_ExceedsLimit(t *testing.T) {
	ctx := context.Background()
	callCount := 0
	floodErr := tgerr.New(420, "FLOOD_WAIT_60")

	res, err := retryOnFloodWait(ctx, func() (string, error) {
		callCount++
		return "", floodErr
	})

	if res != "" {
		t.Errorf("expected empty result, got %q", res)
	}
	if callCount != 1 {
		t.Errorf("expected exactly 1 call when flood wait exceeds limit, got %d", callCount)
	}
	if !errors.Is(err, core.ErrRateLimited) {
		t.Errorf("expected ErrRateLimited, got %v", err)
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

func TestService_InvalidatePeerOnInvalid(t *testing.T) {
	svc := NewService(nil)
	storage := &mockInvalidatingStorage{}
	svc.SetStorage(storage)

	peer := &tg.InputPeerChannel{ChannelID: 12345, AccessHash: 9999}
	svc.checkPeerError(tgerr.New(400, "CHANNEL_INVALID"), peer)

	if len(storage.invalidated) != 1 {
		t.Fatalf("expected 1 invalidated key, got %d", len(storage.invalidated))
	}
	if storage.invalidated[0].Prefix != "channel" || storage.invalidated[0].ID != 12345 {
		t.Errorf("unexpected invalidated key: %+v", storage.invalidated[0])
	}
}
