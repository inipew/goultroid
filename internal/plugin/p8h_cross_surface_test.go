package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/assistant/deeplink"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

type p8hCrossSurfacePlugin struct {
	taskClient tasks.Client
}

func (p *p8hCrossSurfacePlugin) Name() string { return "p8h-cross-surface" }

func (p *p8hCrossSurfacePlugin) Init() error { return nil }

func (p *p8hCrossSurfacePlugin) InitPlugin(ctx PluginContext) error {
	client, err := ctx.TaskClient()
	if err != nil {
		return err
	}
	p.taskClient = client
	return nil
}

func (p *p8hCrossSurfacePlugin) Commands() []core.Command {
	return []core.Command{{
		Name:       "p8hcmd",
		Surfaces:   execution.SurfaceBotAndUser,
		Permission: core.PermissionOwner,
		Invocation: core.InvocationPolicy{
			Userbot:   core.InvocationSelfOnly,
			Assistant: core.InvocationSelfOnly,
		},
		Handler: func(*core.Context) error { return nil },
	}}
}

func (p *p8hCrossSurfacePlugin) FeatureSpec() feature.Spec {
	return feature.Spec{
		ID:   p.Name(),
		Name: "P8-H Cross Surface",
		Interactions: []feature.Interaction{
			{
				ID:       "home",
				Kind:     feature.InteractionScreen,
				Surfaces: execution.SurfaceAssistant,
				Policy:   feature.OwnerPolicy(execution.SurfaceAssistant),
			},
			{
				ID:       "next",
				Kind:     feature.InteractionAction,
				Surfaces: execution.SurfaceAssistant,
				Policy:   feature.OwnerPolicy(execution.SurfaceAssistant),
			},
			{
				ID:       "lookup",
				Kind:     feature.InteractionInline,
				Surfaces: execution.SurfaceInline,
				Policy:   feature.OwnerPolicy(execution.SurfaceInline),
			},
			{
				ID:       "launch",
				Kind:     feature.InteractionDeepLink,
				Surfaces: execution.SurfaceAssistant,
				Policy:   feature.OwnerPolicy(execution.SurfaceAssistant),
			},
		},
	}
}

func (p *p8hCrossSurfacePlugin) InlineBindings() []inlineservice.Binding {
	return []inlineservice.Binding{{
		InteractionID: "lookup",
		Handler:       p8hInlineHandler{},
	}}
}

func (p *p8hCrossSurfacePlugin) ResolveSavedResponse(context.Context, savedresponse.Reference) (savedresponse.Response, bool, error) {
	return savedresponse.NewText("generation-bound response"), true, nil
}

type p8hInlineHandler struct{}

func (p8hInlineHandler) Pattern() string     { return "p8hlookup" }
func (p8hInlineHandler) Description() string { return "P8-H lifecycle lookup" }
func (p8hInlineHandler) HandleInline(*inlineservice.InlineContext) ([]inlineservice.InlineResult, error) {
	return []inlineservice.InlineResult{{ID: "ok", Title: "ok", Text: "ok"}}, nil
}

func newP8HTaskEngine(t *testing.T) *taskengine.Engine {
	t.Helper()
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"interactive": {
				Concurrency:    1,
				MinConcurrency: 0,
				ZeroIdle:       true,
				IdleTimeout:    20 * time.Millisecond,
				BacklogLimit:   16,
				PayloadBudget:  1 << 20,
			},
		},
		ResultCapacity:     32,
		ResourceCapacities: map[string]int64{"download": 1},
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("TaskEngine Start() error=%v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := engine.Stop(ctx); err != nil {
			t.Errorf("TaskEngine Stop() error=%v", err)
		}
	})
	return engine
}

func newP8HDeepLink(t *testing.T, manager *Manager, pluginID string) (*deeplink.Router, *deeplink.SavedResponseProvider, deeplink.Token) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(
		ctx,
		db,
		deeplink.MigrationProvider{},
		savedresponse.MigrationProvider{},
	); err != nil {
		t.Fatalf("RunFeatureMigrations() error=%v", err)
	}

	bindings := savedresponse.NewBindingService(
		savedresponse.NewSQLiteSurfaceBindingRepository(db),
		manager.SavedResponseRegistry(),
	)
	if _, err := bindings.Create(ctx, savedresponse.SurfaceBinding{
		Surface:   savedresponse.SurfaceDeepLink,
		Alias:     "p8h",
		Reference: savedresponse.Reference{Provider: pluginID, ScopeID: 1, Key: "welcome"},
		Enabled:   true,
	}); err != nil {
		t.Fatalf("Create(deep-link binding) error=%v", err)
	}

	provider := deeplink.NewSavedResponseProvider(
		bindings,
		savedresponse.NewResponseDelivery(savedresponse.NewService(nil)),
	)
	router := deeplink.NewRouter(deeplink.NewSQLiteRepository(db))
	registration, err := router.Register(deeplink.SavedResponseKind, provider)
	if err != nil {
		t.Fatalf("Register(deep-link provider) error=%v", err)
	}
	t.Cleanup(registration.Close)

	token, err := provider.Issue(ctx, router, "p8h", 7, time.Hour, false)
	if err != nil {
		t.Fatalf("Issue(deep-link) error=%v", err)
	}
	return router, provider, token
}

func assertP8HSurfaces(t *testing.T, manager *Manager, router *core.Router, inlineRegistry *inlineservice.Registry, pluginID string, wantScope tasks.ScopeIdentity) string {
	t.Helper()

	command, ok := router.Find("p8hcmd")
	if !ok {
		t.Fatal("canonical command surface is not visible")
	}
	if command.Scope != wantScope {
		t.Fatalf("command scope=%+v, want %+v", command.Scope, wantScope)
	}

	catalog := manager.FeatureCatalog()
	entry, ok := catalog.Get(pluginID)
	if !ok {
		t.Fatal("FeatureSpec is not visible")
	}
	if entry.Owner.Scope != wantScope {
		t.Fatalf("FeatureSpec scope=%+v, want %+v", entry.Owner.Scope, wantScope)
	}
	for kind, id := range map[feature.InteractionKind]string{
		feature.InteractionScreen:   "home",
		feature.InteractionAction:   "next",
		feature.InteractionInline:   "lookup",
		feature.InteractionDeepLink: "launch",
	} {
		if _, ok := catalog.FindInteraction(pluginID, kind, id); !ok {
			t.Fatalf("%s interaction %q is not visible", kind, id)
		}
	}

	inline, ok := inlineRegistry.ResolveOwned("p8hlookup value")
	if !ok {
		t.Fatal("inline surface is not visible")
	}
	if inline.Scope != wantScope {
		t.Fatalf("inline scope=%+v, want %+v", inline.Scope, wantScope)
	}
	version := inlineservice.HandlerVersion(inline.Handler)
	if version == "" {
		t.Fatal("inline handler version is empty")
	}
	return version
}

func TestP8HCrossSurfaceUnloadReloadGenerationAcceptance(t *testing.T) {
	ctx := context.Background()
	router := core.NewRouter(".")
	inlineRegistry := inlineservice.NewRegistry()
	engine := newP8HTaskEngine(t)
	manager := NewManager(router)
	manager.SetInlineRegistry(inlineRegistry)
	manager.SetTaskClient(engine)

	plugin := &p8hCrossSurfacePlugin{}
	if err := manager.RegisterWithContext(ctx, plugin); err != nil {
		t.Fatalf("RegisterWithContext() error=%v", err)
	}
	t.Cleanup(func() {
		if manager.IsEnabled(plugin.Name()) {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := manager.ShutdownWithContext(shutdownCtx); err != nil {
				t.Errorf("ShutdownWithContext() error=%v", err)
			}
		}
	})

	firstPluginScope, ok := manager.Scope(plugin.Name())
	if !ok {
		t.Fatal("initial plugin scope is unavailable")
	}
	firstScope := tasks.ScopeIdentity{Owner: firstPluginScope.Owner(), Generation: firstPluginScope.Generation()}
	firstInlineVersion := assertP8HSurfaces(t, manager, router, inlineRegistry, plugin.Name(), firstScope)

	deepRouter, _, deepToken := newP8HDeepLink(t, manager, plugin.Name())
	oldDeepPrepared, err := deepRouter.Prepare(ctx, deepToken.ID, 7)
	if err != nil {
		t.Fatalf("Prepare(initial deep-link) error=%v", err)
	}
	if oldDeepPrepared.Scope() != firstScope {
		t.Fatalf("deep-link scope=%+v, want %+v", oldDeepPrepared.Scope(), firstScope)
	}

	runtime := manager.InteractionRuntime()
	actions := manager.ActionDispatcher()
	firstSession, err := runtime.Create(ctx, interaction.CreateRequest{
		FeatureID: plugin.Name(),
		Binding: interaction.Binding{
			ActorID:   7,
			ChatID:    70,
			MessageID: 700,
		},
		State: []byte("first"),
	})
	if err != nil {
		t.Fatalf("Create(first interaction) error=%v", err)
	}
	if firstSession.Session.Scope != firstScope {
		t.Fatalf("interaction scope=%+v, want %+v", firstSession.Session.Scope, firstScope)
	}
	preInputCallback, err := runtime.CallbackData(ctx, firstSession.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData(pre-input) error=%v", err)
	}
	armed, err := runtime.ArmInput(ctx, firstSession.Session.ID, interaction.InputRequest{
		ExpectedRevision: firstSession.Session.Revision,
		State:            []byte("pending"),
		TTL:              time.Minute,
	})
	if err != nil {
		t.Fatalf("ArmInput(first) error=%v", err)
	}
	if _, err := runtime.ResolveCallback(ctx, preInputCallback, firstSession.Session.Binding); !errors.Is(err, interaction.ErrStaleToken) {
		t.Fatalf("ResolveCallback(pre-input token) error=%v, want %v", err, interaction.ErrStaleToken)
	}
	oldCallback, err := runtime.CallbackData(ctx, firstSession.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData(first) error=%v", err)
	}
	oldActionCalls := 0
	oldActionRegistration, err := actions.Register(firstScope, plugin.Name(), "next", func(context.Context, interaction.Action) error {
		oldActionCalls++
		return nil
	})
	if err != nil {
		t.Fatalf("Register(first action) error=%v", err)
	}
	defer oldActionRegistration.Close()
	oldPreparedAction, err := actions.Prepare(ctx, oldCallback, firstSession.Session.Binding)
	if err != nil {
		t.Fatalf("Prepare(first action) error=%v", err)
	}

	oldTaskClient := plugin.taskClient
	if oldTaskClient == nil {
		t.Fatal("plugin did not receive scoped TaskEngine client")
	}
	taskStarted := make(chan struct{})
	oldTask, err := oldTaskClient.Submit(ctx, tasks.WorkSpec{
		ID:        "p8h-old-running",
		Pool:      "interactive",
		Class:     tasks.PriorityInteractive,
		Resources: []tasks.ResourceRequirement{{Name: "download", Amount: 1}},
		Handler: func(runCtx context.Context) error {
			close(taskStarted)
			<-runCtx.Done()
			return runCtx.Err()
		},
	})
	if err != nil {
		t.Fatalf("Submit(old generation task) error=%v", err)
	}
	select {
	case <-taskStarted:
	case <-time.After(time.Second):
		t.Fatal("old generation task did not start")
	}
	if snap, ok := oldTaskClient.Snapshot("p8h-old-running"); !ok || snap.Scope != firstScope {
		t.Fatalf("old task snapshot=%+v ok=%v, want scope %+v", snap, ok, firstScope)
	}

	if armed.Revision <= firstSession.Session.Revision {
		t.Fatalf("ArmInput revision=%d did not advance from %d", armed.Revision, firstSession.Session.Revision)
	}
	if stats := runtime.Stats(); stats.Sessions != 1 || stats.Inputs != 1 {
		t.Fatalf("interaction stats before disable=%+v, want one session and input", stats)
	}

	if err := manager.Disable(ctx, plugin.Name()); err != nil {
		t.Fatalf("Disable() error=%v", err)
	}

	if _, ok := router.Find("p8hcmd"); ok {
		t.Fatal("disabled plugin left command visible")
	}
	if _, ok := inlineRegistry.ResolveOwned("p8hlookup value"); ok {
		t.Fatal("disabled plugin left inline surface visible")
	}
	if _, ok := manager.FeatureCatalog().Get(plugin.Name()); ok {
		t.Fatal("disabled plugin left FeatureSpec visible")
	}
	if !errors.Is(context.Cause(firstSession.Context), interaction.ErrScopeStale) {
		t.Fatalf("first interaction cause=%v, want %v", context.Cause(firstSession.Context), interaction.ErrScopeStale)
	}
	if stats := runtime.Stats(); stats.Sessions != 0 || stats.Inputs != 0 {
		t.Fatalf("interaction stats after disable=%+v, want empty runtime", stats)
	}
	if _, handled, err := runtime.TakeInput(ctx, 7, 70); err != nil || handled {
		t.Fatalf("TakeInput(after disable) handled=%v err=%v, want false/nil", handled, err)
	}
	if _, err := runtime.ResolveCallback(ctx, oldCallback, firstSession.Session.Binding); err == nil {
		t.Fatal("old a2 token resolved after disable")
	}
	if err := oldPreparedAction.Dispatch(ctx); err == nil {
		t.Fatal("prepared old-generation action executed after disable")
	}
	if oldActionCalls != 0 {
		t.Fatalf("old action calls=%d, want 0", oldActionCalls)
	}
	if _, err := deepRouter.Prepare(ctx, deepToken.ID, 7); !errors.Is(err, savedresponse.ErrResolverUnavailable) {
		t.Fatalf("Prepare(deep-link while disabled) error=%v, want %v", err, savedresponse.ErrResolverUnavailable)
	}

	waitCtx, waitCancel := context.WithTimeout(ctx, time.Second)
	oldResult, waitErr := oldTask.Wait(waitCtx)
	waitCancel()
	if waitErr != nil {
		t.Fatalf("Wait(old task after disable) error=%v", waitErr)
	}
	if oldResult.Outcome != tasks.OutcomeCancelled || oldResult.Cause != tasks.CauseScopeClosed {
		t.Fatalf("old task result=%+v, want cancelled/scope_closed", oldResult)
	}
	if _, err := oldTaskClient.Submit(ctx, tasks.WorkSpec{
		ID:      "p8h-old-late",
		Pool:    "interactive",
		Class:   tasks.PriorityInteractive,
		Handler: func(context.Context) error { return nil },
	}); !errors.Is(err, tasks.ErrScopeClosed) {
		t.Fatalf("old scoped client submission error=%v, want %v", err, tasks.ErrScopeClosed)
	}

	if err := manager.Enable(ctx, plugin.Name()); err != nil {
		t.Fatalf("Enable() error=%v", err)
	}
	secondPluginScope, ok := manager.Scope(plugin.Name())
	if !ok {
		t.Fatal("re-enabled plugin scope is unavailable")
	}
	secondScope := tasks.ScopeIdentity{Owner: secondPluginScope.Owner(), Generation: secondPluginScope.Generation()}
	if secondScope == firstScope {
		t.Fatalf("re-enable reused scope %+v", secondScope)
	}
	secondInlineVersion := assertP8HSurfaces(t, manager, router, inlineRegistry, plugin.Name(), secondScope)
	if secondInlineVersion == firstInlineVersion {
		t.Fatalf("inline generation version did not change: %q", secondInlineVersion)
	}

	if _, err := runtime.ResolveCallback(ctx, oldCallback, firstSession.Session.Binding); err == nil {
		t.Fatal("old a2 token revived after re-enable")
	}
	if err := oldPreparedAction.Dispatch(ctx); err == nil {
		t.Fatal("old prepared action revived after re-enable")
	}
	if err := deepRouter.ExecutePrepared(ctx, oldDeepPrepared, deeplink.Delivery{
		ActorID:  7,
		ChatID:   70,
		SendText: func(string) error { return nil },
	}); !errors.Is(err, savedresponse.ErrBindingStale) {
		t.Fatalf("ExecutePrepared(old deep-link lease) error=%v, want %v", err, savedresponse.ErrBindingStale)
	}

	freshDeepPrepared, err := deepRouter.Prepare(ctx, deepToken.ID, 7)
	if err != nil {
		t.Fatalf("Prepare(deep-link after re-enable) error=%v", err)
	}
	if freshDeepPrepared.Scope() != secondScope {
		t.Fatalf("fresh deep-link scope=%+v, want %+v", freshDeepPrepared.Scope(), secondScope)
	}
	var delivered string
	if err := deepRouter.ExecutePrepared(ctx, freshDeepPrepared, deeplink.Delivery{
		ActorID: 7,
		ChatID:  70,
		SendText: func(text string) error {
			delivered = text
			return nil
		},
	}); err != nil {
		t.Fatalf("ExecutePrepared(fresh deep-link) error=%v", err)
	}
	if delivered != "generation-bound response" {
		t.Fatalf("fresh deep-link delivered %q", delivered)
	}

	secondSession, err := runtime.Create(ctx, interaction.CreateRequest{
		FeatureID: plugin.Name(),
		Binding: interaction.Binding{
			ActorID:   7,
			ChatID:    70,
			MessageID: 701,
		},
		State: []byte("second"),
	})
	if err != nil {
		t.Fatalf("Create(second interaction) error=%v", err)
	}
	if secondSession.Session.Scope != secondScope {
		t.Fatalf("second interaction scope=%+v, want %+v", secondSession.Session.Scope, secondScope)
	}
	secondCallback, err := runtime.CallbackData(ctx, secondSession.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData(second) error=%v", err)
	}
	newActionCalls := 0
	newActionRegistration, err := actions.Register(secondScope, plugin.Name(), "next", func(context.Context, interaction.Action) error {
		newActionCalls++
		return nil
	})
	if err != nil {
		t.Fatalf("Register(second action) error=%v", err)
	}
	defer newActionRegistration.Close()
	if err := actions.Dispatch(ctx, secondCallback, secondSession.Session.Binding); err != nil {
		t.Fatalf("Dispatch(second action) error=%v", err)
	}
	if newActionCalls != 1 {
		t.Fatalf("new action calls=%d, want 1", newActionCalls)
	}

	armedSecond, err := runtime.ArmInput(ctx, secondSession.Session.ID, interaction.InputRequest{
		ExpectedRevision: secondSession.Session.Revision,
		State:            []byte("new pending"),
		TTL:              time.Minute,
	})
	if err != nil {
		t.Fatalf("ArmInput(second) error=%v", err)
	}
	taken, handled, err := runtime.TakeInput(ctx, 7, 70)
	if err != nil || !handled {
		t.Fatalf("TakeInput(second) handled=%v err=%v", handled, err)
	}
	if taken.Session.ID != secondSession.Session.ID || taken.Session.Revision <= armedSecond.Revision {
		t.Fatalf("taken input session=%+v armed=%+v", taken.Session, armedSecond)
	}

	newTaskClient := plugin.taskClient
	if newTaskClient == nil || newTaskClient == oldTaskClient {
		t.Fatal("re-enable did not replace the scoped TaskEngine client")
	}
	newTask, err := newTaskClient.Submit(ctx, tasks.WorkSpec{
		ID:        "p8h-new-running",
		Pool:      "interactive",
		Class:     tasks.PriorityInteractive,
		Resources: []tasks.ResourceRequirement{{Name: "download", Amount: 1}},
		Handler:   func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("Submit(new generation task) error=%v", err)
	}
	newWaitCtx, newWaitCancel := context.WithTimeout(ctx, time.Second)
	newResult, err := newTask.Wait(newWaitCtx)
	newWaitCancel()
	if err != nil || !newResult.IsSuccess() {
		t.Fatalf("new generation task result=%+v err=%v", newResult, err)
	}
	if snap, ok := newTaskClient.Snapshot("p8h-new-running"); !ok || snap.Scope != secondScope {
		t.Fatalf("new task snapshot=%+v ok=%v, want scope %+v", snap, ok, secondScope)
	}
}
