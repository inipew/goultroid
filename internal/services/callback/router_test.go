package callback

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

type mockHandler struct {
	namespace string
	handled   bool
	lastCtx   *CallbackContext
	returnErr error
}

func (m *mockHandler) Namespace() string {
	return m.namespace
}

func (m *mockHandler) HandleCallback(ctx *CallbackContext) error {
	m.handled = true
	m.lastCtx = ctx
	return m.returnErr
}

type recordingService struct {
	core.MockTelegramServicer
	lastAnswerQueryID  int64
	lastAnswerText     string
	lastAnswerAlert    bool
	lastEditInlineID   tg.InputBotInlineMessageIDClass
	lastEditInlineText string
	lastEditPeer                   tg.InputPeerClass
	lastEditMsgID                  int
	lastEditText                   string
	lastEditMarkupOnlyPeer         tg.InputPeerClass
	lastEditMarkupOnlyMsgID        int
	lastEditMarkupOnlyMarkup       tg.ReplyMarkupClass
	lastEditInlineMarkupOnlyID     tg.InputBotInlineMessageIDClass
	lastEditInlineMarkupOnlyMarkup tg.ReplyMarkupClass
	lastDeletePeer                 tg.InputPeerClass
	lastDeleteMsgIDs               []int
	errToAnswer                    error
}

func (r *recordingService) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	r.lastAnswerQueryID = queryID
	r.lastAnswerText = text
	r.lastAnswerAlert = alert
	return r.errToAnswer
}

func (r *recordingService) EditInlineBotMessage(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, text string, markup tg.ReplyMarkupClass) error {
	r.lastEditInlineID = inlineID
	r.lastEditInlineText = text
	return nil
}

func (r *recordingService) EditInlineBotMessageMarkup(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, markup tg.ReplyMarkupClass) error {
	r.lastEditInlineMarkupOnlyID = inlineID
	r.lastEditInlineMarkupOnlyMarkup = markup
	return nil
}

func (r *recordingService) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	r.lastEditPeer = peer
	r.lastEditMsgID = msgID
	r.lastEditText = text
	return nil
}

func (r *recordingService) EditMessageMarkupOnly(ctx context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error {
	r.lastEditMarkupOnlyPeer = peer
	r.lastEditMarkupOnlyMsgID = msgID
	r.lastEditMarkupOnlyMarkup = markup
	return nil
}

func (r *recordingService) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	r.lastDeletePeer = peer
	r.lastDeleteMsgIDs = msgIDs
	return nil
}

func TestCallback_DataEncodingAndParsing(t *testing.T) {
	encoded := EncodeCallbackData("media", "next", "token123")
	if string(encoded) != "v1:media:next:token123" {
		t.Errorf("unexpected encoded format: %s", string(encoded))
	}

	ns, action, opaqueID, err := ParseCallbackData(encoded)
	if err != nil {
		t.Fatalf("unexpected error parsing callback data: %v", err)
	}
	if ns != "media" || action != "next" || opaqueID != "token123" {
		t.Errorf("unexpected parsed fields: %s, %s, %s", ns, action, opaqueID)
	}

	// Invalid format
	_, _, _, err = ParseCallbackData([]byte("invalid"))
	if !errors.Is(err, ErrInvalidCallbackData) {
		t.Errorf("expected ErrInvalidCallbackData, got: %v", err)
	}

	// Wrong version
	_, _, _, err = ParseCallbackData([]byte("v2:media:next:123"))
	if !errors.Is(err, ErrInvalidCallbackData) {
		t.Errorf("expected ErrInvalidCallbackData for v2, got: %v", err)
	}
}

func TestStateStore_StoreGetAndPrune(t *testing.T) {
	store := NewStateStore()

	// 1. Store and retrieve
	id := store.Store("sample-payload", 12345, 100*time.Millisecond)
	if id == "" {
		t.Fatalf("expected non-empty opaque id")
	}

	val, allowedUser, ok := store.Get(id)
	if !ok || val != "sample-payload" || allowedUser != 12345 {
		t.Errorf("unexpected retrieved state: val=%v, user=%d, ok=%v", val, allowedUser, ok)
	}

	// 2. Expiration
	time.Sleep(150 * time.Millisecond)
	_, _, ok = store.Get(id)
	if ok {
		t.Errorf("expected expired state to not be retrieved")
	}

	// 3. Pruning
	id2 := store.Store("prune-me", 0, 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	pruned := store.Prune()
	if pruned == 0 {
		t.Errorf("expected at least 1 pruned state")
	}
	_, _, ok = store.Get(id2)
	if ok {
		t.Errorf("expected pruned item to be gone")
	}
}

func TestRouter_Dispatch_Success(t *testing.T) {
	router := NewRouter(zap.NewNop(), nil)
	handler := &mockHandler{namespace: "test"}
	if err := router.Register(handler); err != nil {
		t.Fatalf("failed to register handler: %v", err)
	}

	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{
		QueryID: 999,
		UserID:  12345,
		ChatID:  -1001,
		MsgID:   50,
		Data:    EncodeCallbackData("test", "click", "abc"),
	}

	ctx := context.Background()
	err := router.Dispatch(ctx, evt, svc)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	if !handler.handled {
		t.Errorf("expected handler to be invoked")
	}
	if handler.lastCtx.Namespace != "test" || handler.lastCtx.Action != "click" || handler.lastCtx.OpaqueID != "abc" {
		t.Errorf("unexpected callback context: %+v", handler.lastCtx)
	}
	if svc.lastAnswerQueryID != 999 {
		t.Errorf("expected auto-answer with queryID 999, got %d", svc.lastAnswerQueryID)
	}
}

func TestRouter_Dispatch_Unauthorized(t *testing.T) {
	store := NewStateStore()
	router := NewRouter(zap.NewNop(), store)
	handler := &mockHandler{namespace: "private"}
	_ = router.Register(handler)

	// State restricted to user 1111
	token := store.Store("secret", 1111, 5*time.Minute)

	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{
		QueryID: 888,
		UserID:  2222, // Impostor
		Data:    EncodeCallbackData("private", "view", token),
	}

	err := router.Dispatch(context.Background(), evt, svc)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got: %v", err)
	}
	if handler.handled {
		t.Errorf("handler should not have been invoked for unauthorized user")
	}
	if !svc.lastAnswerAlert {
		t.Errorf("expected alert modal for unauthorized user")
	}
}

func TestRouter_Dispatch_HandlerNotFound(t *testing.T) {
	router := NewRouter(zap.NewNop(), nil)
	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{
		QueryID: 777,
		UserID:  12345,
		Data:    EncodeCallbackData("missing", "click", "123"),
	}

	err := router.Dispatch(context.Background(), evt, svc)
	if !errors.Is(err, ErrHandlerNotFound) {
		t.Errorf("expected ErrHandlerNotFound, got: %v", err)
	}
}

func TestRouter_RegisterValidation(t *testing.T) {
	router := NewRouter(nil, nil)

	if err := router.Register(nil); err == nil {
		t.Errorf("expected error registering nil handler")
	}

	h := &mockHandler{namespace: "dup"}
	if err := router.Register(h); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := router.Register(h); err == nil {
		t.Errorf("expected error registering duplicate handler")
	}
}

func TestRouter_Dispatch_RawNoop(t *testing.T) {
	router := NewRouter(zap.NewNop(), nil)
	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{
		QueryID: 101,
		UserID:  12345,
		Data:    []byte("noop"),
	}

	err := router.Dispatch(context.Background(), evt, svc)
	if err != nil {
		t.Fatalf("unexpected error for raw noop: %v", err)
	}
	if svc.lastAnswerQueryID != 101 {
		t.Errorf("expected silent answer with queryID 101, got %d", svc.lastAnswerQueryID)
	}
	if svc.lastAnswerText != "" || svc.lastAnswerAlert {
		t.Errorf("expected empty toast for noop button")
	}
}

func TestRouter_Dispatch_ExpiredState(t *testing.T) {
	store := NewStateStore()
	router := NewRouter(zap.NewNop(), store)
	handler := &mockHandler{namespace: "exp"}
	_ = router.Register(handler)

	token := store.Store("short-lived", 12345, 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)

	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{
		QueryID: 102,
		UserID:  12345,
		Data:    EncodeCallbackData("exp", "click", token),
	}

	err := router.Dispatch(context.Background(), evt, svc)
	if !errors.Is(err, ErrStateExpired) {
		t.Fatalf("expected ErrStateExpired, got %v", err)
	}
	if !svc.lastAnswerAlert {
		t.Errorf("expected alert toast for expired state")
	}
}

func TestRouter_Dispatch_SingleUseReplayProtection(t *testing.T) {
	store := NewStateStore()
	router := NewRouter(zap.NewNop(), store)
	handler := &mockHandler{namespace: "action"}
	_ = router.Register(handler)

	token := store.StoreWithScope("one-time-token", StateScope{
		UserID:    12345,
		SingleUse: true,
	}, 5*time.Minute)

	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{
		QueryID: 103,
		UserID:  12345,
		Data:    EncodeCallbackData("action", "delete", token),
	}

	// First click: should succeed and consume state
	err := router.Dispatch(context.Background(), evt, svc)
	if err != nil {
		t.Fatalf("first click error: %v", err)
	}

	// Second click: state is consumed, should return ErrStateNotFound
	handler.handled = false
	err = router.Dispatch(context.Background(), evt, svc)
	if !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("expected ErrStateNotFound on second click, got %v", err)
	}
	if handler.handled {
		t.Errorf("handler should not run on second click")
	}
}

func TestRouter_Dispatch_ScopeRestrictions(t *testing.T) {
	store := NewStateStore()
	router := NewRouter(zap.NewNop(), store)
	handler := &mockHandler{namespace: "scoped"}
	_ = router.Register(handler)

	// 1. Chat mismatch
	tokenChat := store.StoreWithScope("data", StateScope{
		UserID: 12345,
		ChatID: 9999,
	}, 5*time.Minute)

	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{
		QueryID: 104,
		UserID:  12345,
		ChatID:  8888, // wrong chat
		Data:    EncodeCallbackData("scoped", "act", tokenChat),
	}
	err := router.Dispatch(context.Background(), evt, svc)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized for chat mismatch, got %v", err)
	}

	// 2. MessageID mismatch
	tokenMsg := store.StoreWithScope("data", StateScope{
		UserID:    12345,
		MessageID: 55,
	}, 5*time.Minute)

	evt2 := &core.CallbackQueryEvent{
		QueryID: 105,
		UserID:  12345,
		Target: core.CallbackTarget{
			MessageID: 77, // wrong msg ID
		},
		Data: EncodeCallbackData("scoped", "act", tokenMsg),
	}
	err = router.Dispatch(context.Background(), evt2, svc)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized for message mismatch, got %v", err)
	}

	// 3. Namespace mismatch
	tokenNs := store.StoreWithScope("data", StateScope{
		UserID:    12345,
		Namespace: "expected_ns",
	}, 5*time.Minute)

	evt3 := &core.CallbackQueryEvent{
		QueryID: 106,
		UserID:  12345,
		Data:    EncodeCallbackData("scoped", "act", tokenNs),
	}
	err = router.Dispatch(context.Background(), evt3, svc)
	if !errors.Is(err, ErrInvalidCallbackData) {
		t.Errorf("expected ErrInvalidCallbackData for namespace mismatch, got %v", err)
	}
}

func TestCallbackContext_InlineAndMessageEditDelete(t *testing.T) {
	svc := &recordingService{}

	// 1. Inline edit dispatches to EditInlineBotMessage
	inlineID := &tg.InputBotInlineMessageID64{DCID: 1, ID: 123, AccessHash: 456}
	inlineCtx := &CallbackContext{
		Origin: core.CallbackOriginInline,
		Target: core.CallbackTarget{
			Origin:   core.CallbackOriginInline,
			InlineID: inlineID,
		},
		Service: svc,
	}

	err := inlineCtx.Edit("new inline text", nil)
	if err != nil {
		t.Fatalf("unexpected inline edit error: %v", err)
	}
	if svc.lastEditInlineText != "new inline text" {
		t.Errorf("expected EditInlineBotMessage text 'new inline text', got %q", svc.lastEditInlineText)
	}

	// 2. Inline delete is rejected with ErrUnsupported
	err = inlineCtx.Delete()
	if !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported deleting inline message, got %v", err)
	}

	// 3. Normal message edit dispatches to EditMessageMarkup
	peer := &tg.InputPeerChat{ChatID: 42}
	msgCtx := &CallbackContext{
		Origin: core.CallbackOriginMessage,
		Target: core.CallbackTarget{
			Origin:    core.CallbackOriginMessage,
			Peer:      peer,
			MessageID: 99,
		},
		Service: svc,
	}

	err = msgCtx.Edit("new msg text", nil)
	if err != nil {
		t.Fatalf("unexpected message edit error: %v", err)
	}
	if svc.lastEditText != "new msg text" || svc.lastEditMsgID != 99 {
		t.Errorf("expected EditMessageMarkup, got msgID=%d text=%q", svc.lastEditMsgID, svc.lastEditText)
	}

	// 4. Normal message delete dispatches to DeleteMessage
	err = msgCtx.Delete()
	if err != nil {
		t.Fatalf("unexpected message delete error: %v", err)
	}
	if len(svc.lastDeleteMsgIDs) != 1 || svc.lastDeleteMsgIDs[0] != 99 {
		t.Errorf("expected DeleteMessage with msgID 99, got %+v", svc.lastDeleteMsgIDs)
	}
}

type panickingHandler struct{}

func (p *panickingHandler) Namespace() string { return "panic" }
func (p *panickingHandler) HandleCallback(ctx *CallbackContext) error {
	panic("callback test panic")
}

func TestRouter_HandlerPanicRecovery(t *testing.T) {
	router := NewRouter(zap.NewNop(), nil)
	_ = router.Register(&panickingHandler{})

	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{
		QueryID: 107,
		UserID:  12345,
		Data:    EncodeCallbackData("panic", "fail", "0"),
	}

	err := router.Dispatch(context.Background(), evt, svc)
	if err == nil {
		t.Fatalf("expected error from recovered panic, got nil")
	}
	if !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
}

func TestCallbackContext_AnswerErrorSemantics(t *testing.T) {
	svc := &recordingService{errToAnswer: errors.New("rpc failed")}
	cbCtx := &CallbackContext{
		Ctx:     context.Background(),
		QueryID: 999,
		Service: svc,
	}

	err := cbCtx.Answer("hello", false)
	if err == nil {
		t.Fatalf("expected error from failed answer RPC")
	}
	if cbCtx.IsAnswered() {
		t.Errorf("IsAnswered must remain false when AnswerCallbackQuery returns an error")
	}

	// Now succeed
	svc.errToAnswer = nil
	err = cbCtx.Answer("hello", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cbCtx.IsAnswered() {
		t.Errorf("IsAnswered must be true after successful Answer")
	}
}

func TestCallbackContext_EditMarkupOnlySemantics(t *testing.T) {
	svc := &recordingService{}
	markup := &tg.ReplyKeyboardMarkup{}

	// 1. Normal message: EditMarkup calls EditMessageMarkupOnly (without changing text)
	peer := &tg.InputPeerChat{ChatID: 42}
	msgCtx := &CallbackContext{
		Origin: core.CallbackOriginMessage,
		Target: core.CallbackTarget{
			Origin:    core.CallbackOriginMessage,
			Peer:      peer,
			MessageID: 100,
		},
		Service: svc,
	}
	if err := msgCtx.EditMarkup(markup); err != nil {
		t.Fatalf("EditMarkup failed: %v", err)
	}
	if svc.lastEditMarkupOnlyMsgID != 100 || svc.lastEditMarkupOnlyPeer != peer {
		t.Errorf("expected EditMessageMarkupOnly called for normal message, got msgID=%d", svc.lastEditMarkupOnlyMsgID)
	}

	// 2. Inline message: EditMarkup calls EditInlineBotMessageMarkup
	inlineID := &tg.InputBotInlineMessageID64{DCID: 2, ID: 789, AccessHash: 111}
	inlineCtx := &CallbackContext{
		Origin: core.CallbackOriginInline,
		Target: core.CallbackTarget{
			Origin:   core.CallbackOriginInline,
			InlineID: inlineID,
		},
		Service: svc,
	}
	if err := inlineCtx.EditMarkup(markup); err != nil {
		t.Fatalf("EditMarkup on inline failed: %v", err)
	}
	if svc.lastEditInlineMarkupOnlyID != inlineID {
		t.Errorf("expected EditInlineBotMessageMarkup called for inline message")
	}
}

