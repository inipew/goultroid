package client

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgmock"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/services/download"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/tasks"
	telegramservice "github.com/inipew/goultroid/internal/telegram"
	"github.com/inipew/goultroid/plugins/downloader"
	"go.uber.org/zap"
)

type downloaderContinuationTasks struct {
	specs []tasks.WorkSpec
}

func (c *downloaderContinuationTasks) Submit(_ context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.specs = append(c.specs, spec)
	done := make(chan struct{})
	close(done)
	return &downloaderContinuationTicket{id: spec.ID, done: done}, nil
}

func (*downloaderContinuationTasks) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*downloaderContinuationTasks) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*downloaderContinuationTasks) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type downloaderContinuationTicket struct {
	id   tasks.TaskID
	done chan struct{}
}

func (t *downloaderContinuationTicket) TaskID() tasks.TaskID  { return t.id }
func (*downloaderContinuationTicket) State() tasks.TaskState  { return tasks.StateQueued }
func (t *downloaderContinuationTicket) Done() <-chan struct{} { return t.done }
func (*downloaderContinuationTicket) Result() (tasks.TaskResult, bool) {
	return tasks.TaskResult{}, false
}
func (*downloaderContinuationTicket) Wait(context.Context) (tasks.TaskResult, error) {
	return tasks.TaskResult{}, nil
}

func callbackDataFromInlineEdit(t *testing.T, req *tg.MessagesEditInlineBotMessageRequest) []byte {
	t.Helper()
	if req == nil {
		t.Fatal("inline edit request is nil")
	}
	markup, ok := req.ReplyMarkup.(*tg.ReplyInlineMarkup)
	if !ok || markup == nil {
		t.Fatalf("inline edit markup=%T, want ReplyInlineMarkup", req.ReplyMarkup)
	}
	for _, row := range markup.Rows {
		for _, raw := range row.Buttons {
			if callback, ok := raw.(*tg.KeyboardButtonCallback); ok && len(callback.Data) > 0 {
				return append([]byte(nil), callback.Data...)
			}
		}
	}
	t.Fatal("inline edit did not contain callback data")
	return nil
}

func hasTaskResource(resources []tasks.ResourceRequirement, name string) bool {
	for _, resource := range resources {
		if resource.Name == name && resource.Amount > 0 {
			return true
		}
	}
	return false
}

func TestSelfInlineDownloaderTrueEndToEndQueuesBoundedExtractorContinuation(t *testing.T) {
	ctx := context.Background()
	catalog := feature.NewRegistry()
	inlineRegistry := inlineservice.NewRegistry()
	continuations := &downloaderContinuationTasks{}
	downloadRegistry := download.NewRegistry(
		download.NewExtractorProvider(nil, 500*1024*1024),
		download.NewDirectHTTPProvider(time.Minute, 500*1024*1024),
	)
	plugin := downloader.New(downloadRegistry, continuations)
	scope := tasks.ScopeIdentity{Owner: "plugin:downloader", Generation: 1}
	featureRegistration, err := catalog.Register(feature.Owner{ID: plugin.Name(), Scope: scope}, plugin.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer featureRegistration.Close()

	for _, binding := range plugin.InlineBindings() {
		registration, err := inlineRegistry.RegisterOwned(plugin.Name(), binding.InteractionID, scope, binding.Handler, binding.Priority)
		if err != nil {
			t.Fatalf("RegisterOwned(%s) error=%v", binding.InteractionID, err)
		}
		defer registration.Close()
	}

	runtime, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	dispatcher := rootinteraction.NewDispatcher(runtime)

	engine := inlineservice.NewEngine(inlineRegistry, zap.NewNop())
	engine.SetPermissions(core.NewPermissions(p1E2EOwnerID, nil))
	engine.SetFeatureCatalog(catalog)
	engine.SetInteractionRuntime(runtime)

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

	const rawURL = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	renderer := selfinline.NewWithIdentity(userbotService, func() (string, error) {
		return "assistant_bot", nil
	})
	result, err := renderer.Render(ctx, selfinline.Request{
		Peer:     &tg.InputPeerChat{ChatID: 77},
		Query:    "dl " + rawURL,
		ResultID: "downloader",
	})
	if err != nil {
		t.Fatalf("Render(downloader) error=%v", err)
	}
	if result.ResultID != "downloader" || result.QueryID == 0 || result.RandomID == 0 {
		t.Fatalf("Render(downloader) result=%+v", result)
	}
	if !reflect.DeepEqual(broker.events, []string{"query", "answer", "send"}) {
		t.Fatalf("downloader MTProto flow=%v, want query -> answer -> send", broker.events)
	}
	firstCallback := p1CallbackDataFromAssistantAnswer(t, broker.setReq)

	assistantInteraction := assistantinteraction.NewClientInteraction(assistantAPI, zap.NewNop())
	presentationService := newInteractionPresentationServicer(assistantInteraction)
	interactionEngine, err := orchestration.New(runtime, dispatcher, presentationtelegram.NewBridge(presentationService))
	if err != nil {
		t.Fatalf("orchestration.New() error=%v", err)
	}
	actionClient := NewAssistantClient(1, "hash", "token", zap.NewNop())
	actionClient.SetOwner(p1E2EOwnerID, nil)
	actionClient.SetInteractionDrivers([]assistantinteraction.FeatureDriver{plugin})
	actionClient.SetInteractionFoundation(catalog, runtime, dispatcher)
	if err := actionClient.bindFeatureDrivers(interactionEngine, catalog, presentationService); err != nil {
		t.Fatalf("bindFeatureDrivers(downloader) error=%v", err)
	}
	t.Cleanup(actionClient.unbindFeatureDrivers)

	outerTasks := &preparedCallbackTasks{}
	ack := &preparedCallbackAck{}
	ingress := &interactionIngress{engine: interactionEngine, ack: ack, tasks: outerTasks}
	inlineMessageID := &tg.InputBotInlineMessageID{DCID: 2, ID: 778901, AccessHash: 0x1236}

	handled, err := ingress.tryInline(ctx, firstCallback, p1E2EOwnerID, 99020, inlineMessageID)
	if err != nil {
		t.Fatalf("tryInline(downloader audio) error=%v", err)
	}
	if !handled || outerTasks.calls != 1 || broker.editCalls != 1 {
		t.Fatalf("audio transition handled=%v outerTasks=%d edits=%d", handled, outerTasks.calls, broker.editCalls)
	}
	if !strings.Contains(broker.editReq.Message, "Choose audio format") {
		t.Fatalf("audio transition text=%q", broker.editReq.Message)
	}

	formatCallback := callbackDataFromInlineEdit(t, broker.editReq)
	handled, err = ingress.tryInline(ctx, formatCallback, p1E2EOwnerID, 99021, inlineMessageID)
	if err != nil {
		t.Fatalf("tryInline(downloader format) error=%v", err)
	}
	if !handled || outerTasks.calls != 2 {
		t.Fatalf("format transition handled=%v outerTasks=%d", handled, outerTasks.calls)
	}
	if broker.editCalls != 2 || !strings.Contains(broker.editReq.Message, "Downloading") {
		t.Fatalf("running transition edits=%d text=%q", broker.editCalls, broker.editReq.Message)
	}
	if len(continuations.specs) != 1 {
		t.Fatalf("downloader continuations=%d, want 1", len(continuations.specs))
	}
	spec := continuations.specs[0]
	if spec.Pool != tasks.PoolID("download") || !hasTaskResource(spec.Resources, "download") || !hasTaskResource(spec.Resources, "process") {
		t.Fatalf("extractor continuation pool=%q resources=%+v", spec.Pool, spec.Resources)
	}
	if string(spec.Input) != rawURL {
		t.Fatalf("extractor continuation input=%q, want %q", string(spec.Input), rawURL)
	}
	if ack.calls != 2 || ack.err != nil {
		t.Fatalf("downloader callback completion ack calls=%d err=%v", ack.calls, ack.err)
	}
	if !reflect.DeepEqual(broker.events, []string{"query", "answer", "send", "edit", "edit"}) {
		t.Fatalf("downloader callback MTProto flow=%v", broker.events)
	}
}
