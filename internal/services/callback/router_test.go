package callback

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type mockHandler struct {
	namespace string
	handled   bool
	lastCtx   *CallbackContext
	returnErr error
}

func (m *mockHandler) Namespace() string { return m.namespace }
func (m *mockHandler) HandleCallback(ctx *CallbackContext) error {
	m.handled = true
	m.lastCtx = ctx
	return m.returnErr
}

type recordingService struct {
	core.MockTelegramServicer
	lastAnswerQueryID              int64
	lastAnswerText                 string
	lastAnswerAlert                bool
	lastEditInlineID               tg.InputBotInlineMessageIDClass
	lastEditInlineText             string
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

func (r *recordingService) AnswerCallbackQuery(_ context.Context, queryID int64, text string, alert bool) error {
	r.lastAnswerQueryID = queryID
	r.lastAnswerText = text
	r.lastAnswerAlert = alert
	return r.errToAnswer
}

func (r *recordingService) EditInlineBotMessage(_ context.Context, inlineID tg.InputBotInlineMessageIDClass, text string, _ tg.ReplyMarkupClass) error {
	r.lastEditInlineID = inlineID
	r.lastEditInlineText = text
	return nil
}

func (r *recordingService) EditInlineBotMessageMarkup(_ context.Context, inlineID tg.InputBotInlineMessageIDClass, markup tg.ReplyMarkupClass) error {
	r.lastEditInlineMarkupOnlyID = inlineID
	r.lastEditInlineMarkupOnlyMarkup = markup
	return nil
}

func (r *recordingService) EditMessageMarkup(_ context.Context, peer tg.InputPeerClass, msgID int, text string, _ tg.ReplyMarkupClass) error {
	r.lastEditPeer = peer
	r.lastEditMsgID = msgID
	r.lastEditText = text
	return nil
}

func (r *recordingService) EditMessageMarkupOnly(_ context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error {
	r.lastEditMarkupOnlyPeer = peer
	r.lastEditMarkupOnlyMsgID = msgID
	r.lastEditMarkupOnlyMarkup = markup
	return nil
}

func (r *recordingService) DeleteMessage(_ context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	r.lastDeletePeer = peer
	r.lastDeleteMsgIDs = msgIDs
	return nil
}

func dispatchForTest(router *Router, ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
	prepared, err := router.Prepare(ctx, evt, svc, func(string) (tasks.ScopeIdentity, bool) {
		return tasks.ScopeIdentity{Owner: "plugin:test", Generation: 1}, true
	})
	if err != nil {
		return err
	}
	return prepared.Dispatch(ctx, evt, svc)
}

func TestRouterRegisterValidationAndLease(t *testing.T) {
	router := NewRouter(nil)
	if _, err := router.RegisterOwned("", &mockHandler{namespace: "unowned"}); err == nil {
		t.Fatal("expected owner validation error")
	}
	if _, err := router.RegisterOwned("test", nil); err == nil {
		t.Fatal("expected nil handler validation error")
	}

	h := &mockHandler{namespace: "owned"}
	registration, err := router.RegisterOwned("feature", h)
	if err != nil {
		t.Fatalf("RegisterOwned: %v", err)
	}
	if _, err := router.RegisterOwned("feature", h); err == nil {
		t.Fatal("expected duplicate namespace rejection")
	}
	registration.Close()
	registration.Close()
	router.mu.RLock()
	_, exists := router.handlers["owned"]
	router.mu.RUnlock()
	if exists {
		t.Fatal("owned handler remained after lease close")
	}
}

func TestRouterRawNoopStillClearsSpinner(t *testing.T) {
	router := NewRouter(zap.NewNop())
	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{QueryID: 101, UserID: 42, Data: []byte(ActionNoop)}
	if err := dispatchForTest(router, context.Background(), evt, svc); err != nil {
		t.Fatalf("noop dispatch: %v", err)
	}
	if svc.lastAnswerQueryID != 101 || svc.lastAnswerText != "" || svc.lastAnswerAlert {
		t.Fatalf("unexpected noop answer: query=%d text=%q alert=%v", svc.lastAnswerQueryID, svc.lastAnswerText, svc.lastAnswerAlert)
	}
}

func TestRouterRejectsRetiredNamespacePayloadBeforeTaskExecution(t *testing.T) {
	router := NewRouter(zap.NewNop())
	h := &mockHandler{namespace: "legacy"}
	if _, err := router.RegisterOwned("legacy", h); err != nil {
		t.Fatalf("register legacy handler: %v", err)
	}
	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{QueryID: 102, UserID: 42, Data: []byte("retired-namespace-payload")}
	prepared, err := router.Prepare(context.Background(), evt, svc, func(string) (tasks.ScopeIdentity, bool) {
		return tasks.ScopeIdentity{Owner: "plugin:legacy", Generation: 1}, true
	})
	if !errors.Is(err, ErrHandlerNotFound) || prepared != nil {
		t.Fatalf("retired payload result prepared=%v err=%v", prepared, err)
	}
	if h.handled {
		t.Fatal("retired payload reached a registered legacy handler")
	}
	if svc.lastAnswerText != legacyExpiredText || svc.lastAnswerAlert {
		t.Fatalf("retired payload answer text=%q alert=%v", svc.lastAnswerText, svc.lastAnswerAlert)
	}
}

func TestRouterPreparedNoopRejectsPayloadMutation(t *testing.T) {
	router := NewRouter(zap.NewNop())
	svc := &recordingService{}
	evt := &core.CallbackQueryEvent{QueryID: 103, UserID: 42, Data: []byte(ActionNoop)}
	prepared, err := router.Prepare(context.Background(), evt, svc, nil)
	if err != nil {
		t.Fatalf("prepare noop: %v", err)
	}
	evt.Data = []byte("changed")
	if err := prepared.Dispatch(context.Background(), evt, svc); !errors.Is(err, ErrInvalidCallbackData) {
		t.Fatalf("mutated prepared callback err=%v", err)
	}
}

func TestCallbackContextInlineAndMessageEditDelete(t *testing.T) {
	svc := &recordingService{}
	inlineID := &tg.InputBotInlineMessageID64{DCID: 1, ID: 123, AccessHash: 456}
	inlineCtx := &CallbackContext{
		Origin:  core.CallbackOriginInline,
		Target:  core.CallbackTarget{Origin: core.CallbackOriginInline, InlineID: inlineID},
		Service: svc,
	}
	if err := inlineCtx.Edit("new inline text", nil); err != nil {
		t.Fatalf("inline edit: %v", err)
	}
	if svc.lastEditInlineText != "new inline text" {
		t.Fatalf("inline edit text=%q", svc.lastEditInlineText)
	}
	if err := inlineCtx.Delete(); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("inline delete err=%v", err)
	}

	peer := &tg.InputPeerChat{ChatID: 42}
	msgCtx := &CallbackContext{
		Origin:  core.CallbackOriginMessage,
		Target:  core.CallbackTarget{Origin: core.CallbackOriginMessage, Peer: peer, MessageID: 99},
		Service: svc,
	}
	if err := msgCtx.Edit("new msg text", nil); err != nil {
		t.Fatalf("message edit: %v", err)
	}
	if svc.lastEditText != "new msg text" || svc.lastEditMsgID != 99 {
		t.Fatalf("message edit msgID=%d text=%q", svc.lastEditMsgID, svc.lastEditText)
	}
	if err := msgCtx.Delete(); err != nil {
		t.Fatalf("message delete: %v", err)
	}
	if len(svc.lastDeleteMsgIDs) != 1 || svc.lastDeleteMsgIDs[0] != 99 {
		t.Fatalf("deleted ids=%v", svc.lastDeleteMsgIDs)
	}
}

type panickingHandler struct{}

func (*panickingHandler) Namespace() string                     { return "panic" }
func (*panickingHandler) HandleCallback(*CallbackContext) error { panic("callback test panic") }

type contextCaptureHandler struct{ seen context.Context }

func (*contextCaptureHandler) Namespace() string { return "capture" }
func (h *contextCaptureHandler) HandleCallback(ctx *CallbackContext) error {
	h.seen = ctx.Ctx
	return nil
}

func TestLegacyHandlerMiddlewareStillBoundedUntilP1F4(t *testing.T) {
	panicHandler := &panickingHandler{}
	cbCtx := &CallbackContext{Ctx: context.Background()}
	if err := chain(panicHandler, recoverMiddleware(zap.NewNop())).HandleCallback(cbCtx); !errors.Is(err, ErrHandlerPanic) {
		t.Fatalf("panic classification err=%v", err)
	}

	parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	capture := &contextCaptureHandler{}
	cbCtx = &CallbackContext{Ctx: parent}
	if err := chain(capture, timeoutMiddleware(15*time.Second)).HandleCallback(cbCtx); err != nil {
		t.Fatalf("timeout middleware: %v", err)
	}
	if capture.seen != parent {
		t.Fatal("timeout middleware replaced an already tighter upstream deadline")
	}
}

func TestCallbackContextAnswerAndMarkupSemantics(t *testing.T) {
	svc := &recordingService{errToAnswer: errors.New("rpc failed")}
	ctx := &CallbackContext{Ctx: context.Background(), QueryID: 999, Service: svc}
	if err := ctx.Answer("hello", false); err == nil || ctx.IsAnswered() {
		t.Fatalf("failed answer must remain uncommitted: err=%v answered=%v", err, ctx.IsAnswered())
	}
	svc.errToAnswer = nil
	if err := ctx.Answer("hello", false); err != nil || !ctx.IsAnswered() {
		t.Fatalf("successful answer err=%v answered=%v", err, ctx.IsAnswered())
	}

	markup := &tg.ReplyKeyboardMarkup{}
	peer := &tg.InputPeerChat{ChatID: 42}
	msgCtx := &CallbackContext{
		Target:  core.CallbackTarget{Origin: core.CallbackOriginMessage, Peer: peer, MessageID: 100},
		Service: svc,
	}
	if err := msgCtx.EditMarkup(markup); err != nil {
		t.Fatalf("message markup edit: %v", err)
	}
	if svc.lastEditMarkupOnlyMsgID != 100 || svc.lastEditMarkupOnlyPeer != peer {
		t.Fatal("message markup target mismatch")
	}
}

func TestCallbackContextUTF8SafeTruncation(t *testing.T) {
	svc := &recordingService{}
	ctx := &CallbackContext{
		Ctx:     context.Background(),
		QueryID: 1,
		Service: svc,
		Target:  core.CallbackTarget{Peer: &tg.InputPeerSelf{}, MessageID: 7},
	}
	if err := ctx.Answer(strings.Repeat("😀", 80), false); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if !utf8.ValidString(svc.lastAnswerText) || len(svc.lastAnswerText) > 200 {
		t.Fatalf("invalid answer truncation bytes=%d valid=%v", len(svc.lastAnswerText), utf8.ValidString(svc.lastAnswerText))
	}
	if err := ctx.Edit(strings.Repeat("界", 2000), nil); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !utf8.ValidString(svc.lastEditText) || len(svc.lastEditText) > 4096 {
		t.Fatalf("invalid edit truncation bytes=%d valid=%v", len(svc.lastEditText), utf8.ValidString(svc.lastEditText))
	}
}
