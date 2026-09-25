package client

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	_ "modernc.org/sqlite"
)

func newP5SettingsService(t *testing.T) *settings.Service {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open P5 settings database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })

	repo := settings.NewSQLiteRepository(db)
	if err := repo.InitSchema(context.Background()); err != nil {
		t.Fatalf("initialize P5 settings schema: %v", err)
	}
	registry := settings.NewRegistry()
	if err := registry.Register(settings.SettingDefinition{
		Namespace:    "core",
		Key:          "prefix",
		Title:        "Command Prefix",
		Description:  "Prefix used for userbot commands",
		Category:     settings.CategoryGeneral,
		Type:         settings.TypeString,
		DefaultValue: ".",
	}); err != nil {
		t.Fatalf("register P5 setting: %v", err)
	}
	return settings.NewService(repo, registry, nil)
}

func newP5TaskEngine(t *testing.T) *taskengine.Engine {
	t.Helper()
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"interactive": {
				Concurrency:    2,
				MinConcurrency: 0,
				ZeroIdle:       true,
				IdleTimeout:    20 * time.Millisecond,
				BacklogLimit:   32,
				PayloadBudget:  1 << 20,
			},
		},
		ResultCapacity: 64,
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("start P5 TaskEngine: %v", err)
	}
	return engine
}

func waitP5WorkersRetired(t *testing.T, engine *taskengine.Engine) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		stats, err := engine.Stats(context.Background())
		if err != nil {
			t.Fatalf("P5 TaskEngine stats: %v", err)
		}
		allZero := true
		for _, pool := range stats.Pools {
			if pool.Workers != 0 || pool.Running != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("P5 TaskEngine workers did not retire: %+v", stats.Pools)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestP5FinalAssistantUXLifecycleAcceptance(t *testing.T) {
	ctx := context.Background()
	manager, client, port, interactionEngine := newShellEngine(t)
	managerStopped := false
	t.Cleanup(func() {
		if !managerStopped {
			_ = manager.Shutdown()
		}
	})

	taskRuntime := newP5TaskEngine(t)
	taskStopped := false
	t.Cleanup(func() {
		if !taskStopped {
			stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = taskRuntime.Stop(stopCtx)
		}
	})
	manager.SetTaskClient(taskRuntime)
	client.SetTasks(taskRuntime)

	settingsSvc := newP5SettingsService(t)
	client.SetSettingsService(settingsSvc)

	router := core.NewRouter(".")
	if err := router.RegisterBatch([]core.Command{
		{
			Name:        "alive",
			Description: "Read-only status",
			Category:    "System",
			Surfaces:    execution.SurfaceAssistant,
			Handler:     func(*core.Context) error { return nil },
		},
		{
			Name:        "download",
			Description: "Media workflow",
			Category:    "Media",
			Surfaces:    execution.SurfaceAssistant,
			Handler:     func(*core.Context) error { return nil },
		},
	}); err != nil {
		t.Fatalf("register P5 shell commands: %v", err)
	}
	client.SetCoreRouter(router)

	peer := &tg.InputPeerUser{UserID: 7}
	beginShell(t, interactionEngine, port, peer)
	if stats := manager.InteractionRuntime().Stats(); stats.Sessions != 1 || stats.Inputs != 0 {
		t.Fatalf("P5 initial shell stats=%+v, want one session and no input", stats)
	}

	view := port.sent
	helpToken := callbackForAction(t, view, assistantshell.ActionHelp)
	if err := dispatchShell(t, interactionEngine, helpToken, 1000, peer); err != nil {
		t.Fatalf("P5 Home -> Help: %v", err)
	}
	view = port.edited
	moduleToken := callbackForAction(t, view, assistantshell.HelpModuleSlotActionIDs()[0])
	if err := dispatchShell(t, interactionEngine, moduleToken, 1001, peer); err != nil {
		t.Fatalf("P5 Help -> Module: %v", err)
	}
	view = port.edited
	commandToken := callbackForAction(t, view, assistantshell.HelpCommandSlotActionIDs()[0])
	if err := dispatchShell(t, interactionEngine, commandToken, 1002, peer); err != nil {
		t.Fatalf("P5 Module -> Command: %v", err)
	}
	view = port.edited
	backToken := callbackForAction(t, view, assistantshell.ActionHelpBack)
	if err := dispatchShell(t, interactionEngine, backToken, 1003, peer); err != nil {
		t.Fatalf("P5 Command -> Back: %v", err)
	}
	view = port.edited
	homeToken := callbackForAction(t, view, assistantshell.ActionHome)
	if err := dispatchShell(t, interactionEngine, homeToken, 1004, peer); err != nil {
		t.Fatalf("P5 Module -> Home: %v", err)
	}
	view = port.edited
	if err := dispatchShell(t, interactionEngine, helpToken, 1005, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("P5 stale pre-navigation token error=%v, want %v", err, rootinteraction.ErrStaleToken)
	}

	settingsToken := callbackForAction(t, view, assistantshell.ActionSettings)
	if err := dispatchShell(t, interactionEngine, settingsToken, 1010, peer); err != nil {
		t.Fatalf("P5 Home -> Settings: %v", err)
	}
	view = port.edited
	categoryToken := callbackForAction(t, view, assistantshell.SettingsCategorySlotActionIDs()[0])
	if err := dispatchShell(t, interactionEngine, categoryToken, 1011, peer); err != nil {
		t.Fatalf("P5 Settings -> Category: %v", err)
	}
	view = port.edited
	settingToken := callbackForAction(t, view, assistantshell.SettingSlotActionIDs()[0])
	if err := dispatchShell(t, interactionEngine, settingToken, 1012, peer); err != nil {
		t.Fatalf("P5 Category -> Detail: %v", err)
	}
	view = port.edited
	inputToken := callbackForAction(t, view, assistantshell.ActionSettingInput)
	if err := dispatchShell(t, interactionEngine, inputToken, 1013, peer); err != nil {
		t.Fatalf("P5 Detail -> Input: %v", err)
	}
	if stats := manager.InteractionRuntime().Stats(); stats.Sessions != 1 || stats.Inputs != 1 {
		t.Fatalf("P5 armed input stats=%+v, want one session/input", stats)
	}
	if err := dispatchShell(t, interactionEngine, inputToken, 1014, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("P5 stale input opener error=%v, want %v", err, rootinteraction.ErrStaleToken)
	}

	client.interactionIngress.input = client.handleInteractionTextInput
	handled, err := client.interactionIngress.tryText(ctx, "!", 7, 7, peer)
	if err != nil || !handled {
		t.Fatalf("P5 TakeInput handled=%v err=%v", handled, err)
	}
	if stats := manager.InteractionRuntime().Stats(); stats.Inputs != 0 || stats.Sessions != 1 {
		t.Fatalf("P5 post-input stats=%+v, want session retained and input cleared", stats)
	}
	if got, err := settingsSvc.Resolve(ctx, 7, 7, "core", "prefix"); err != nil || got != "!" {
		t.Fatalf("P5 persisted setting=%q err=%v, want !", got, err)
	}

	view = port.edited
	inputToken = callbackForAction(t, view, assistantshell.ActionSettingInput)
	if err := dispatchShell(t, interactionEngine, inputToken, 1015, peer); err != nil {
		t.Fatalf("P5 reopen input: %v", err)
	}
	view = port.edited
	cancelInput := callbackForAction(t, view, assistantshell.ActionSettingInputCancel)
	if err := dispatchShell(t, interactionEngine, cancelInput, 1016, peer); err != nil {
		t.Fatalf("P5 cancel input: %v", err)
	}
	if stats := manager.InteractionRuntime().Stats(); stats.Inputs != 0 {
		t.Fatalf("P5 cancelled input survived: %+v", stats)
	}

	view = port.edited
	inputToken = callbackForAction(t, view, assistantshell.ActionSettingInput)
	if err := dispatchShell(t, interactionEngine, inputToken, 1017, peer); err != nil {
		t.Fatalf("P5 reopen input before Close: %v", err)
	}
	view = port.edited
	closeInput := callbackForAction(t, view, assistantshell.ActionClose)
	if err := dispatchShell(t, interactionEngine, closeInput, 1018, peer); err != nil {
		t.Fatalf("P5 Close while input armed: %v", err)
	}
	if stats := manager.InteractionRuntime().Stats(); stats.Sessions != 0 || stats.Inputs != 0 || stats.StateBytes != 0 {
		t.Fatalf("P5 Close retained interaction state: %+v", stats)
	}
	if err := dispatchShell(t, interactionEngine, closeInput, 1019, peer); !errors.Is(err, rootinteraction.ErrNotFound) {
		t.Fatalf("P5 terminal callback error=%v, want %v", err, rootinteraction.ErrNotFound)
	}

	beginShell(t, interactionEngine, port, peer)
	view = port.sent
	pingToken := callbackForAction(t, view, assistantshell.ActionPing)
	for i := 0; i < 512; i++ {
		if err := dispatchShell(t, interactionEngine, pingToken, int64(2000+i), peer); err != nil {
			t.Fatalf("P5 callback burst %d: %v", i, err)
		}
	}
	if err := interactionEngine.Dispatch(ctx, orchestration.CallbackRequest{
		Data:    pingToken,
		ActorID: 8,
		QueryID: 2600,
		Target: presentationtelegram.MessageTarget{
			Peer:      peer,
			ChatID:    7,
			MessageID: 77,
		},
	}); err == nil {
		t.Fatal("P5 wrong-actor callback unexpectedly executed")
	}

	refreshToken := callbackForAction(t, view, assistantshell.ActionRefresh)
	if err := dispatchShell(t, interactionEngine, refreshToken, 2601, peer); err != nil {
		t.Fatalf("P5 refresh before reload: %v", err)
	}
	if err := dispatchShell(t, interactionEngine, pingToken, 2602, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("P5 old burst token error=%v, want %v", err, rootinteraction.ErrStaleToken)
	}
	view = port.edited
	oldGenerationToken := callbackForAction(t, view, assistantshell.ActionPing)

	oldScope, ok := manager.FeatureCatalog().FeatureScope(assistantshell.FeatureID)
	if !ok || oldScope.IsZero() {
		t.Fatal("P5 shell scope missing before disable")
	}
	taskStarted := make(chan struct{})
	ticket, err := taskRuntime.Submit(ctx, tasks.WorkSpec{
		ID:         "p5-shell-active",
		Scope:      oldScope,
		QuotaOwner: "plugin:assistant-shell",
		Pool:       "interactive",
		Class:      tasks.PriorityInteractive,
		Handler: func(runCtx context.Context) error {
			close(taskStarted)
			<-runCtx.Done()
			return runCtx.Err()
		},
	})
	if err != nil {
		t.Fatalf("P5 submit active shell task: %v", err)
	}
	select {
	case <-taskStarted:
	case <-time.After(time.Second):
		t.Fatal("P5 active shell task did not start")
	}

	if err := manager.Disable(ctx, assistantshell.FeatureID); err != nil {
		t.Fatalf("P5 disable shell: %v", err)
	}
	if stats := manager.InteractionRuntime().Stats(); stats.Sessions != 0 || stats.Inputs != 0 {
		t.Fatalf("P5 disabled shell retained sessions: %+v", stats)
	}
	if err := dispatchShell(t, interactionEngine, oldGenerationToken, 2603, peer); err == nil {
		t.Fatal("P5 disabled generation callback unexpectedly executed")
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, time.Second)
	result, waitErr := ticket.Wait(waitCtx)
	waitCancel()
	if waitErr != nil {
		t.Fatalf("P5 wait disabled-scope task: %v", waitErr)
	}
	if result.Outcome != tasks.OutcomeCancelled || result.Cause != tasks.CauseScopeClosed {
		t.Fatalf("P5 disabled-scope task result=%+v", result)
	}

	if err := manager.Enable(ctx, assistantshell.FeatureID); err != nil {
		t.Fatalf("P5 re-enable shell: %v", err)
	}
	newScope, ok := manager.FeatureCatalog().FeatureScope(assistantshell.FeatureID)
	if !ok || newScope.IsZero() || newScope == oldScope {
		t.Fatalf("P5 reloaded scope=%+v old=%+v ok=%v", newScope, oldScope, ok)
	}
	if err := client.ensureShellActions(interactionEngine, manager.FeatureCatalog()); err != nil {
		t.Fatalf("P5 bind reloaded shell actions: %v", err)
	}
	beginShell(t, interactionEngine, port, peer)
	view = port.sent
	freshPing := callbackForAction(t, view, assistantshell.ActionPing)
	if err := dispatchShell(t, interactionEngine, freshPing, 2700, peer); err != nil {
		t.Fatalf("P5 fresh generation callback: %v", err)
	}
	freshClose := callbackForAction(t, view, assistantshell.ActionClose)
	if err := dispatchShell(t, interactionEngine, freshClose, 2701, peer); err != nil {
		t.Fatalf("P5 close reloaded shell: %v", err)
	}

	waitP5WorkersRetired(t, taskRuntime)
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, time.Second)
	if err := manager.ShutdownWithContext(shutdownCtx); err != nil {
		shutdownCancel()
		t.Fatalf("P5 plugin manager shutdown: %v", err)
	}
	shutdownCancel()
	managerStopped = true

	stopCtx, stopCancel := context.WithTimeout(ctx, time.Second)
	if err := taskRuntime.Stop(stopCtx); err != nil {
		stopCancel()
		t.Fatalf("P5 TaskEngine stop: %v", err)
	}
	stopCancel()
	taskStopped = true

	if stats := manager.InteractionRuntime().Stats(); stats.Sessions != 0 || stats.Inputs != 0 || stats.StateBytes != 0 {
		t.Fatalf("P5 final interaction stats=%+v, want zero logical state", stats)
	}
}
