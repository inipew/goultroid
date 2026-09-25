package client

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgmock"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	telegramservice "github.com/inipew/goultroid/internal/telegram"
	"github.com/inipew/goultroid/plugins/calculator"
	"go.uber.org/zap"
)

const (
	p1E2EOwnerID       int64 = 42
	p1E2EAssistantID   int64 = 9001
	p1E2EAssistantHash int64 = 0x12345678
)

type p1SelfInlineBroker struct {
	ctx       context.Context
	engine    *inlineservice.Engine
	assistant core.TelegramServicer
	ownerID   int64

	querySeq int64
	events   []string
	getReq   *tg.MessagesGetInlineBotResultsRequest
	setReq   *tg.MessagesSetInlineBotResultsRequest
	sendReq  *tg.MessagesSendInlineBotResultRequest
	editReq  *tg.MessagesEditInlineBotMessageRequest

	queryCalls  int
	answerCalls int
	sendCalls   int
	editCalls   int
}

func (b *p1SelfInlineBroker) assistantInvoke(input bin.Encoder) (bin.Encoder, error) {
	switch req := input.(type) {
	case *tg.MessagesSetInlineBotResultsRequest:
		b.answerCalls++
		b.events = append(b.events, "answer")
		b.setReq = req
		return &tg.BoolTrue{}, nil
	case *tg.MessagesEditInlineBotMessageRequest:
		b.editCalls++
		b.events = append(b.events, "edit")
		b.editReq = req
		return &tg.BoolTrue{}, nil
	default:
		return nil, fmt.Errorf("unexpected Assistant RPC %T", input)
	}
}

func (b *p1SelfInlineBroker) userbotInvoke(input bin.Encoder) (bin.Encoder, error) {
	switch req := input.(type) {
	case *tg.ContactsResolveUsernameRequest:
		if req.Username != "assistant_bot" {
			return nil, fmt.Errorf("unexpected resolved username %q", req.Username)
		}
		user := &tg.User{ID: p1E2EAssistantID}
		user.SetAccessHash(p1E2EAssistantHash)
		user.SetUsername("assistant_bot")
		return &tg.ContactsResolvedPeer{
			Peer:  &tg.PeerUser{UserID: p1E2EAssistantID},
			Users: []tg.UserClass{user},
		}, nil

	case *tg.MessagesGetInlineBotResultsRequest:
		b.queryCalls++
		b.events = append(b.events, "query")
		b.getReq = req
		b.querySeq++
		queryID := int64(88000) + b.querySeq
		b.setReq = nil
		if err := b.engine.Execute(b.ctx, b.assistant, queryID, b.ownerID, req.Query, req.Offset); err != nil {
			return nil, fmt.Errorf("execute production inline engine: %w", err)
		}
		if b.setReq == nil {
			return nil, errors.New("Assistant did not answer inline query")
		}
		results, err := p1BotResultsFromAssistantAnswer(b.setReq.Results)
		if err != nil {
			return nil, err
		}
		return &tg.MessagesBotResults{
			QueryID:   queryID,
			Results:   results,
			CacheTime: b.setReq.CacheTime,
		}, nil

	case *tg.MessagesSendInlineBotResultRequest:
		b.sendCalls++
		b.events = append(b.events, "send")
		b.sendReq = req
		return &tg.UpdatesTooLong{}, nil

	default:
		return nil, fmt.Errorf("unexpected userbot RPC %T", input)
	}
}

func p1BotResultsFromAssistantAnswer(results []tg.InputBotInlineResultClass) ([]tg.BotInlineResultClass, error) {
	out := make([]tg.BotInlineResultClass, 0, len(results))
	for _, raw := range results {
		result, ok := raw.(*tg.InputBotInlineResult)
		if !ok {
			return nil, fmt.Errorf("unexpected inline result type %T", raw)
		}
		out = append(out, &tg.BotInlineResult{
			ID:   result.ID,
			Type: result.Type,
			SendMessage: &tg.BotInlineMessageText{
				Message: result.ID,
			},
		})
	}
	return out, nil
}

func p1CallbackDataFromAssistantAnswer(t *testing.T, req *tg.MessagesSetInlineBotResultsRequest) []byte {
	t.Helper()
	if req == nil || len(req.Results) != 1 {
		t.Fatalf("Assistant answer results=%v, want exactly one", req)
	}
	result, ok := req.Results[0].(*tg.InputBotInlineResult)
	if !ok {
		t.Fatalf("Assistant result type=%T, want *tg.InputBotInlineResult", req.Results[0])
	}
	message, ok := result.SendMessage.(*tg.InputBotInlineMessageText)
	if !ok {
		t.Fatalf("Assistant send message type=%T, want *tg.InputBotInlineMessageText", result.SendMessage)
	}
	markup, ok := message.ReplyMarkup.(*tg.ReplyInlineMarkup)
	if !ok || markup == nil {
		t.Fatalf("Assistant result markup=%T, want ReplyInlineMarkup", message.ReplyMarkup)
	}
	for _, row := range markup.Rows {
		for _, raw := range row.Buttons {
			if callback, ok := raw.(*tg.KeyboardButtonCallback); ok && len(callback.Data) > 0 {
				return append([]byte(nil), callback.Data...)
			}
		}
	}
	t.Fatal("Assistant result did not contain a compiled a2 callback")
	return nil
}

func TestP1SelfInlineTrueEndToEndAndReloadAcceptance(t *testing.T) {
	ctx := context.Background()
	registry := inlineservice.NewRegistry()
	manager := plugin.NewManager(core.NewRouter("."))
	manager.SetInlineRegistry(registry)
	calculatorPlugin := calculator.New()
	if err := manager.RegisterWithContext(ctx, calculatorPlugin); err != nil {
		t.Fatalf("RegisterWithContext(calculator) error=%v", err)
	}
	t.Cleanup(func() { _ = manager.ShutdownWithContext(context.Background()) })

	engine := inlineservice.NewEngine(registry, zap.NewNop())
	engine.SetPermissions(core.NewPermissions(p1E2EOwnerID, nil))
	engine.SetFeatureCatalog(manager.FeatureCatalog())
	engine.SetInteractionRuntime(manager.InteractionRuntime())

	broker := &p1SelfInlineBroker{
		ctx:     ctx,
		engine:  engine,
		ownerID: p1E2EOwnerID,
	}
	assistantAPI := tg.NewClient(tgmock.Invoker(broker.assistantInvoke))
	assistantService := newAssistantInlineQueryServicer(assistantAPI, nil)
	broker.assistant = assistantService

	userbotAPI := tg.NewClient(tgmock.Invoker(broker.userbotInvoke))
	rpcExecutor, err := telegramservice.NewRPCExecutor(telegramservice.RPCExecutorConfig{})
	if err != nil {
		t.Fatalf("NewRPCExecutor() error=%v", err)
	}
	resolver := telegramservice.NewResolverWithContextAndExecutor(
		ctx,
		userbotAPI,
		nil,
		telegramservice.ResolverCacheConfig{},
		rpcExecutor,
	)
	t.Cleanup(func() { _ = resolver.Close() })
	userbotService := telegramservice.NewServiceWithExecutor(userbotAPI, rpcExecutor)
	userbotService.SetResolver(resolver)

	// This is the real self-inline bridge over production telegram.Service. The
	// only test seam is gotd's raw tg.Invoker beneath generated MTProto clients.
	renderer := selfinline.NewWithIdentity(userbotService, func() (string, error) {
		return "assistant_bot", nil
	})
	request := selfinline.Request{
		Peer:     &tg.InputPeerChat{ChatID: 77},
		Query:    "calc 1+2",
		ResultID: "calculator",
	}
	preparedBeforeReload, err := engine.Prepare(request.Query)
	if err != nil {
		t.Fatalf("Prepare(before reload) error=%v", err)
	}

	result, err := renderer.Render(ctx, request)
	if err != nil {
		t.Fatalf("Render() error=%v", err)
	}
	if result.ResultID != "calculator" || result.QueryID == 0 || result.RandomID == 0 {
		t.Fatalf("Render() result=%+v", result)
	}
	if !reflect.DeepEqual(broker.events, []string{"query", "answer", "send"}) {
		t.Fatalf("MTProto flow=%v, want query -> answer -> send", broker.events)
	}
	if broker.queryCalls != 1 || broker.answerCalls != 1 || broker.sendCalls != 1 {
		t.Fatalf("query/answer/send=%d/%d/%d, want 1/1/1", broker.queryCalls, broker.answerCalls, broker.sendCalls)
	}
	if broker.getReq == nil || broker.getReq.Query != "calc 1+2" {
		t.Fatalf("messages.getInlineBotResults request=%+v", broker.getReq)
	}
	if bot, ok := broker.getReq.Bot.(*tg.InputUser); !ok || bot.UserID != p1E2EAssistantID || bot.AccessHash != p1E2EAssistantHash {
		t.Fatalf("resolved inline bot=%T %+v", broker.getReq.Bot, broker.getReq.Bot)
	}
	if broker.setReq == nil || broker.setReq.QueryID != result.QueryID || !broker.setReq.Private || broker.setReq.CacheTime != 0 {
		t.Fatalf("messages.setInlineBotResults request=%+v", broker.setReq)
	}
	if broker.sendReq == nil || broker.sendReq.QueryID != result.QueryID || broker.sendReq.ID != "calculator" || broker.sendReq.RandomID != result.RandomID {
		t.Fatalf("messages.sendInlineBotResult request=%+v", broker.sendReq)
	}

	callbackData := p1CallbackDataFromAssistantAnswer(t, broker.setReq)

	assistantInteraction := assistantinteraction.NewClientInteraction(assistantAPI, zap.NewNop())
	presentationService := newInteractionPresentationServicer(assistantInteraction)
	interactionEngine, err := orchestration.New(
		manager.InteractionRuntime(),
		manager.ActionDispatcher(),
		presentationtelegram.NewBridge(presentationService),
	)
	if err != nil {
		t.Fatalf("orchestration.New() error=%v", err)
	}
	actionClient := NewAssistantClient(1, "hash", "token", zap.NewNop())
	actionClient.SetOwner(p1E2EOwnerID, nil)
	actionClient.SetInteractionDrivers([]assistantinteraction.FeatureDriver{calculatorPlugin})
	actionClient.SetInteractionFoundation(manager.FeatureCatalog(), manager.InteractionRuntime(), manager.ActionDispatcher())
	if err := actionClient.bindFeatureDrivers(interactionEngine, manager.FeatureCatalog(), presentationService); err != nil {
		t.Fatalf("bindFeatureDrivers() error=%v", err)
	}
	t.Cleanup(actionClient.unbindFeatureDrivers)

	taskClient := &preparedCallbackTasks{}
	ack := &preparedCallbackAck{}
	ingress := &interactionIngress{engine: interactionEngine, ack: ack, tasks: taskClient}
	inlineMessageID := &tg.InputBotInlineMessageID{
		DCID:       2,
		ID:         778899,
		AccessHash: 0x1234,
	}
	handled, err := ingress.tryInline(ctx, callbackData, p1E2EOwnerID, 99000, inlineMessageID)
	if err != nil {
		t.Fatalf("tryInline(real callback) error=%v", err)
	}
	if !handled {
		t.Fatal("tryInline(real callback) did not claim a2 callback")
	}
	if taskClient.calls != 1 {
		t.Fatalf("callback TaskEngine submissions=%d, want 1", taskClient.calls)
	}
	if ack.calls != 1 || ack.err != nil {
		t.Fatalf("callback completion ack calls=%d err=%v", ack.calls, ack.err)
	}
	if broker.editCalls != 1 || broker.editReq == nil {
		t.Fatalf("inline transition edits=%d request=%v, want one edit", broker.editCalls, broker.editReq)
	}
	if !reflect.DeepEqual(broker.events, []string{"query", "answer", "send", "edit"}) {
		t.Fatalf("callback MTProto flow=%v, want query -> answer -> send -> edit", broker.events)
	}

	if stats := manager.InteractionRuntime().Stats(); stats.Sessions != 1 {
		t.Fatalf("interaction sessions after inline answer=%+v, want one", stats)
	}
	firstScope, ok := manager.Scope(calculatorPlugin.Name())
	if !ok {
		t.Fatal("calculator scope missing before reload")
	}
	firstGeneration := firstScope.Generation()

	if err := manager.Disable(ctx, calculatorPlugin.Name()); err != nil {
		t.Fatalf("Disable(calculator) error=%v", err)
	}
	if stats := manager.InteractionRuntime().Stats(); stats.Sessions != 0 {
		t.Fatalf("interaction sessions after disable=%+v, want zero", stats)
	}
	if _, err := manager.InteractionRuntime().ResolveCallback(ctx, callbackData, rootinteraction.Binding{ActorID: p1E2EOwnerID}); err == nil {
		t.Fatal("old callback resolved after plugin disable")
	}

	if err := manager.Enable(ctx, calculatorPlugin.Name()); err != nil {
		t.Fatalf("Enable(calculator) error=%v", err)
	}
	secondScope, ok := manager.Scope(calculatorPlugin.Name())
	if !ok {
		t.Fatal("calculator scope missing after reload")
	}
	if secondScope.Generation() == firstGeneration {
		t.Fatalf("plugin reload reused generation %d", firstGeneration)
	}
	if _, err := manager.InteractionRuntime().ResolveCallback(ctx, callbackData, rootinteraction.Binding{ActorID: p1E2EOwnerID}); err == nil {
		t.Fatal("old callback revived after plugin reload")
	}
	if err := engine.ExecutePreparedWithPeerType(
		ctx,
		assistantService,
		99001,
		p1E2EOwnerID,
		preparedBeforeReload,
		"",
		nil,
	); !errors.Is(err, inlineservice.ErrStaleHandler) {
		t.Fatalf("ExecutePrepared(old inline generation) error=%v, want %v", err, inlineservice.ErrStaleHandler)
	}

	broker.events = nil
	fresh, err := renderer.Render(ctx, request)
	if err != nil {
		t.Fatalf("Render(after reload) error=%v", err)
	}
	if fresh.QueryID == result.QueryID || fresh.ResultID != "calculator" {
		t.Fatalf("fresh render result=%+v, previous=%+v", fresh, result)
	}
	if !reflect.DeepEqual(broker.events, []string{"query", "answer", "send"}) {
		t.Fatalf("post-reload MTProto flow=%v", broker.events)
	}
	if broker.queryCalls != 2 || broker.answerCalls != 2 || broker.sendCalls != 2 {
		t.Fatalf("post-reload query/answer/send=%d/%d/%d, want 2/2/2", broker.queryCalls, broker.answerCalls, broker.sendCalls)
	}
	freshCallbackData := p1CallbackDataFromAssistantAnswer(t, broker.setReq)
	if _, err := manager.InteractionRuntime().ResolveCallback(ctx, freshCallbackData, rootinteraction.Binding{ActorID: p1E2EOwnerID}); err != nil {
		t.Fatalf("fresh callback after plugin reload error=%v", err)
	}
	if stats := manager.InteractionRuntime().Stats(); stats.Sessions != 1 {
		t.Fatalf("fresh interaction sessions=%+v, want one", stats)
	}
}
