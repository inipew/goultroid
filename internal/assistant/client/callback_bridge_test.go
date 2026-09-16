package client_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/client"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

type mockCoreDispatcher struct {
	hasHandlerFunc func(namespace string) bool
	dispatchFunc   func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error
}

func (m *mockCoreDispatcher) HasHandler(namespace string) bool {
	if m.hasHandlerFunc != nil {
		return m.hasHandlerFunc(namespace)
	}
	return false
}

func (m *mockCoreDispatcher) Dispatch(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
	if m.dispatchFunc != nil {
		return m.dispatchFunc(ctx, evt, svc)
	}
	return nil
}

type mockInteraction struct {
	answered bool
	answer   string
	edited   bool
	editText string
}

func (m *mockInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	m.answered = true
	m.answer = text
	return nil
}

func (m *mockInteraction) Edit(ctx context.Context, target interaction.MessageTarget, text string, markup tg.ReplyMarkupClass) error {
	m.edited = true
	m.editText = text
	return nil
}

func (m *mockInteraction) EditMarkup(ctx context.Context, target interaction.MessageTarget, markup tg.ReplyMarkupClass) error {
	return nil
}

func (m *mockInteraction) Delete(ctx context.Context, target interaction.MessageTarget) error {
	return nil
}

func (m *mockInteraction) GetMessage(ctx context.Context, target interaction.MessageTarget) (*tg.Message, error) {
	return &tg.Message{ID: target.MessageID()}, nil
}

func (m *mockInteraction) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	return &tg.Message{ID: 1}, nil
}

func TestAssistantClient_CallbackBridge_DispatchToCoreRouter(t *testing.T) {
	asst := client.NewAssistantClient(1, "hash", "token", zap.NewNop())

	var receivedEvt *core.CallbackQueryEvent
	dispatched := false

	coreRouter := &mockCoreDispatcher{
		hasHandlerFunc: func(namespace string) bool {
			return namespace == "myxl"
		},
		dispatchFunc: func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
			dispatched = true
			receivedEvt = evt
			// Verify servicer methods
			if err := svc.AnswerCallbackQuery(ctx, evt.QueryID, "Kuotamu updated!", false); err != nil {
				return err
			}
			return svc.EditMessageMarkup(ctx, evt.Target.Peer, evt.Target.MessageID, "Quota: 10GB", nil)
		},
	}

	asst.SetCallbackRouter(coreRouter)

	mockInter := &mockInteraction{}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 589287392}, 42, 589287392, 12345)
	payload := callback.ParsedPayload{
		Version:   "v1",
		Namespace: "myxl",
		Action:    "refresh",
		State:     "628123456789",
	}

	tx := callback.NewTransaction(987654321, 589287392, payload, target, mockInter)
	tx.RawData = []byte("v1:myxl:refresh:628123456789")

	cbRouter := asst.CallbackRouter()
	if cbRouter == nil {
		t.Fatalf("expected cbRouter to be initialized")
	}

	ctx := context.Background()
	if err := cbRouter.Dispatch(ctx, tx); err != nil {
		t.Fatalf("unexpected dispatch error: %v", err)
	}

	if !dispatched {
		t.Fatalf("expected coreRouter.Dispatch to have been called")
	}
	if receivedEvt == nil {
		t.Fatalf("receivedEvt is nil")
	}
	if receivedEvt.QueryID != 987654321 {
		t.Errorf("expected QueryID 987654321, got %d", receivedEvt.QueryID)
	}
	if receivedEvt.UserID != 589287392 {
		t.Errorf("expected UserID 589287392, got %d", receivedEvt.UserID)
	}
	if string(receivedEvt.Data) != "v1:myxl:refresh:628123456789" {
		t.Errorf("expected data v1:myxl:refresh:628123456789, got %s", string(receivedEvt.Data))
	}
	if !mockInter.answered || mockInter.answer != "Kuotamu updated!" {
		t.Errorf("expected answered with 'Kuotamu updated!', got %v, text %s", mockInter.answered, mockInter.answer)
	}
	if !mockInter.edited || mockInter.editText != "Quota: 10GB" {
		t.Errorf("expected edited with 'Quota: 10GB', got %v, text %s", mockInter.edited, mockInter.editText)
	}
}

func TestAssistantClient_CallbackBridge_UnhandledNamespaceReturnsError(t *testing.T) {
	asst := client.NewAssistantClient(1, "hash", "token", zap.NewNop())

	coreRouter := &mockCoreDispatcher{
		hasHandlerFunc: func(namespace string) bool {
			return false
		},
	}

	asst.SetCallbackRouter(coreRouter)

	mockInter := &mockInteraction{}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 589287392}, 42, 589287392, 12345)
	payload := callback.ParsedPayload{
		Version:   "v1",
		Namespace: "unknown_plugin",
		Action:    "run",
	}

	tx := callback.NewTransaction(111, 589287392, payload, target, mockInter)

	cbRouter := asst.CallbackRouter()
	ctx := context.Background()
	err := cbRouter.Dispatch(ctx, tx)
	if err == nil || !errors.Is(err, callback.ErrUnknownAction) {
		t.Fatalf("expected ErrUnknownAction, got %v", err)
	}
	if !mockInter.answered {
		t.Fatalf("expected query to be answered to clear spinner")
	}
}

type mockInlineInteraction struct {
	answered bool
	answer   string
	edited   bool
	editText string
}

func (m *mockInlineInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	m.answered = true
	m.answer = text
	return nil
}

func (m *mockInlineInteraction) Edit(ctx context.Context, target interaction.InlineTarget, text string, markup tg.ReplyMarkupClass) error {
	m.edited = true
	m.editText = text
	return nil
}

func TestAssistantClient_CallbackBridge_DispatchInlineToCoreRouter(t *testing.T) {
	asst := client.NewAssistantClient(1, "hash", "token", zap.NewNop())

	var receivedEvt *core.CallbackQueryEvent
	dispatched := false

	coreRouter := &mockCoreDispatcher{
		hasHandlerFunc: func(namespace string) bool {
			return namespace == "help"
		},
		dispatchFunc: func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
			dispatched = true
			receivedEvt = evt
			if err := svc.AnswerCallbackQuery(ctx, evt.QueryID, "Help updated!", false); err != nil {
				return err
			}
			return svc.EditInlineBotMessage(ctx, evt.Target.InlineID, "Help text", nil)
		},
	}

	asst.SetCallbackRouter(coreRouter)

	mockInter := &mockInlineInteraction{}
	inlineMsgID := &tg.InputBotInlineMessageID{DCID: 1, ID: 12345, AccessHash: 67890}
	target := interaction.NewInlineTarget(555, inlineMsgID, 999)
	payload := callback.ParsedPayload{
		Version:   "v1",
		Namespace: "help",
		Action:    "module",
		State:     "myxl",
	}

	tx := callback.NewInlineTransaction(555, 589287392, payload, target, mockInter)
	tx.RawData = []byte("v1:help:module:myxl")

	cbRouter := asst.CallbackRouter()
	ctx := context.Background()
	if err := cbRouter.DispatchInline(ctx, tx); err != nil {
		t.Fatalf("unexpected dispatch inline error: %v", err)
	}

	if !dispatched {
		t.Fatalf("expected coreRouter.Dispatch to have been called")
	}
	if receivedEvt == nil {
		t.Fatalf("receivedEvt is nil")
	}
	if receivedEvt.Origin != core.CallbackOriginInline {
		t.Errorf("expected Origin inline, got %v", receivedEvt.Origin)
	}
	if !mockInter.answered || mockInter.answer != "Help updated!" {
		t.Errorf("expected answered with 'Help updated!', got %v, text %s", mockInter.answered, mockInter.answer)
	}
	if !mockInter.edited || mockInter.editText != "Help text" {
		t.Errorf("expected edited with 'Help text', got %v, text %s", mockInter.edited, mockInter.editText)
	}
}
