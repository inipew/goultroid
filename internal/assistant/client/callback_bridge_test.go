package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type mockCoreDispatcher struct {
	hasHandlerFunc func(namespace string) bool
	taskScopeFunc  func(data []byte, resolve func(string) (tasks.ScopeIdentity, bool)) (tasks.ScopeIdentity, bool)
	dispatchFunc   func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error
}

func (m *mockCoreDispatcher) HasHandler(namespace string) bool {
	if m.hasHandlerFunc != nil {
		return m.hasHandlerFunc(namespace)
	}
	return false
}

func (m *mockCoreDispatcher) TaskScope(data []byte, resolve func(string) (tasks.ScopeIdentity, bool)) (tasks.ScopeIdentity, bool) {
	if m.taskScopeFunc != nil {
		return m.taskScopeFunc(data, resolve)
	}
	return tasks.ScopeIdentity{}, true
}

func (m *mockCoreDispatcher) Dispatch(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
	if m.dispatchFunc != nil {
		return m.dispatchFunc(ctx, evt, svc)
	}
	return nil
}

type mockInteraction struct {
	answered    bool
	answer      string
	alert       bool
	edited      bool
	editText    string
	editMarkup  tg.ReplyMarkupClass
	editTarget  interaction.MessageTarget
	deletedList []interaction.MessageTarget
}

func (m *mockInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	m.answered = true
	m.answer = text
	m.alert = alert
	return nil
}

func (m *mockInteraction) Edit(ctx context.Context, target interaction.MessageTarget, text string, markup tg.ReplyMarkupClass) error {
	m.edited = true
	m.editText = text
	m.editMarkup = markup
	m.editTarget = target
	return nil
}

func (m *mockInteraction) EditMarkup(ctx context.Context, target interaction.MessageTarget, markup tg.ReplyMarkupClass) error {
	m.edited = true
	m.editMarkup = markup
	m.editTarget = target
	return nil
}

func (m *mockInteraction) Delete(ctx context.Context, target interaction.MessageTarget) error {
	m.deletedList = append(m.deletedList, target)
	return nil
}

func (m *mockInteraction) GetMessage(ctx context.Context, target interaction.MessageTarget) (*tg.Message, error) {
	return &tg.Message{ID: target.MessageID()}, nil
}

func (m *mockInteraction) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	return &tg.Message{ID: 1}, nil
}

func (m *mockInteraction) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	return &tg.Message{ID: 1}, nil
}

type mockInlineInteraction struct {
	answered     bool
	answer       string
	edited       bool
	editText     string
	markupEdited bool
	editMarkup   tg.ReplyMarkupClass
}

func (m *mockInlineInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	m.answered = true
	m.answer = text
	return nil
}

func (m *mockInlineInteraction) Edit(ctx context.Context, target interaction.InlineTarget, text string, markup tg.ReplyMarkupClass) error {
	m.edited = true
	m.editText = text
	m.editMarkup = markup
	return nil
}

func (m *mockInlineInteraction) EditMarkup(ctx context.Context, target interaction.InlineTarget, markup tg.ReplyMarkupClass) error {
	m.markupEdited = true
	m.editMarkup = markup
	return nil
}

type testTaskClient struct {
	last tasks.WorkSpec
}

func (c *testTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.last = spec
	if spec.Handler != nil {
		_ = spec.Handler(ctx)
	}
	return nil, nil
}

func (c *testTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (c *testTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (c *testTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type mockTelegramAPI struct {
	answerReq       *tg.MessagesSetBotCallbackAnswerRequest
	editReq         *tg.MessagesEditMessageRequest
	deleteMsgsReq   *tg.MessagesDeleteMessagesRequest
	deleteChanReq   *tg.ChannelsDeleteMessagesRequest
	sendMsgReq      *tg.MessagesSendMessageRequest
	editInlineReq   *tg.MessagesEditInlineBotMessageRequest
	inlineResultReq *tg.MessagesSetInlineBotResultsRequest
}

func (m *mockTelegramAPI) MessagesSetBotCallbackAnswer(ctx context.Context, req *tg.MessagesSetBotCallbackAnswerRequest) (bool, error) {
	m.answerReq = req
	return true, nil
}
func (m *mockTelegramAPI) MessagesEditMessage(ctx context.Context, req *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error) {
	m.editReq = req
	return &tg.Updates{}, nil
}
func (m *mockTelegramAPI) MessagesDeleteMessages(ctx context.Context, req *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	m.deleteMsgsReq = req
	return &tg.MessagesAffectedMessages{Pts: 1, PtsCount: 1}, nil
}
func (m *mockTelegramAPI) ChannelsDeleteMessages(ctx context.Context, req *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	m.deleteChanReq = req
	return &tg.MessagesAffectedMessages{Pts: 1, PtsCount: 1}, nil
}
func (m *mockTelegramAPI) MessagesGetMessages(ctx context.Context, id []tg.InputMessageClass) (tg.MessagesMessagesClass, error) {
	return &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 10}}}, nil
}
func (m *mockTelegramAPI) ChannelsGetMessages(ctx context.Context, req *tg.ChannelsGetMessagesRequest) (tg.MessagesMessagesClass, error) {
	return &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 10}}}, nil
}
func (m *mockTelegramAPI) MessagesSendMessage(ctx context.Context, req *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error) {
	m.sendMsgReq = req
	return &tg.Updates{}, nil
}
func (m *mockTelegramAPI) MessagesEditInlineBotMessage(ctx context.Context, req *tg.MessagesEditInlineBotMessageRequest) (bool, error) {
	m.editInlineReq = req
	return true, nil
}
func (m *mockTelegramAPI) MessagesSetInlineBotResults(ctx context.Context, req *tg.MessagesSetInlineBotResultsRequest) (bool, error) {
	m.inlineResultReq = req
	return true, nil
}

type recordingInlineExecutor struct {
	called   bool
	queryID  int64
	userID   int64
	query    string
	offset   string
	peerType tg.InlineQueryPeerTypeClass
}

func (e *recordingInlineExecutor) ExecuteWithPeerType(ctx context.Context, svc core.TelegramServicer, queryID, userID int64, query, offset string, peerType tg.InlineQueryPeerTypeClass) error {
	e.called, e.queryID, e.userID, e.query, e.offset, e.peerType = true, queryID, userID, query, offset, peerType
	return svc.AnswerInlineQueryOptions(ctx, queryID, []tg.InputBotInlineResultClass{
		&tg.InputBotInlineResult{ID: "result-1", Type: "article", Title: "Result"},
	}, core.InlineAnswerOptions{NextOffset: "next", CacheTime: 7, Private: true})
}

func TestUpdateHandlers_InlineQueryExecutesThroughTaskEngine(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	api := &mockTelegramAPI{}
	executor := &recordingInlineExecutor{}
	taskClient := &testTaskClient{}
	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		InlineEngine:  executor,
		InlineService: newAssistantInlineQueryServicer(api),
		Tasks:         taskClient,
	})

	peerType := &tg.InlineQueryPeerTypePM{}
	update := &tg.UpdateBotInlineQuery{QueryID: 77, UserID: 42, Query: "help ping", Offset: "20", PeerType: peerType}
	if err := dispatcher.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{update}}); err != nil {
		t.Fatalf("handle inline query: %v", err)
	}
	if !executor.called {
		t.Fatal("expected inline engine execution")
	}
	if executor.queryID != 77 || executor.userID != 42 || executor.query != "help ping" || executor.offset != "20" || executor.peerType != peerType {
		t.Fatalf("unexpected inline coordinates: %+v", executor)
	}
	if taskClient.last.ID != "asst:inline:77" || taskClient.last.Pool != "interactive" || taskClient.last.Class != tasks.PriorityInteractive {
		t.Fatalf("unexpected work spec: %+v", taskClient.last)
	}
	if api.inlineResultReq == nil {
		t.Fatal("expected Telegram inline result answer")
	}
	if api.inlineResultReq.QueryID != 77 || api.inlineResultReq.NextOffset != "next" || api.inlineResultReq.CacheTime != 7 || !api.inlineResultReq.Private || len(api.inlineResultReq.Results) != 1 {
		t.Fatalf("unexpected inline answer: %+v", api.inlineResultReq)
	}
}

func TestUpdateHandlers_InlineQueryWithoutTaskEngineAnswersEmpty(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	api := &mockTelegramAPI{}
	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		InlineEngine:  &recordingInlineExecutor{},
		InlineService: newAssistantInlineQueryServicer(api),
	})

	update := &tg.UpdateBotInlineQuery{QueryID: 88, UserID: 42, Query: "help"}
	if err := dispatcher.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{update}}); err != nil {
		t.Fatalf("handle inline query: %v", err)
	}
	if api.inlineResultReq == nil || api.inlineResultReq.QueryID != 88 {
		t.Fatalf("expected terminal empty answer, got %+v", api.inlineResultReq)
	}
	if len(api.inlineResultReq.Results) != 0 || api.inlineResultReq.CacheTime != 1 || !api.inlineResultReq.Private {
		t.Fatalf("unexpected unavailable answer: %+v", api.inlineResultReq)
	}
}

func TestAssistantClient_CallbackBridge_DispatchToCoreRouter(t *testing.T) {
	asst := NewAssistantClient(1, "hash", "token", zap.NewNop())
	asst.SetTasks(&testTaskClient{})

	var receivedEvt *core.CallbackQueryEvent
	dispatched := false

	coreRouter := &mockCoreDispatcher{
		hasHandlerFunc: func(namespace string) bool {
			return namespace == "myxl"
		},
		dispatchFunc: func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
			dispatched = true
			receivedEvt = evt
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

func TestAssistantClient_CallbackBridge_TaskEngineSubmission(t *testing.T) {
	asst := NewAssistantClient(1, "hash", "token", zap.NewNop())
	taskClient := &testTaskClient{}
	asst.SetTasks(taskClient)

	expectedScope := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 3}
	asst.SetPluginScopeResolver(func(owner string) (tasks.ScopeIdentity, bool) {
		if owner == "myxl" {
			return expectedScope, true
		}
		return tasks.ScopeIdentity{}, false
	})

	dispatched := false
	coreRouter := &mockCoreDispatcher{
		hasHandlerFunc: func(namespace string) bool { return namespace == "myxl" },
		taskScopeFunc: func(data []byte, resolve func(string) (tasks.ScopeIdentity, bool)) (tasks.ScopeIdentity, bool) {
			return resolve("myxl")
		},
		dispatchFunc: func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
			dispatched = true
			return svc.AnswerCallbackQuery(ctx, evt.QueryID, "TaskEngine Ran!", false)
		},
	}
	asst.SetCallbackRouter(coreRouter)

	mockInter := &mockInteraction{}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 589287392}, 100, 589287392, 12345)
	payload := callback.ParsedPayload{
		Version:   "v1",
		Namespace: "myxl",
		Action:    "refresh",
		State:     "123",
	}
	tx := callback.NewTransaction(777, 589287392, payload, target, mockInter)
	tx.RawData = []byte("v1:myxl:refresh:123")

	ctx := context.Background()
	if err := asst.CallbackRouter().Dispatch(ctx, tx); err != nil {
		t.Fatalf("unexpected dispatch error: %v", err)
	}

	if !dispatched {
		t.Fatalf("expected dispatch through TaskEngine")
	}
	if taskClient.last.Scope != expectedScope {
		t.Errorf("expected Scope %+v, got %+v", expectedScope, taskClient.last.Scope)
	}
	if taskClient.last.QuotaOwner != tasks.OwnerID("telegram:user:589287392") {
		t.Errorf("expected QuotaOwner telegram:user:589287392, got %s", taskClient.last.QuotaOwner)
	}
	if !strings.HasPrefix(string(taskClient.last.ID), "asst:cb:777") {
		t.Errorf("expected task ID asst:cb:777, got %s", taskClient.last.ID)
	}
	if taskClient.last.OrderingKey != "callback:msg:589287392:100" {
		t.Errorf("expected ordering key callback:msg:589287392:100, got %s", taskClient.last.OrderingKey)
	}
}

func TestAssistantClient_CallbackBridge_TaskScopeUnavailable(t *testing.T) {
	asst := NewAssistantClient(1, "hash", "token", zap.NewNop())

	coreRouter := &mockCoreDispatcher{
		hasHandlerFunc: func(namespace string) bool { return namespace == "disabled_feature" },
		taskScopeFunc: func(data []byte, resolve func(string) (tasks.ScopeIdentity, bool)) (tasks.ScopeIdentity, bool) {
			return tasks.ScopeIdentity{}, false // disabled/unavailable
		},
	}
	asst.SetCallbackRouter(coreRouter)

	mockInter := &mockInteraction{}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 589287392}, 1, 589287392, 1)
	payload := callback.ParsedPayload{
		Version:   "v1",
		Namespace: "disabled_feature",
		Action:    "run",
	}
	tx := callback.NewTransaction(111, 589287392, payload, target, mockInter)
	tx.RawData = []byte("v1:disabled_feature:run:0")

	ctx := context.Background()
	err := asst.CallbackRouter().Dispatch(ctx, tx)
	if err == nil {
		t.Fatalf("expected error for unavailable feature")
	}
	if !mockInter.answered || mockInter.answer != "Feature not available." {
		t.Errorf("expected 'Feature not available.' answer, got answered=%v, answer=%s", mockInter.answered, mockInter.answer)
	}
}

func TestAssistantClient_CallbackBridge_RequiresTaskEngine(t *testing.T) {
	asst := NewAssistantClient(1, "hash", "token", zap.NewNop())
	coreRouter := &mockCoreDispatcher{
		hasHandlerFunc: func(string) bool { return true },
		dispatchFunc: func(context.Context, *core.CallbackQueryEvent, core.TelegramServicer) error {
			t.Fatal("core callback must not execute without TaskEngine")
			return nil
		},
	}
	asst.SetCallbackRouter(coreRouter)
	inter := &mockInteraction{}
	tx := callback.NewTransaction(112, 42, callback.ParsedPayload{Version: "v1", Namespace: "myxl", Action: "refresh", State: "1"}, interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 42}, 1, 42, 1), inter)
	tx.RawData = []byte("v1:myxl:refresh:1")
	err := asst.CallbackRouter().Dispatch(context.Background(), tx)
	if !errors.Is(err, ErrCallbackTasksNotConfigured) {
		t.Fatalf("expected ErrCallbackTasksNotConfigured, got %v", err)
	}
	if !inter.answered || inter.answer != "Interaction service unavailable." {
		t.Fatalf("expected unavailable acknowledgement, got answered=%v text=%q", inter.answered, inter.answer)
	}
}

func TestAssistantClient_CallbackBridge_UnhandledNamespaceReturnsError(t *testing.T) {
	asst := NewAssistantClient(1, "hash", "token", zap.NewNop())

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

func TestAssistantClient_CallbackBridge_DispatchInlineToCoreRouter(t *testing.T) {
	asst := NewAssistantClient(1, "hash", "token", zap.NewNop())
	asst.SetTasks(&testTaskClient{})

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

func TestAssistantClient_CallbackBridge_EditInlineBotMessageMarkup_PreservesText(t *testing.T) {
	asst := NewAssistantClient(1, "hash", "token", zap.NewNop())
	asst.SetTasks(&testTaskClient{})

	coreRouter := &mockCoreDispatcher{
		hasHandlerFunc: func(namespace string) bool { return namespace == "help" },
		dispatchFunc: func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
			markup := &tg.ReplyInlineMarkup{}
			return svc.EditInlineBotMessageMarkup(ctx, evt.Target.InlineID, markup)
		},
	}
	asst.SetCallbackRouter(coreRouter)

	mockInter := &mockInlineInteraction{}
	inlineMsgID := &tg.InputBotInlineMessageID{DCID: 1, ID: 12345, AccessHash: 67890}
	target := interaction.NewInlineTarget(888, inlineMsgID, 999)
	payload := callback.ParsedPayload{
		Version:   "v1",
		Namespace: "help",
		Action:    "close",
	}

	tx := callback.NewInlineTransaction(888, 589287392, payload, target, mockInter)
	tx.RawData = []byte("v1:help:close")

	ctx := context.Background()
	if err := asst.CallbackRouter().DispatchInline(ctx, tx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockInter.markupEdited {
		t.Errorf("expected EditMarkup to be called without touching text")
	}
	if mockInter.edited {
		t.Errorf("expected Edit (which changes text) NOT to be called")
	}
}

func TestAssistantClient_CallbackBridge_PartialTargetMerging(t *testing.T) {
	asst := NewAssistantClient(1, "hash", "token", zap.NewNop())
	asst.SetTasks(&testTaskClient{})

	originalPeer := &tg.InputPeerUser{UserID: 12345, AccessHash: 9999}
	originalMsgID := 10

	var capturedTarget interaction.MessageTarget
	coreRouter := &mockCoreDispatcher{
		hasHandlerFunc: func(namespace string) bool { return namespace == "test" },
		dispatchFunc: func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
			// Case 1: peer is nil, msgID is 20 -> originalPeer must be preserved
			if err := svc.EditMessageMarkup(ctx, nil, 20, "new text", nil); err != nil {
				return err
			}
			return nil
		},
	}
	asst.SetCallbackRouter(coreRouter)

	mockInter := &mockInteraction{}
	target := interaction.NewMessageTarget(originalPeer, originalMsgID, 12345, 1)
	payload := callback.ParsedPayload{Version: "v1", Namespace: "test", Action: "act"}
	tx := callback.NewTransaction(1, 12345, payload, target, mockInter)

	ctx := context.Background()
	if err := asst.CallbackRouter().Dispatch(ctx, tx); err != nil {
		t.Fatalf("unexpected dispatch error: %v", err)
	}

	capturedTarget = mockInter.editTarget
	if capturedTarget.Peer() != originalPeer {
		t.Errorf("expected originalPeer preserved when peer=nil, got %v", capturedTarget.Peer())
	}
	if capturedTarget.MessageID() != 20 {
		t.Errorf("expected msgID=20, got %d", capturedTarget.MessageID())
	}

	// Case 2: peer is newPeer, msgID is 0 -> originalMsgID must be preserved
	newPeer := &tg.InputPeerChannel{ChannelID: 777, AccessHash: 888}
	coreRouter.dispatchFunc = func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
		return svc.EditMessageMarkupOnly(ctx, newPeer, 0, nil)
	}
	tx2 := callback.NewTransaction(2, 12345, payload, target, mockInter)
	if err := asst.CallbackRouter().Dispatch(ctx, tx2); err != nil {
		t.Fatalf("unexpected dispatch error: %v", err)
	}
	capturedTarget = mockInter.editTarget
	if capturedTarget.Peer() != newPeer {
		t.Errorf("expected newPeer, got %v", capturedTarget.Peer())
	}
	if capturedTarget.MessageID() != originalMsgID {
		t.Errorf("expected originalMsgID preserved when msgID=0, got %d", capturedTarget.MessageID())
	}
}

func TestAssistantClient_CallbackBridge_DeleteMessage_MultipleIDs(t *testing.T) {
	asst := NewAssistantClient(1, "hash", "token", zap.NewNop())
	asst.SetTasks(&testTaskClient{})

	originalPeer := &tg.InputPeerUser{UserID: 12345, AccessHash: 9999}
	mockInter := &mockInteraction{}
	target := interaction.NewMessageTarget(originalPeer, 10, 12345, 1)

	coreRouter := &mockCoreDispatcher{
		hasHandlerFunc: func(namespace string) bool { return namespace == "del" },
		dispatchFunc: func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
			return svc.DeleteMessage(ctx, nil, []int{101, 102, 103})
		},
	}
	asst.SetCallbackRouter(coreRouter)

	payload := callback.ParsedPayload{Version: "v1", Namespace: "del", Action: "multi"}
	tx := callback.NewTransaction(1, 12345, payload, target, mockInter)

	ctx := context.Background()
	if err := asst.CallbackRouter().Dispatch(ctx, tx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mockInter.deletedList) != 3 {
		t.Fatalf("expected 3 deleted messages, got %d", len(mockInter.deletedList))
	}
	if mockInter.deletedList[0].MessageID() != 101 || mockInter.deletedList[1].MessageID() != 102 || mockInter.deletedList[2].MessageID() != 103 {
		t.Errorf("unexpected deleted IDs: %+v", mockInter.deletedList)
	}
}

func TestAssistantClient_CallbackBridge_ExplicitErrors(t *testing.T) {
	// When tx is nil, servicer methods return core.ErrInternal
	svc := &assistantCallbackServicer{tx: nil}
	ctx := context.Background()

	if err := svc.AnswerCallbackQuery(ctx, 1, "test", false); !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
	if err := svc.EditMessageMarkup(ctx, nil, 1, "test", nil); !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
	if err := svc.EditMessageMarkupOnly(ctx, nil, 1, nil); !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
	if err := svc.DeleteMessage(ctx, nil, []int{1}); !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
	if _, err := svc.GetMessage(ctx, nil, 1); !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
	if _, err := svc.SendMessage(ctx, nil, "test"); !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
	if _, err := svc.SendMedia(ctx, nil, "photo", "a.png", "cap"); !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}

	inlineSvc := &assistantInlineCallbackServicer{tx: nil}
	if err := inlineSvc.AnswerCallbackQuery(ctx, 1, "test", false); !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
	if err := inlineSvc.EditInlineBotMessage(ctx, nil, "test", nil); !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
	if err := inlineSvc.EditInlineBotMessageMarkup(ctx, nil, nil); !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
}

func TestAssistantClient_Updates_SpinnerProtection(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	api := &mockTelegramAPI{}
	logger := zap.NewNop()
	clientInter := interaction.NewClientInteraction(api, logger)

	isShutdown := false
	cbRouter := callback.NewRouter(logger)

	deps := UpdateHandlerDeps{
		Logger:         logger,
		IsShuttingDown: func() bool { return isShutdown },
		Interaction:    clientInter,
		CallbackRouter: cbRouter,
	}

	RegisterUpdateHandlers(&dispatcher, deps)
	ctx := context.Background()

	// 1. Malformed payload answers with "Invalid callback"
	err := dispatcher.Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateBotCallbackQuery{
				QueryID: 101,
				UserID:  1,
				Data:    []byte("malformed_payload"),
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if api.answerReq == nil || api.answerReq.QueryID != 101 || api.answerReq.Message != "Invalid callback" {
		t.Errorf("expected Invalid callback answer, got %+v", api.answerReq)
	}

	// 2. Shutting down answers with retry alert
	isShutdown = true
	api.answerReq = nil
	err = dispatcher.Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateBotCallbackQuery{
				QueryID: 102,
				UserID:  1,
				Data:    []byte("v1:test:act:noop"),
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if api.answerReq == nil || api.answerReq.QueryID != 102 || !strings.Contains(api.answerReq.Message, "shutting down") {
		t.Errorf("expected shutdown answer, got %+v", api.answerReq)
	}

	// 3. Inline malformed payload answers
	isShutdown = false
	api.answerReq = nil
	err = dispatcher.Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateInlineBotCallbackQuery{
				QueryID: 201,
				UserID:  1,
				Data:    []byte("malformed_inline"),
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if api.answerReq == nil || api.answerReq.QueryID != 201 || api.answerReq.Message != "Invalid callback" {
		t.Errorf("expected Invalid callback inline answer, got %+v", api.answerReq)
	}
}

func TestAssistantClient_CallbackBridge_UnsupportedMethodsFailClosed(t *testing.T) {
	msgSvc := &assistantCallbackServicer{}
	inlineSvc := &assistantInlineCallbackServicer{}
	ctx := context.Background()

	// In message callback servicer: inline methods must return ErrUnsupported
	if err := msgSvc.EditInlineBotMessage(ctx, nil, "text", nil); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported for EditInlineBotMessage, got %v", err)
	}
	if err := msgSvc.EditInlineBotMessageMarkup(ctx, nil, nil); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported for EditInlineBotMessageMarkup, got %v", err)
	}
	if err := msgSvc.PinMessage(ctx, nil, 1, false); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported for PinMessage, got %v", err)
	}
	if err := msgSvc.React(ctx, nil, 1, "👍"); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported for React, got %v", err)
	}

	// In inline callback servicer: delete message and normal message edits must return ErrUnsupported
	if err := inlineSvc.DeleteMessage(ctx, nil, []int{1}); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported for inline DeleteMessage, got %v", err)
	}
	if err := inlineSvc.EditMessage(ctx, nil, 1, "text"); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported for inline EditMessage, got %v", err)
	}
	if _, err := inlineSvc.SendMessage(ctx, nil, "text"); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported for inline SendMessage, got %v", err)
	}
}

type testCancelledTicket struct {
	done chan struct{}
}

func (t *testCancelledTicket) TaskID() tasks.TaskID   { return "asst:cb:888" }
func (t *testCancelledTicket) State() tasks.TaskState { return tasks.StateCancelled }
func (t *testCancelledTicket) Done() <-chan struct{}  { return t.done }
func (t *testCancelledTicket) Result() (tasks.TaskResult, bool) {
	return tasks.TaskResult{
		TaskID:  "asst:cb:888",
		Outcome: tasks.OutcomeCancelled,
		Cause:   tasks.CauseUserCancel,
		Failure: tasks.FailureInfo{Message: "scope cancelled"},
	}, true
}
func (t *testCancelledTicket) Wait(ctx context.Context) (tasks.TaskResult, error) {
	res, _ := t.Result()
	return res, nil
}

type testCancelledTaskClient struct {
	ticket tasks.Ticket
}

func (c *testCancelledTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	// TaskEngine cancelled in queue: spec.Handler is NEVER executed
	return c.ticket, nil
}
func (c *testCancelledTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (c *testCancelledTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (c *testCancelledTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestAssistantClient_CallbackBridge_TaskEngineCancellationUnblocks(t *testing.T) {
	asst := NewAssistantClient(1, "hash", "token", zap.NewNop())
	cancelledTicket := &testCancelledTicket{done: make(chan struct{})}
	close(cancelledTicket.done) // Already done/cancelled
	asst.SetTasks(&testCancelledTaskClient{ticket: cancelledTicket})

	asst.SetPluginScopeResolver(func(owner string) (tasks.ScopeIdentity, bool) {
		return tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 1}, true
	})

	coreRouter := &mockCoreDispatcher{
		hasHandlerFunc: func(namespace string) bool { return true },
		taskScopeFunc: func(data []byte, resolve func(string) (tasks.ScopeIdentity, bool)) (tasks.ScopeIdentity, bool) {
			return resolve("myxl")
		},
		dispatchFunc: func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
			t.Fatalf("dispatch should not be called when task is cancelled in queue")
			return nil
		},
	}
	asst.SetCallbackRouter(coreRouter)

	mockInter := &mockInteraction{}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 589287392}, 100, 589287392, 12345)
	payload := callback.ParsedPayload{
		Version:   "v1",
		Namespace: "myxl",
		Action:    "refresh",
		State:     "123",
	}
	tx := callback.NewTransaction(888, 589287392, payload, target, mockInter)
	tx.RawData = []byte("v1:myxl:refresh:123")

	cbRouter := asst.CallbackRouter()
	ctx := context.Background()

	// Should unblock promptly and return an error without deadlocking
	err := cbRouter.Dispatch(ctx, tx)
	if err == nil {
		t.Fatalf("expected error from cancelled task, got nil")
	}
	if !strings.Contains(err.Error(), "scope cancelled") {
		t.Errorf("expected 'scope cancelled' error, got %v", err)
	}
}

func TestAssistantClient_Updates_SpinnerProtection_OnErrorAndPanic(t *testing.T) {
	api := &mockTelegramAPI{}
	clientInter := interaction.NewClientInteraction(api, zap.NewNop())
	cbRouter := callback.NewRouter(zap.NewNop())

	// Register a handler that fails without answering
	cbRouter.Register("test", "fail", func(ctx context.Context, tx *callback.Transaction) error {
		return errors.New("business error before answering")
	})

	// Register a handler that panics without answering
	cbRouter.Register("test", "panic", func(ctx context.Context, tx *callback.Transaction) error {
		panic("boom")
	})

	dispatcher := tg.NewUpdateDispatcher()
	deps := UpdateHandlerDeps{
		Logger:         zap.NewNop(),
		Interaction:    clientInter,
		CallbackRouter: cbRouter,
	}
	RegisterUpdateHandlers(&dispatcher, deps)
	ctx := context.Background()

	// 1. Handler error without answering stops spinner
	api.answerReq = nil
	_ = dispatcher.Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateBotCallbackQuery{
				QueryID: 301,
				UserID:  1,
				Data:    []byte("v1:test:fail:noop"),
			},
		},
	})
	if api.answerReq == nil || api.answerReq.QueryID != 301 {
		t.Errorf("expected query 301 answered to dismiss spinner, got %+v", api.answerReq)
	}

	// 2. Handler panic without answering stops spinner
	api.answerReq = nil
	_ = dispatcher.Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateBotCallbackQuery{
				QueryID: 302,
				UserID:  1,
				Data:    []byte("v1:test:panic:noop"),
			},
		},
	})
	if api.answerReq == nil || api.answerReq.QueryID != 302 {
		t.Errorf("expected query 302 answered to dismiss spinner, got %+v", api.answerReq)
	}
}
