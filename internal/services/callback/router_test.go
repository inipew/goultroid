package callback

import (
	"context"
	"errors"
	"testing"
	"time"

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
	lastAnswerQueryID int64
	lastAnswerText    string
	lastAnswerAlert   bool
}

func (r *recordingService) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	r.lastAnswerQueryID = queryID
	r.lastAnswerText = text
	r.lastAnswerAlert = alert
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
