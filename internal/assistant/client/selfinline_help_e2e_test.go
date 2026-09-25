package client

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgmock"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	telegramservice "github.com/inipew/goultroid/internal/telegram"
	"go.uber.org/zap"
)

func TestSelfInlineHelpTrueEndToEndDispatchesCanonicalA2Action(t *testing.T) {
	ctx := context.Background()
	registry := inlineservice.NewRegistry()
	manager := plugin.NewManager(core.NewRouter("."))
	manager.SetInlineRegistry(registry)

	shellFeature := assistantshell.NewFeature()
	shellFeature.SetHelpCommandProvider(func() []core.Command {
		return []core.Command{
			{
				Name:        "help",
				Description: "Show help",
				Category:    "Utility",
				Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
			},
			{
				Name:        "ping",
				Aliases:     []string{"p"},
				Description: "Ping command",
				Category:    "Utility",
				Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
			},
		}
	})
	if err := manager.RegisterWithContext(ctx, shellFeature); err != nil {
		t.Fatalf("RegisterWithContext(shell) error=%v", err)
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

	renderer := selfinline.NewWithIdentity(userbotService, func() (string, error) {
		return "assistant_bot", nil
	})
	result, err := renderer.Render(ctx, selfinline.Request{
		Peer:     &tg.InputPeerChat{ChatID: 77},
		Query:    "help",
		ResultID: "assistant_help",
	})
	if err != nil {
		t.Fatalf("Render(help) error=%v", err)
	}
	if result.ResultID != "assistant_help" || result.QueryID == 0 || result.RandomID == 0 {
		t.Fatalf("Render(help) result=%+v", result)
	}
	if !reflect.DeepEqual(broker.events, []string{"query", "answer", "send"}) {
		t.Fatalf("help MTProto flow=%v, want query -> answer -> send", broker.events)
	}
	if broker.setReq == nil || !broker.setReq.Private || broker.setReq.CacheTime != 0 {
		t.Fatalf("help Assistant answer=%+v", broker.setReq)
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
	actionClient.SetInteractionFoundation(manager.FeatureCatalog(), manager.InteractionRuntime(), manager.ActionDispatcher())
	if err := actionClient.syncShellActions(interactionEngine, manager.FeatureCatalog()); err != nil {
		t.Fatalf("syncShellActions() error=%v", err)
	}
	t.Cleanup(actionClient.clearShellActions)

	taskClient := &preparedCallbackTasks{}
	ack := &preparedCallbackAck{}
	ingress := &interactionIngress{engine: interactionEngine, ack: ack, tasks: taskClient}
	inlineMessageID := &tg.InputBotInlineMessageID{
		DCID:       2,
		ID:         778900,
		AccessHash: 0x1235,
	}
	handled, err := ingress.tryInline(ctx, callbackData, p1E2EOwnerID, 99010, inlineMessageID)
	if err != nil {
		t.Fatalf("tryInline(help callback) error=%v", err)
	}
	if !handled {
		t.Fatal("tryInline(help callback) did not claim a2 callback")
	}
	if taskClient.calls != 1 {
		t.Fatalf("help callback TaskEngine submissions=%d, want 1", taskClient.calls)
	}
	if ack.calls != 1 || ack.err != nil {
		t.Fatalf("help callback completion ack calls=%d err=%v", ack.calls, ack.err)
	}
	if broker.editCalls != 1 || broker.editReq == nil {
		t.Fatalf("help inline transition edits=%d request=%v, want one edit", broker.editCalls, broker.editReq)
	}
	if !strings.Contains(broker.editReq.Message, "Utility") {
		t.Fatalf("help module transition text=%q, want Utility module", broker.editReq.Message)
	}
	if !reflect.DeepEqual(broker.events, []string{"query", "answer", "send", "edit"}) {
		t.Fatalf("help callback MTProto flow=%v, want query -> answer -> send -> edit", broker.events)
	}
	if stats := manager.InteractionRuntime().Stats(); stats.Sessions != 1 {
		t.Fatalf("help interaction sessions=%+v, want one", stats)
	}
}
