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
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

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
		InlineEngine: executor, InlineService: newAssistantInlineQueryServicer(api, nil), Tasks: taskClient,
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
		InlineEngine: &recordingInlineExecutor{}, InlineService: newAssistantInlineQueryServicer(api, nil),
	})
	update := &tg.UpdateBotInlineQuery{QueryID: 88, UserID: 42, Query: "help"}
	if err := dispatcher.Handle(context.Background(), &tg.Updates{Updates: []tg.UpdateClass{update}}); err != nil {
		t.Fatalf("handle inline query: %v", err)
	}
	if api.inlineResultReq == nil || api.inlineResultReq.QueryID != 88 || len(api.inlineResultReq.Results) != 0 {
		t.Fatalf("expected terminal empty answer, got %+v", api.inlineResultReq)
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
	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		Logger: zap.NewNop(), IsShuttingDown: func() bool { return isShutdown },
		Interaction: clientInter,
		CallbackDeduper: newCallbackQueryDeduper(),
	})
	ctx := context.Background()

	if err := dispatcher.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateBotCallbackQuery{QueryID: 101, UserID: 1, Data: []byte("malformed_payload")},
	}}); err != nil {
		t.Fatalf("malformed handle: %v", err)
	}
	if api.answerReq == nil || api.answerReq.QueryID != 101 || api.answerReq.Message != "⌛ Interaction expired. Please reopen it." {
		t.Fatalf("invalid answer: %+v", api.answerReq)
	}

	isShutdown = true
	api.answerReq = nil
	if err := dispatcher.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateBotCallbackQuery{QueryID: 102, UserID: 1, Data: []byte("test:act:noop")},
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
	if api.answerReq == nil || api.answerReq.QueryID != 201 || api.answerReq.Message != "⌛ Interaction expired. Please reopen it." {
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

func TestUpdateHandlers_A2DuplicateBypassesTransportRateLimit(t *testing.T) {
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
	if transportLimiter.calls != 0 {
		t.Fatalf("a2 callback hit transport limiter %d times, want 0", transportLimiter.calls)
	}
	if api.answerReq == nil || api.answerReq.QueryID != 702 || api.answerReq.Message != "" {
		t.Fatalf("duplicate a2 callback must receive terminal empty ack, got %+v", api.answerReq)
	}
}

