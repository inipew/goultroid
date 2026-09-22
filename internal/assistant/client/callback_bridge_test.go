package client

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	corecallback "github.com/inipew/goultroid/internal/services/callback"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type mockCoreDispatcher struct {
	hasHandlerFunc func(namespace string) bool
	taskScopeFunc  func(data []byte, resolve func(string) (tasks.ScopeIdentity, bool)) (tasks.ScopeIdentity, bool)
	dispatchFunc   func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error
}

type mockPreparedCallback struct {
	scope    tasks.ScopeIdentity
	dispatch func(context.Context, *core.CallbackQueryEvent, core.TelegramServicer) error
}

func (p *mockPreparedCallback) Scope() tasks.ScopeIdentity {
	if p == nil {
		return tasks.ScopeIdentity{}
	}
	return p.scope
}

func (p *mockPreparedCallback) Dispatch(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
	if p == nil || p.dispatch == nil {
		return nil
	}
	return p.dispatch(ctx, evt, svc)
}

func (m *mockCoreDispatcher) Prepare(
	ctx context.Context,
	evt *core.CallbackQueryEvent,
	svc core.TelegramServicer,
	resolve func(string) (tasks.ScopeIdentity, bool),
) (corecallback.PreparedCallback, error) {
	if evt == nil {
		return nil, corecallback.ErrInvalidCallbackData
	}
	ns, _, _, err := corecallback.ParseCallbackData(evt.Data)
	if err != nil {
		if svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Invalid callback", false)
		}
		return nil, err
	}
	if m.hasHandlerFunc != nil && !m.hasHandlerFunc(ns) {
		if svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Feature not available.", false)
		}
		return nil, corecallback.ErrHandlerNotFound
	}
	var scope tasks.ScopeIdentity
	if m.taskScopeFunc != nil {
		var ok bool
		scope, ok = m.taskScopeFunc(evt.Data, resolve)
		if !ok {
			if svc != nil {
				_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Feature not available.", false)
			}
			return nil, corecallback.ErrHandlerRegistrationChanged
		}
	}
	return &mockPreparedCallback{scope: scope, dispatch: m.dispatchFunc}, nil
}

type mockInteraction struct {
	answered    bool
	answer      string
	alert       bool
	answerCalls int
	edited      bool
	editText    string
	editMarkup  tg.ReplyMarkupClass
	editTarget  interaction.MessageTarget
	deletedList []interaction.MessageTarget
}

func (m *mockInteraction) Answer(context.Context, int64, string, bool) error {
	m.answerCalls++
	return nil
}

type recordingInteraction struct{ *mockInteraction }

func (m *recordingInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	m.answered = true
	m.answer = text
	m.alert = alert
	m.answerCalls++
	return nil
}
func (m *recordingInteraction) Edit(ctx context.Context, target interaction.MessageTarget, text string, markup tg.ReplyMarkupClass) error {
	m.edited, m.editText, m.editMarkup, m.editTarget = true, text, markup, target
	return nil
}
func (m *recordingInteraction) EditMarkup(ctx context.Context, target interaction.MessageTarget, markup tg.ReplyMarkupClass) error {
	m.edited, m.editMarkup, m.editTarget = true, markup, target
	return nil
}
func (m *recordingInteraction) Delete(ctx context.Context, target interaction.MessageTarget) error {
	m.deletedList = append(m.deletedList, target)
	return nil
}
func (m *recordingInteraction) GetMessage(ctx context.Context, target interaction.MessageTarget) (*tg.Message, error) {
	return &tg.Message{ID: target.MessageID()}, nil
}
func (m *recordingInteraction) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	return &tg.Message{ID: 1}, nil
}
func (m *recordingInteraction) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType, filePath, caption string) (*tg.Message, error) {
	return &tg.Message{ID: 1}, nil
}

type mockInlineInteraction struct {
	answered     bool
	answer       string
	answerCalls  int
	edited       bool
	editText     string
	markupEdited bool
	editMarkup   tg.ReplyMarkupClass
}

func (m *mockInlineInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	m.answered, m.answer = true, text
	m.answerCalls++
	return nil
}
func (m *mockInlineInteraction) Edit(ctx context.Context, target interaction.InlineTarget, text string, markup tg.ReplyMarkupClass) error {
	m.edited, m.editText, m.editMarkup = true, text, markup
	return nil
}
func (m *mockInlineInteraction) EditMarkup(ctx context.Context, target interaction.InlineTarget, markup tg.ReplyMarkupClass) error {
	m.markupEdited, m.editMarkup = true, markup
	return nil
}

type testTaskClient struct{ last tasks.WorkSpec }

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
func (m *mockTelegramAPI) MessagesSendMedia(context.Context, *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
	return &tg.UpdateShortSentMessage{ID: 11}, nil
}
func (m *mockTelegramAPI) MessagesForwardMessages(context.Context, *tg.MessagesForwardMessagesRequest) (tg.UpdatesClass, error) {
	return &tg.UpdateShortSentMessage{ID: 12}, nil
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

func (e *recordingInlineExecutor) Prepare(string) (inlineservice.PreparedQuery, error) {
	return inlineservice.PreparedQuery{}, inlineservice.ErrNoMatchingHandler
}

func (e *recordingInlineExecutor) ExecutePreparedWithPeerType(
	ctx context.Context,
	svc core.TelegramServicer,
	queryID, userID int64,
	prepared inlineservice.PreparedQuery,
	offset string,
	peerType tg.InlineQueryPeerTypeClass,
) error {
	return e.ExecuteWithPeerType(ctx, svc, queryID, userID, prepared.Query(), offset, peerType)
}

func messageEvent(queryID, userID int64, data []byte, target interaction.MessageTarget) *core.CallbackQueryEvent {
	return &core.CallbackQueryEvent{
		At:      time.Now(),
		QueryID: queryID,
		UserID:  userID,
		ChatID:  target.ChatID(),
		MsgID:   target.MessageID(),
		Data:    data,
		Origin:  core.CallbackOriginMessage,
		Target: core.CallbackTarget{
			Origin:       core.CallbackOriginMessage,
			Peer:         target.Peer(),
			MessageID:    target.MessageID(),
			ChatInstance: target.ChatInstance(),
		},
		ChatInstance: target.ChatInstance(),
	}
}

func inlineEvent(queryID, userID int64, data []byte, target interaction.InlineTarget) *core.CallbackQueryEvent {
	return &core.CallbackQueryEvent{
		At:      time.Now(),
		QueryID: queryID,
		UserID:  userID,
		Data:    data,
		Origin:  core.CallbackOriginInline,
		Target: core.CallbackTarget{
			Origin:       core.CallbackOriginInline,
			InlineID:     target.MessageID(),
			ChatInstance: target.ChatInstance(),
		},
		ChatInstance: target.ChatInstance(),
	}
}

func TestUpdateHandlers_InlineQueryExecutesThroughTaskEngine(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	api := &mockTelegramAPI{}
	executor := &recordingInlineExecutor{}
	taskClient := &testTaskClient{}
	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		InlineEngine: executor, InlineService: newAssistantInlineQueryServicer(api), Tasks: taskClient,
	})

	peerType := &tg.InlineQueryPeerTypePM{}
	update := &tg.UpdateBotInlineQuery{QueryID: 77, UserID: 42, Query: "help ping", Offset: "20", PeerType: peerType}
	if err := dispatcher.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{update}}); err != nil {
		t.Fatalf("handle inline query: %v", err)
	}
	if !executor.called || taskClient.last.ID != "asst:inline:77" || api.inlineResultReq == nil {
		t.Fatalf("inline execution not routed through TaskEngine: executor=%+v spec=%+v answer=%+v", executor, taskClient.last, api.inlineResultReq)
	}
}

func TestUpdateHandlers_InlineQueryWithoutTaskEngineAnswersEmpty(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	api := &mockTelegramAPI{}
	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		InlineEngine: &recordingInlineExecutor{}, InlineService: newAssistantInlineQueryServicer(api),
	})
	update := &tg.UpdateBotInlineQuery{QueryID: 88, UserID: 42, Query: "help"}
	if err := dispatcher.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{update}}); err != nil {
		t.Fatalf("handle inline query: %v", err)
	}
	if api.inlineResultReq == nil || api.inlineResultReq.QueryID != 88 || len(api.inlineResultReq.Results) != 0 {
		t.Fatalf("expected terminal empty answer, got %+v", api.inlineResultReq)
	}
}

func TestCallbackIngress_DispatchMessageDirectlyToCore(t *testing.T) {
	taskClient := &testTaskClient{}
	inter := &recordingInteraction{mockInteraction: &mockInteraction{}}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 589287392}, 42, 589287392, 12345)
	evt := messageEvent(987654321, 589287392, []byte("v1:myxl:refresh:628123456789"), target)
	var received *core.CallbackQueryEvent

	router := &mockCoreDispatcher{
		hasHandlerFunc: func(ns string) bool { return ns == "myxl" },
		dispatchFunc: func(ctx context.Context, got *core.CallbackQueryEvent, svc core.TelegramServicer) error {
			received = got
			if err := svc.AnswerCallbackQuery(ctx, got.QueryID, "Kuotamu updated!", false); err != nil {
				return err
			}
			return svc.EditMessageMarkup(ctx, got.Target.Peer, got.Target.MessageID, "Quota: 10GB", nil)
		},
	}
	svc := newAssistantCallbackServicer(evt.QueryID, target, inter)
	if err := dispatchCoreCallback(context.Background(), router, taskClient, nil, newCallbackQueryDeduper(), evt, svc, zap.NewNop()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if received != evt || !inter.answered || inter.answer != "Kuotamu updated!" || !inter.edited || inter.editText != "Quota: 10GB" {
		t.Fatalf("bridge mismatch: evt=%+v inter=%+v", received, inter)
	}
}

func TestCallbackIngress_TaskEngineScopeAndOrdering(t *testing.T) {
	taskClient := &testTaskClient{}
	expectedScope := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 3}
	resolver := func(owner string) (tasks.ScopeIdentity, bool) {
		if owner == "myxl" {
			return expectedScope, true
		}
		return tasks.ScopeIdentity{}, false
	}
	router := &mockCoreDispatcher{
		hasHandlerFunc: func(string) bool { return true },
		taskScopeFunc: func(data []byte, resolve func(string) (tasks.ScopeIdentity, bool)) (tasks.ScopeIdentity, bool) {
			return resolve("myxl")
		},
	}
	inter := &recordingInteraction{mockInteraction: &mockInteraction{}}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 7}, 100, 7, 9)
	evt := messageEvent(777, 7, []byte("v1:myxl:refresh:123"), target)
	if err := dispatchCoreCallback(context.Background(), router, taskClient, resolver, newCallbackQueryDeduper(), evt, newAssistantCallbackServicer(evt.QueryID, target, inter), zap.NewNop()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if taskClient.last.Scope != expectedScope || taskClient.last.QuotaOwner != "telegram:user:7" || taskClient.last.OrderingKey != "callback:msg:7:100" || taskClient.last.ID != "asst:cb:777" {
		t.Fatalf("unexpected work spec: %+v", taskClient.last)
	}
}

func TestCallbackIngress_RejectsUnavailableAndMissingTaskEngine(t *testing.T) {
	inter := &recordingInteraction{mockInteraction: &mockInteraction{}}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 7}, 1, 7, 1)

	unavailable := &mockCoreDispatcher{
		hasHandlerFunc: func(string) bool { return true },
		taskScopeFunc: func([]byte, func(string) (tasks.ScopeIdentity, bool)) (tasks.ScopeIdentity, bool) {
			return tasks.ScopeIdentity{}, false
		},
	}
	evt := messageEvent(111, 7, []byte("v1:myxl:run:noop"), target)
	err := dispatchCoreCallback(context.Background(), unavailable, &testTaskClient{}, nil, newCallbackQueryDeduper(), evt, newAssistantCallbackServicer(evt.QueryID, target, inter), zap.NewNop())
	if err == nil || inter.answer != "Feature not available." {
		t.Fatalf("expected unavailable feature rejection, err=%v answer=%q", err, inter.answer)
	}

	inter2 := &recordingInteraction{mockInteraction: &mockInteraction{}}
	available := &mockCoreDispatcher{hasHandlerFunc: func(string) bool { return true }}
	evt2 := messageEvent(112, 7, []byte("v1:myxl:run:noop"), target)
	err = dispatchCoreCallback(context.Background(), available, nil, nil, newCallbackQueryDeduper(), evt2, newAssistantCallbackServicer(evt2.QueryID, target, inter2), zap.NewNop())
	if !errors.Is(err, ErrCallbackTasksNotConfigured) || inter2.answer != "Interaction service unavailable." {
		t.Fatalf("expected missing TaskEngine rejection, err=%v answer=%q", err, inter2.answer)
	}
}

func TestCallbackIngress_InvalidUnknownAndDuplicate(t *testing.T) {
	taskClient := &testTaskClient{}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 7}, 1, 7, 1)

	invalidInter := &recordingInteraction{mockInteraction: &mockInteraction{}}
	invalid := messageEvent(201, 7, []byte("malformed"), target)
	canonicalRouter := corecallback.NewRouter(zap.NewNop(), corecallback.NewStateStore())
	if err := dispatchCoreCallback(context.Background(), canonicalRouter, taskClient, nil, newCallbackQueryDeduper(), invalid, newAssistantCallbackServicer(201, target, invalidInter), zap.NewNop()); !errors.Is(err, corecallback.ErrInvalidCallbackData) {
		t.Fatalf("malformed callback error = %v, want ErrInvalidCallbackData", err)
	}
	if invalidInter.answer != "Invalid callback" {
		t.Fatalf("invalid answer = %q", invalidInter.answer)
	}

	unknownInter := &recordingInteraction{mockInteraction: &mockInteraction{}}
	unknown := messageEvent(202, 7, []byte("v1:missing:run:noop"), target)
	err := dispatchCoreCallback(context.Background(), &mockCoreDispatcher{}, taskClient, nil, newCallbackQueryDeduper(), unknown, newAssistantCallbackServicer(202, target, unknownInter), zap.NewNop())
	if !errors.Is(err, corecallback.ErrHandlerNotFound) || unknownInter.answer != "Feature not available." {
		t.Fatalf("unknown result err=%v answer=%q", err, unknownInter.answer)
	}

	deduper := newCallbackQueryDeduper()
	dupInter1 := &recordingInteraction{mockInteraction: &mockInteraction{}}
	dupInter2 := &recordingInteraction{mockInteraction: &mockInteraction{}}
	router := &mockCoreDispatcher{hasHandlerFunc: func(string) bool { return true }}
	first := messageEvent(203, 7, []byte("v1:myxl:refresh:noop"), target)
	second := messageEvent(203, 7, []byte("v1:myxl:refresh:noop"), target)
	if err := dispatchCoreCallback(context.Background(), router, taskClient, nil, deduper, first, newAssistantCallbackServicer(203, target, dupInter1), zap.NewNop()); err != nil {
		t.Fatalf("first duplicate test dispatch: %v", err)
	}
	if err := dispatchCoreCallback(context.Background(), router, taskClient, nil, deduper, second, newAssistantCallbackServicer(203, target, dupInter2), zap.NewNop()); err != nil {
		t.Fatalf("duplicate dispatch: %v", err)
	}
	if dupInter2.answerCalls != 1 {
		t.Fatalf("duplicate callback must only receive terminal ack, calls=%d", dupInter2.answerCalls)
	}
}

func TestCallbackIngress_DispatchInlineDirectlyToCore(t *testing.T) {
	taskClient := &testTaskClient{}
	inter := &mockInlineInteraction{}
	inlineID := &tg.InputBotInlineMessageID{DCID: 1, ID: 12345, AccessHash: 67890}
	target := interaction.NewInlineTarget(555, inlineID, 999)
	evt := inlineEvent(555, 9, []byte("v1:help:module:myxl"), target)
	var received *core.CallbackQueryEvent
	router := &mockCoreDispatcher{
		hasHandlerFunc: func(ns string) bool { return ns == "help" },
		dispatchFunc: func(ctx context.Context, got *core.CallbackQueryEvent, svc core.TelegramServicer) error {
			received = got
			if err := svc.AnswerCallbackQuery(ctx, got.QueryID, "Help updated!", false); err != nil {
				return err
			}
			return svc.EditInlineBotMessage(ctx, got.Target.InlineID, "Help text", nil)
		},
	}
	if err := dispatchCoreCallback(context.Background(), router, taskClient, nil, newCallbackQueryDeduper(), evt, newAssistantInlineCallbackServicer(evt.QueryID, target, inter), zap.NewNop()); err != nil {
		t.Fatalf("dispatch inline: %v", err)
	}
	if received == nil || received.Origin != core.CallbackOriginInline || !inter.answered || !inter.edited || inter.editText != "Help text" {
		t.Fatalf("inline bridge mismatch evt=%+v inter=%+v", received, inter)
	}
	if taskClient.last.ID != "asst:cb:inline:555" {
		t.Fatalf("inline task id = %s", taskClient.last.ID)
	}
}

func TestCallbackServicers_PreserveTargetsAndSingleFlightAnswer(t *testing.T) {
	base := &tg.InputPeerUser{UserID: 12345, AccessHash: 9999}
	inter := &recordingInteraction{mockInteraction: &mockInteraction{}}
	target := interaction.NewMessageTarget(base, 10, 12345, 1)
	svc := newAssistantCallbackServicer(1, target, inter)

	if err := svc.EditMessageMarkup(context.Background(), nil, 20, "new", nil); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if inter.editTarget.Peer() != base || inter.editTarget.MessageID() != 20 {
		t.Fatalf("partial target merge failed: %+v", inter.editTarget)
	}
	newPeer := &tg.InputPeerChannel{ChannelID: 777, AccessHash: 888}
	if err := svc.EditMessageMarkupOnly(context.Background(), newPeer, 0, nil); err != nil {
		t.Fatalf("edit markup: %v", err)
	}
	if inter.editTarget.Peer() != newPeer || inter.editTarget.MessageID() != 10 {
		t.Fatalf("partial target merge failed: %+v", inter.editTarget)
	}
	if err := svc.DeleteMessage(context.Background(), nil, []int{101, 102, 103}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(inter.deletedList) != 3 {
		t.Fatalf("deleted = %d, want 3", len(inter.deletedList))
	}
	if err := svc.AnswerCallbackQuery(context.Background(), 1, "ok", false); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if err := svc.AnswerCallbackQuery(context.Background(), 1, "again", false); !errors.Is(err, interaction.ErrCallbackAlreadyAnswered) {
		t.Fatalf("second answer err=%v", err)
	}
	if inter.answerCalls != 1 {
		t.Fatalf("answer RPC calls=%d, want 1", inter.answerCalls)
	}
}

func TestCallbackServicers_FailClosed(t *testing.T) {
	ctx := context.Background()
	msgSvc := &assistantCallbackServicer{}
	if err := msgSvc.AnswerCallbackQuery(ctx, 1, "test", false); !errors.Is(err, core.ErrInternal) {
		t.Fatalf("message answer err=%v", err)
	}
	if err := msgSvc.EditInlineBotMessage(ctx, nil, "text", nil); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("message inline edit err=%v", err)
	}

	inlineSvc := &assistantInlineCallbackServicer{}
	if err := inlineSvc.AnswerCallbackQuery(ctx, 1, "test", false); !errors.Is(err, core.ErrInternal) {
		t.Fatalf("inline answer err=%v", err)
	}
	if err := inlineSvc.DeleteMessage(ctx, nil, []int{1}); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("inline delete err=%v", err)
	}
}

func TestUpdateHandlers_CallbackSpinnerProtection(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	api := &mockTelegramAPI{}
	clientInter := interaction.NewClientInteraction(api, zap.NewNop())
	isShutdown := false
	canonicalRouter := corecallback.NewRouter(zap.NewNop(), corecallback.NewStateStore())
	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		Logger: zap.NewNop(), IsShuttingDown: func() bool { return isShutdown },
		Interaction: clientInter, CallbackDispatcher: canonicalRouter,
		CallbackDeduper: newCallbackQueryDeduper(), Tasks: &testTaskClient{},
	})
	ctx := context.Background()

	if err := dispatcher.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateBotCallbackQuery{QueryID: 101, UserID: 1, Data: []byte("malformed_payload")},
	}}); err != nil {
		t.Fatalf("malformed handle: %v", err)
	}
	if api.answerReq == nil || api.answerReq.QueryID != 101 || api.answerReq.Message != "Invalid callback" {
		t.Fatalf("invalid answer: %+v", api.answerReq)
	}

	isShutdown = true
	api.answerReq = nil
	if err := dispatcher.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateBotCallbackQuery{QueryID: 102, UserID: 1, Data: []byte("v1:test:act:noop")},
	}}); err != nil {
		t.Fatalf("shutdown handle: %v", err)
	}
	if api.answerReq == nil || api.answerReq.QueryID != 102 || !strings.Contains(api.answerReq.Message, "shutting down") {
		t.Fatalf("shutdown answer: %+v", api.answerReq)
	}

	isShutdown = false
	api.answerReq = nil
	if err := dispatcher.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateInlineBotCallbackQuery{QueryID: 201, UserID: 1, Data: []byte("malformed_inline")},
	}}); err != nil {
		t.Fatalf("inline malformed handle: %v", err)
	}
	if api.answerReq == nil || api.answerReq.QueryID != 201 || api.answerReq.Message != "Invalid callback" {
		t.Fatalf("inline invalid answer: %+v", api.answerReq)
	}
}

type testCancelledTicket struct{ done chan struct{} }

func (t *testCancelledTicket) TaskID() tasks.TaskID   { return "asst:cb:888" }
func (t *testCancelledTicket) State() tasks.TaskState { return tasks.StateCancelled }
func (t *testCancelledTicket) Done() <-chan struct{}  { return t.done }
func (t *testCancelledTicket) Result() (tasks.TaskResult, bool) {
	return tasks.TaskResult{TaskID: "asst:cb:888", Outcome: tasks.OutcomeCancelled, Cause: tasks.CauseUserCancel, Failure: tasks.FailureInfo{Message: "scope cancelled"}}, true
}
func (t *testCancelledTicket) Wait(ctx context.Context) (tasks.TaskResult, error) {
	res, _ := t.Result()
	return res, nil
}

type testCancelledTaskClient struct{ ticket tasks.Ticket }

func (c *testCancelledTaskClient) Submit(context.Context, tasks.WorkSpec) (tasks.Ticket, error) {
	return c.ticket, nil
}
func (c *testCancelledTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (c *testCancelledTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (c *testCancelledTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestCallbackIngress_TaskCancellationUnblocksAndAnswers(t *testing.T) {
	ticket := &testCancelledTicket{done: make(chan struct{})}
	close(ticket.done)
	router := &mockCoreDispatcher{hasHandlerFunc: func(string) bool { return true }}
	inter := &recordingInteraction{mockInteraction: &mockInteraction{}}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 9}, 100, 9, 1)
	evt := messageEvent(888, 9, []byte("v1:myxl:refresh:123"), target)
	err := dispatchCoreCallback(context.Background(), router, &testCancelledTaskClient{ticket: ticket}, nil, newCallbackQueryDeduper(), evt, newAssistantCallbackServicer(evt.QueryID, target, inter), zap.NewNop())
	if err == nil || !strings.Contains(err.Error(), "scope cancelled") {
		t.Fatalf("expected cancellation error, got %v", err)
	}
	if inter.answer != "Action failed. Please retry." {
		t.Fatalf("cancellation must clear spinner, answer=%q", inter.answer)
	}
}

type canonicalBridgeHandler struct {
	handled bool
}

func (h *canonicalBridgeHandler) Namespace() string { return "bridge" }
func (h *canonicalBridgeHandler) CallbackOptions() corecallback.CallbackHandlerOptions {
	return corecallback.CallbackHandlerOptions{AutoAnswer: false}
}
func (h *canonicalBridgeHandler) HandleCallback(ctx *corecallback.CallbackContext) error {
	h.handled = true
	return ctx.Answer("canonical", false)
}

func TestCallbackIngress_UsesCanonicalCallbackRouter(t *testing.T) {
	router := corecallback.NewRouter(zap.NewNop(), corecallback.NewStateStore())
	handler := &canonicalBridgeHandler{}
	if _, err := router.RegisterOwned("test", handler); err != nil {
		t.Fatalf("register canonical handler: %v", err)
	}

	taskClient := &testTaskClient{}
	inter := &recordingInteraction{mockInteraction: &mockInteraction{}}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 42}, 9, 42, 1)
	evt := messageEvent(990, 42, corecallback.EncodeCallbackData("bridge", "run", "noop"), target)
	svc := newAssistantCallbackServicer(evt.QueryID, target, inter)

	if err := dispatchCoreCallback(context.Background(), router, taskClient, nil, newCallbackQueryDeduper(), evt, svc, zap.NewNop()); err != nil {
		t.Fatalf("dispatch canonical router: %v", err)
	}
	if !handler.handled {
		t.Fatal("canonical callback handler was not executed")
	}
	if inter.answer != "canonical" || inter.answerCalls != 1 {
		t.Fatalf("canonical acknowledgement = %q calls=%d", inter.answer, inter.answerCalls)
	}
}

func TestCallbackQueryDeduper_BoundedAndReusableAfterTTL(t *testing.T) {
	deduper := newCallbackQueryDeduper()
	now := time.Unix(100, 0)
	if !deduper.Admit(1, now) || deduper.Admit(1, now) {
		t.Fatal("query id must be admitted once within the dedupe window")
	}
	reuseAt := now.Add(assistantCallbackDedupTTL)
	if !deduper.Admit(1, reuseAt) {
		t.Fatal("query id must be reusable after the dedupe window")
	}

	fillAt := reuseAt.Add(time.Minute)
	for i := int64(2); i < int64(assistantCallbackDedupMax)+32; i++ {
		if !deduper.Admit(i, fillAt) {
			t.Fatalf("unexpected rejection for unique query %d", i)
		}
	}
	if len(deduper.seen) > assistantCallbackDedupMax {
		t.Fatalf("dedupe map exceeded bound: %d > %d", len(deduper.seen), assistantCallbackDedupMax)
	}
}

type countingAssistantRateLimiter struct {
	calls int
	allow bool
}

func (l *countingAssistantRateLimiter) Allow(int64, string) bool {
	l.calls++
	return l.allow
}

func TestCallbackQueryDeduper_ConcurrentDuplicateStormAdmitsOnce(t *testing.T) {
	deduper := newCallbackQueryDeduper()
	const workers = 256
	start := make(chan struct{})
	results := make(chan bool, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	now := time.Unix(200, 0)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			results <- deduper.Admit(9001, now)
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	admitted := 0
	for ok := range results {
		if ok {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("concurrent duplicate storm admitted %d callbacks, want 1", admitted)
	}
	if len(deduper.seen) != 1 {
		t.Fatalf("dedupe cardinality after duplicate storm = %d, want 1", len(deduper.seen))
	}
}

func TestUpdateHandlers_A2DuplicateSuppressedBeforeRateLimit(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	api := &mockTelegramAPI{}
	clientInter := interaction.NewClientInteraction(api, zap.NewNop())
	transportLimiter := &countingAssistantRateLimiter{allow: true}
	deduper := newCallbackQueryDeduper()

	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		Logger:          zap.NewNop(),
		RateLimiter:     transportLimiter,
		Interaction:     clientInter,
		CallbackDeduper: deduper,
	})

	update := &tg.UpdateInlineBotCallbackQuery{
		QueryID:      702,
		UserID:       42,
		MsgID:        &tg.InputBotInlineMessageID{DCID: 1, ID: 100, AccessHash: 7},
		ChatInstance: 1,
		Data:         []byte("a2:synthetic:next:invalid.1"),
	}
	for i := 0; i < 2; i++ {
		if err := dispatcher.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{update}}); err != nil {
			t.Fatalf("a2 duplicate handle %d: %v", i+1, err)
		}
	}
	if transportLimiter.calls != 1 {
		t.Fatalf("duplicate a2 delivery consumed transport limiter %d times, want 1", transportLimiter.calls)
	}
	if api.answerReq == nil || api.answerReq.QueryID != 702 || api.answerReq.Message != "" {
		t.Fatalf("duplicate a2 callback must receive terminal empty ack, got %+v", api.answerReq)
	}
}

func TestUpdateHandlers_V1UsesOnlyCanonicalCallbackRateLimit(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	api := &mockTelegramAPI{}
	clientInter := interaction.NewClientInteraction(api, zap.NewNop())
	transportLimiter := &countingAssistantRateLimiter{allow: false}
	dispatched := false
	coreDispatcher := &mockCoreDispatcher{
		hasHandlerFunc: func(string) bool { return true },
		dispatchFunc: func(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
			dispatched = true
			return svc.AnswerCallbackQuery(ctx, evt.QueryID, "ok", false)
		},
	}

	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		Logger:             zap.NewNop(),
		RateLimiter:        transportLimiter,
		Interaction:        clientInter,
		CallbackDispatcher: coreDispatcher,
		CallbackDeduper:    newCallbackQueryDeduper(),
		Tasks:              &testTaskClient{},
	})

	inlineID := &tg.InputBotInlineMessageID{DCID: 1, ID: 99, AccessHash: 7}
	if err := dispatcher.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateInlineBotCallbackQuery{
			QueryID:      700,
			UserID:       42,
			MsgID:        inlineID,
			ChatInstance: 1,
			Data:         []byte("v1:test:run:noop"),
		},
	}}); err != nil {
		t.Fatalf("v1 callback handle: %v", err)
	}
	if transportLimiter.calls != 0 {
		t.Fatalf("v1 callback hit Assistant transport limiter %d times; canonical router must own v1 rate limiting", transportLimiter.calls)
	}
	if !dispatched {
		t.Fatal("v1 callback did not reach canonical callback dispatcher")
	}

	dispatched = false
	if err := dispatcher.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateInlineBotCallbackQuery{
			QueryID:      701,
			UserID:       42,
			MsgID:        inlineID,
			ChatInstance: 1,
			Data:         []byte("a2:synthetic"),
		},
	}}); err != nil {
		t.Fatalf("a2 callback handle: %v", err)
	}
	if transportLimiter.calls != 1 {
		t.Fatalf("a2 callback must retain transport limiter, calls=%d", transportLimiter.calls)
	}
	if dispatched {
		t.Fatal("rate-limited a2 callback reached canonical v1 dispatcher")
	}
}
