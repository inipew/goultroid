package telegram

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

type callbackRecordingService struct {
	core.MockTelegramServicer
	mu        sync.Mutex
	answered  map[int64]string
	callCount atomic.Int64
}

func newCallbackRecordingService() *callbackRecordingService {
	return &callbackRecordingService{
		answered: make(map[int64]string),
	}
}

func (s *callbackRecordingService) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	s.mu.Lock()
	s.answered[queryID] = text
	s.mu.Unlock()
	s.callCount.Add(1)
	return nil
}

func TestDispatcher_MissingCallbackRouter_GracefulFallback(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, nil, nil, logger)

	svc := newCallbackRecordingService()
	dispatcher.SetService(svc)
	// cbRouter is intentionally nil

	ctx := context.Background()

	// 1. Test OnBotCallbackQuery with nil cbRouter
	updateBot := &tg.UpdateBotCallbackQuery{
		QueryID:      1001,
		UserID:       2001,
		Peer:         &tg.PeerUser{UserID: 2001},
		MsgID:        50,
		ChatInstance: 9999,
		Data:         []byte("test_button"),
	}
	if err := dispatcher.OnBotCallbackQuery(ctx, tg.Entities{}, updateBot); err != nil {
		t.Fatalf("unexpected error on bot callback query: %v", err)
	}

	// 2. Test OnInlineBotCallbackQuery with nil cbRouter
	updateInline := &tg.UpdateInlineBotCallbackQuery{
		QueryID:      1002,
		UserID:       2001,
		ChatInstance: 9999,
		Data:         []byte("test_inline_button"),
	}
	if err := dispatcher.OnInlineBotCallbackQuery(ctx, tg.Entities{}, updateInline); err != nil {
		t.Fatalf("unexpected error on inline callback query: %v", err)
	}

	if count := svc.callCount.Load(); count != 2 {
		t.Fatalf("expected 2 AnswerCallbackQuery invocations, got %d", count)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if text := svc.answered[1001]; text != "Interaction service unavailable." {
		t.Errorf("expected 'Interaction service unavailable.', got '%s'", text)
	}
	if text := svc.answered[1002]; text != "Interaction service unavailable." {
		t.Errorf("expected 'Interaction service unavailable.', got '%s'", text)
	}
}

func TestDispatcher_CallbackOrderingKey(t *testing.T) {
	// 1. Message target with chat and msg ID
	evt1 := &core.CallbackQueryEvent{
		ChatID:  -100123456789,
		MsgID:   42,
		QueryID: 999,
		Origin:  core.CallbackOriginMessage,
	}
	key1 := callbackOrderingKey(evt1)
	if expected := "callback:msg:-100123456789:42"; key1 != expected {
		t.Errorf("expected %s, got %s", expected, key1)
	}

	// 2. Message target with zero ChatID but MsgID
	evt2 := &core.CallbackQueryEvent{
		ChatID:  0,
		MsgID:   88,
		QueryID: 1000,
		Origin:  core.CallbackOriginMessage,
	}
	key2 := callbackOrderingKey(evt2)
	if expected := "callback:msg:88"; key2 != expected {
		t.Errorf("expected %s, got %s", expected, key2)
	}

	// 3. Inline message target with DCID and ID
	evt3 := &core.CallbackQueryEvent{
		QueryID: 1001,
		Origin:  core.CallbackOriginInline,
		Target: core.CallbackTarget{
			Origin:   core.CallbackOriginInline,
			InlineID: &tg.InputBotInlineMessageID{DCID: 2, ID: 12345},
		},
	}
	key3 := callbackOrderingKey(evt3)
	if expected := "callback:inline_msg:2:12345"; key3 != expected {
		t.Errorf("expected %s, got %s", expected, key3)
	}

	// 4. Inline message target with ChatInstance fallback
	evt4 := &core.CallbackQueryEvent{
		QueryID:      1002,
		Origin:       core.CallbackOriginInline,
		ChatInstance: 55555,
	}
	key4 := callbackOrderingKey(evt4)
	if expected := "callback:instance:55555"; key4 != expected {
		t.Errorf("expected %s, got %s", expected, key4)
	}
}

func TestDispatcher_CanonicalPipeline_Normalizer(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, nil, nil, logger)

	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatalf("start event bus: %v", err)
	}
	defer bus.Close()
	dispatcher.SetEventBus(bus)

	var receivedEdit *core.MessageEditedEvent
	var receivedDel *core.MessagesDeletedEvent
	var receivedReaction *core.ReactionUpdatedEvent
	var wg sync.WaitGroup

	unsubEdit := bus.Subscribe(core.EventTypeMessageEdited, func(e core.Event) {
		if evt, ok := e.(*core.MessageEditedEvent); ok {
			receivedEdit = evt
			wg.Done()
		}
	})
	defer unsubEdit()

	unsubDel := bus.Subscribe(core.EventTypeMessagesDeleted, func(e core.Event) {
		if evt, ok := e.(*core.MessagesDeletedEvent); ok {
			receivedDel = evt
			wg.Done()
		}
	})
	defer unsubDel()

	unsubReaction := bus.Subscribe(core.EventTypeReactionUpdated, func(e core.Event) {
		if evt, ok := e.(*core.ReactionUpdatedEvent); ok {
			receivedReaction = evt
			wg.Done()
		}
	})
	defer unsubReaction()

	ctx := context.Background()

	// 1. Test OnEditMessage
	wg.Add(1)
	err := dispatcher.OnEditMessage(ctx, tg.Entities{}, &tg.UpdateEditMessage{
		Message: &tg.Message{
			ID:      501,
			PeerID:  &tg.PeerUser{UserID: 123},
			Message: "edited text",
		},
	})
	if err != nil {
		t.Fatalf("OnEditMessage error: %v", err)
	}

	// 2. Test OnDeleteMessages
	wg.Add(1)
	err = dispatcher.OnDeleteMessages(ctx, tg.Entities{}, &tg.UpdateDeleteMessages{
		Messages: []int{501, 502},
	})
	if err != nil {
		t.Fatalf("OnDeleteMessages error: %v", err)
	}

	// 3. Test OnMessageReactions
	wg.Add(1)
	err = dispatcher.OnMessageReactions(ctx, tg.Entities{}, &tg.UpdateMessageReactions{
		Peer:  &tg.PeerUser{UserID: 123},
		MsgID: 501,
	})
	if err != nil {
		t.Fatalf("OnMessageReactions error: %v", err)
	}

	wg.Wait()

	if receivedEdit == nil || receivedEdit.MsgID != 501 || receivedEdit.Text != "edited text" {
		t.Errorf("unexpected receivedEdit: %+v", receivedEdit)
	}
	if receivedDel == nil || len(receivedDel.MsgIDs) != 2 || !receivedDel.PeerUnknown {
		t.Errorf("unexpected receivedDel: %+v", receivedDel)
	}
	if receivedReaction == nil || receivedReaction.MsgID != 501 || receivedReaction.ChatID != 123 {
		t.Errorf("unexpected receivedReaction: %+v", receivedReaction)
	}
}

func TestDispatcher_PeerCoalescingDeduplication(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, nil, nil, logger)

	db, err := database.Open(fmt.Sprintf("file:peer_coalesce_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	storage := NewPeerStorage(db)
	resolver := NewResolver(nil, nil)
	resolver.SetStorage(storage)
	dispatcher.SetResolver(resolver)

	ctx := context.Background()
	if err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("dispatcher start: %v", err)
	}

	// Send 10 messages burst with the same user ID 1001, but varying titles/usernames
	entities := tg.Entities{
		Users: map[int64]*tg.User{
			1001: {ID: 1001, AccessHash: 5555, Username: "burstuser"},
		},
	}
	for i := 0; i < 10; i++ {
		err := dispatcher.OnNewMessage(ctx, entities, &tg.UpdateNewMessage{
			Message: &tg.Message{ID: i + 1, Message: fmt.Sprintf("burst %d", i)},
		})
		if err != nil {
			t.Fatalf("OnNewMessage burst %d error: %v", i, err)
		}
	}

	stopCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := dispatcher.Stop(stopCtx); err != nil {
		t.Fatalf("dispatcher stop: %v", err)
	}

	val, found, err := storage.Find(ctx, peers.Key{Prefix: "user", ID: 1001})
	if err != nil || !found || val.AccessHash != 5555 {
		t.Fatalf("expected user 1001 in storage with hash 5555, found=%v, val=%+v, err=%v", found, val, err)
	}
}

func TestDispatcher_PeerCoalescingBurstDoesNotDropRepeatedPeer(t *testing.T) {
	dispatcher := NewDispatcher(core.NewRouter("."), nil, nil, zap.NewNop())
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	entities := tg.Entities{Users: map[int64]*tg.User{1001: {ID: 1001, AccessHash: 55}}}
	for i := 0; i < 5000; i++ {
		dispatcher.enqueuePeerEntities(entities)
	}
	_, dropped, _ := dispatcher.PeerCacheStats()
	if dropped != 0 {
		t.Fatalf("repeated peer burst dropped %d updates", dropped)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
