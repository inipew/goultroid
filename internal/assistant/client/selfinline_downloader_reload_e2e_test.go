package client

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgmock"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/plugin"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/services/download"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/plugins/downloader"
	"go.uber.org/zap"
)

func TestSelfInlineDownloaderDisableEnableRejectsOldGenerationAndRebindsNew(t *testing.T) {
	ctx := context.Background()
	inlineRegistry := inlineservice.NewRegistry()
	baseTasks := &downloaderContinuationTasks{}
	manager := plugin.NewManager(core.NewRouter("."))
	manager.SetInlineRegistry(inlineRegistry)
	manager.SetTaskClient(baseTasks)

	gate := plugin.NewCapabilityGate()
	if err := gate.RegisterManifest(plugin.Manifest{
		ID:      "downloader",
		Name:    "downloader",
		Version: "1",
		Capabilities: []string{
			plugin.CapFilesystemData,
			plugin.CapTasks,
		},
	}); err != nil {
		t.Fatalf("RegisterManifest(downloader) error=%v", err)
	}
	fs, err := filesystem.NewManager(t.TempDir(), t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("filesystem.NewManager() error=%v", err)
	}
	manager.SetPlatformServices(gate, nil, nil, fs, nil, nil)

	downloadRegistry := download.NewRegistry(
		download.NewExtractorProvider(nil, 500*1024*1024),
		download.NewDirectHTTPProvider(time.Minute, 500*1024*1024),
	)
	downloadPlugin := downloader.New(downloadRegistry, baseTasks)
	if err := manager.RegisterWithContext(ctx, downloadPlugin); err != nil {
		t.Fatalf("RegisterWithContext(downloader) error=%v", err)
	}
	t.Cleanup(func() { _ = manager.ShutdownWithContext(context.Background()) })

	inlineEngine := inlineservice.NewEngine(inlineRegistry, zap.NewNop())
	inlineEngine.SetPermissions(core.NewPermissions(p1E2EOwnerID, nil))
	inlineEngine.SetFeatureCatalog(manager.FeatureCatalog())
	inlineEngine.SetInteractionRuntime(manager.InteractionRuntime())

	broker := &p1SelfInlineBroker{
		ctx:     ctx,
		engine:  inlineEngine,
		ownerID: p1E2EOwnerID,
	}
	assistantAPI := tg.NewClient(tgmock.Invoker(broker.assistantInvoke))
	assistantService := newAssistantInlineQueryServicer(assistantAPI, nil)
	broker.assistant = assistantService

	presentationService := newInteractionPresentationServicer(
		assistantinteraction.NewClientInteraction(assistantAPI, zap.NewNop()),
	)
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
	actionClient.SetInteractionDrivers([]assistantinteraction.FeatureDriver{downloadPlugin})
	actionClient.SetInteractionFoundation(manager.FeatureCatalog(), manager.InteractionRuntime(), manager.ActionDispatcher())
	actionClient.interactionIngress = &interactionIngress{
		engine: interactionEngine,
		ack:    presentationService,
	}
	if err := actionClient.RefreshInteractionBindings(); err != nil {
		t.Fatalf("RefreshInteractionBindings(initial) error=%v", err)
	}
	t.Cleanup(actionClient.unbindFeatureDrivers)
	manager.SetRegistrationValidator(func(context.Context) error {
		return actionClient.RefreshInteractionBindings()
	})

	firstScope, ok := manager.FeatureCatalog().FeatureScope(downloadPlugin.Name())
	if !ok || firstScope.IsZero() {
		t.Fatal("downloader feature scope missing before disable")
	}

	const rawURL = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	mintAudioCallback := func(queryID int64) []byte {
		t.Helper()
		broker.setReq = nil
		if err := inlineEngine.Execute(ctx, assistantService, queryID, p1E2EOwnerID, "dl "+rawURL, ""); err != nil {
			t.Fatalf("Execute(downloader inline) error=%v", err)
		}
		return p1CallbackDataFromAssistantAnswer(t, broker.setReq)
	}

	inlineMessageID := &tg.InputBotInlineMessageID{DCID: 2, ID: 778902, AccessHash: 0x1237}
	outerTasks := &preparedCallbackTasks{}
	ack := &preparedCallbackAck{}
	dispatchIngress := &interactionIngress{engine: interactionEngine, ack: ack, tasks: outerTasks}

	initialCallback := mintAudioCallback(99100)
	handled, err := dispatchIngress.tryInline(ctx, initialCallback, p1E2EOwnerID, 99101, inlineMessageID)
	if err != nil {
		t.Fatalf("tryInline(initial downloader generation) error=%v", err)
	}
	if !handled || outerTasks.calls != 1 {
		t.Fatalf("initial callback handled=%v TaskEngine calls=%d, want true/1", handled, outerTasks.calls)
	}

	staleCallback := mintAudioCallback(99102)
	if stats := manager.InteractionRuntime().Stats(); stats.Sessions < 1 {
		t.Fatalf("downloader sessions before disable=%+v, want active session", stats)
	}

	if err := manager.Disable(ctx, downloadPlugin.Name()); err != nil {
		t.Fatalf("Disable(downloader) error=%v", err)
	}
	if err := actionClient.RefreshInteractionBindings(); err != nil {
		t.Fatalf("RefreshInteractionBindings(disabled) error=%v", err)
	}
	if stats := manager.InteractionRuntime().Stats(); stats.Sessions != 0 {
		t.Fatalf("downloader sessions after disable=%+v, want zero", stats)
	}
	callsBeforeStale := outerTasks.calls
	if handled, err := dispatchIngress.tryInline(ctx, staleCallback, p1E2EOwnerID, 99103, inlineMessageID); !handled || err == nil {
		t.Fatalf("disabled-generation callback handled=%v err=%v, want claimed rejection", handled, err)
	}
	if outerTasks.calls != callsBeforeStale {
		t.Fatalf("stale downloader callback reached TaskEngine: calls=%d want=%d", outerTasks.calls, callsBeforeStale)
	}

	if err := manager.Enable(ctx, downloadPlugin.Name()); err != nil {
		t.Fatalf("Enable(downloader) error=%v", err)
	}
	secondScope, ok := manager.FeatureCatalog().FeatureScope(downloadPlugin.Name())
	if !ok || secondScope.IsZero() {
		t.Fatal("downloader feature scope missing after enable")
	}
	if secondScope == firstScope {
		t.Fatalf("downloader scope after enable=%+v, want new generation from %+v", secondScope, firstScope)
	}

	if handled, err := dispatchIngress.tryInline(ctx, staleCallback, p1E2EOwnerID, 99104, inlineMessageID); !handled || err == nil {
		t.Fatalf("old downloader callback revived after enable: handled=%v err=%v", handled, err)
	}
	if outerTasks.calls != callsBeforeStale {
		t.Fatalf("old downloader callback reached TaskEngine after enable: calls=%d want=%d", outerTasks.calls, callsBeforeStale)
	}

	freshCallback := mintAudioCallback(99105)
	editCallsBeforeFresh := broker.editCalls
	handled, err = dispatchIngress.tryInline(ctx, freshCallback, p1E2EOwnerID, 99106, inlineMessageID)
	if err != nil {
		t.Fatalf("tryInline(re-enabled downloader generation) error=%v", err)
	}
	if !handled || outerTasks.calls != callsBeforeStale+1 {
		t.Fatalf("fresh callback handled=%v TaskEngine calls=%d, want true/%d", handled, outerTasks.calls, callsBeforeStale+1)
	}
	if broker.editCalls != editCallsBeforeFresh+1 {
		t.Fatalf("fresh downloader callback edits=%d, want %d", broker.editCalls, editCallsBeforeFresh+1)
	}
}
