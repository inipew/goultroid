package command_test

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/presentation"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
	appPresentation "github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/deeplink"
	deeplinkSqlite "github.com/inipew/goultroid/internal/services/deeplink/sqlite"
	"go.uber.org/zap"
	_ "modernc.org/sqlite"
)

func TestEndToEnd_UserbotToAssistant_Handoff(t *testing.T) {
	ctx := context.Background()

	// 1. Setup real SQLite database and schema
	dbPath := filepath.Join(t.TempDir(), "e2e_handoff.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if err := deeplinkSqlite.InitSchema(ctx, db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	// 2. Setup deeplink service with real SQLite repository
	deeplinkRepo := deeplinkSqlite.NewRepository(db)
	deeplinkSvc := deeplink.NewService(deeplinkRepo, "GoUltroidBot", nil)

	// 3. Setup presentation registry and service
	presRegistry := appPresentation.NewRegistry()
	evaluator := appPresentation.NewEvaluator(100, nil)
	presSvc := appPresentation.NewService(presRegistry, evaluator)

	helpKey := appPresentation.ScreenKey{Namespace: "core", Name: "help", Version: 1}
	_, err = presRegistry.Register(appPresentation.Registration{
		Owner:      "core",
		Generation: 1,
		Builder: &mockScreenBuilder{
			key: helpKey,
		},
		Policy: appPresentation.PublicPolicy(),
	})
	if err != nil {
		t.Fatalf("register screen: %v", err)
	}

	// 4. Setup handoff service
	handoffSvc := appPresentation.NewHandoffService(presSvc, deeplinkSvc)

	// 5. Userbot initiates handoff for user 12345
	actor := execution.NewActor(12345, -1001, false, false)
	handoffRes, err := handoffSvc.Handoff(ctx, appPresentation.HandoffRequest{
		Screen:        helpKey,
		Actor:         actor,
		Source:        execution.SourceUserbot,
		ChatType:      appPresentation.ChatTypeGroup,
		PreferredMode: appPresentation.HandoffDeepLink,
	})
	if err != nil {
		t.Fatalf("handoff failed: %v", err)
	}
	if handoffRes.Mode != appPresentation.HandoffDeepLink {
		t.Fatalf("expected mode DeepLink, got: %s", handoffRes.Mode)
	}
	if !strings.HasPrefix(handoffRes.DeepLinkURL, "https://t.me/GoUltroidBot?start=") {
		t.Fatalf("unexpected deep-link URL: %s", handoffRes.DeepLinkURL)
	}

	// Extract token from URL
	u, err := url.Parse(handoffRes.DeepLinkURL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	token := u.Query().Get("start")
	if token == "" {
		t.Fatalf("empty start token in URL: %s", handoffRes.DeepLinkURL)
	}

	// 6. Setup assistant router
	r := command.NewRouter(zap.NewNop())
	startTime := time.Now().Add(-10 * time.Minute)
	command.AttachDefaultCommands(r, func() string { return "GoUltroidBot" }, func() time.Time { return startTime }, presentation.RenderScreen)
	r.SetPresentation(presSvc)
	r.SetDeepLinks(deeplinkSvc)

	fake := &fakeInteraction{}
	peer := &tg.InputPeerUser{UserID: 12345}

	// 7. Attacker (user 99999) tries to consume victim's token -> rejected, token not burned
	attackerFake := &fakeInteraction{}
	attackerPeer := &tg.InputPeerUser{UserID: 99999}
	_ = r.Dispatch(ctx, 99999, attackerPeer, "/start "+token, attackerFake)
	if !strings.Contains(attackerFake.lastSentText, "not intended for your account") {
		t.Errorf("expected unauthorized alert for attacker, got: %s", attackerFake.lastSentText)
	}

	// 8. Legitimate user 12345 sends /start <token> to assistant
	err = r.Dispatch(ctx, 12345, peer, "/start "+token, fake)
	if err != nil {
		t.Fatalf("dispatch start token: %v", err)
	}
	if !strings.Contains(fake.lastSentText, "Custom Screen Content") {
		t.Errorf("expected custom screen content delivered to user, got: %s", fake.lastSentText)
	}

	// 9. Legitimate user sends /start <token> again -> token already consumed
	repeatFake := &fakeInteraction{}
	_ = r.Dispatch(ctx, 12345, peer, "/start "+token, repeatFake)
	if !strings.Contains(repeatFake.lastSentText, "was already used") {
		t.Errorf("expected already used alert, got: %s", repeatFake.lastSentText)
	}
}

type dummyLifecyclePlugin struct {
	name string
}

func (p *dummyLifecyclePlugin) Name() string             { return p.name }
func (p *dummyLifecyclePlugin) Commands() []core.Command { return nil }
func (p *dummyLifecyclePlugin) Init() error              { return nil }
func (p *dummyLifecyclePlugin) Shutdown() error          { return nil }

func TestEndToEnd_PluginLifecycle_RevocationAndGenerationIsolation(t *testing.T) {
	ctx := context.Background()

	// 1. Setup real SQLite database and schema
	dbPath := filepath.Join(t.TempDir(), "e2e_lifecycle.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if err := deeplinkSqlite.InitSchema(ctx, db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	// 2. Setup deeplink service with real SQLite repository
	deeplinkRepo := deeplinkSqlite.NewRepository(db)
	deeplinkSvc := deeplink.NewService(deeplinkRepo, "GoUltroidBot", nil)

	// 3. Setup presentation registry and service
	presRegistry := appPresentation.NewRegistry()
	evaluator := appPresentation.NewEvaluator(100, nil)
	presSvc := appPresentation.NewService(presRegistry, evaluator)

	// 4. Setup plugin manager with revokers and active generation resolver
	coreRouter := core.NewRouter(".")
	pluginMgr := plugin.NewManager(coreRouter)
	pluginMgr.SetPresentationRevoker(presRegistry)
	pluginMgr.SetDeepLinkRevoker(deeplinkSvc)

	deeplinkSvc.SetActiveGenerationResolver(func(owner string) (uint64, bool) {
		scope, ok := pluginMgr.Scope(owner)
		if !ok {
			return 0, false
		}
		return scope.Generation(), true
	})

	// 5. Setup runtime and register plugin
	rt := &module.Runtime{
		CoreRuntime: module.CoreRuntime{
			Plugins: pluginMgr,
			Router:  coreRouter,
		},
		TelegramRuntime: module.TelegramRuntime{
			Presentation: presSvc,
		},
	}

	pluginName := "myfeature"
	dummy := &dummyLifecyclePlugin{name: pluginName}
	if err := rt.RegisterPlugin(ctx, module.Manifest{ID: pluginName, Version: "1.0.0"}, dummy); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	screenKey := appPresentation.ScreenKey{Namespace: pluginName, Name: "panel", Version: 1}
	_, err = rt.RegisterScreen(pluginName, appPresentation.Registration{
		Builder: &mockScreenBuilder{key: screenKey},
		Policy:  appPresentation.PublicPolicy(),
	})
	if err != nil {
		t.Fatalf("register screen: %v", err)
	}

	// Verify screen is registered with generation 1
	reg1, ok := presRegistry.Resolve(screenKey)
	if !ok {
		t.Fatal("expected screen to be registered")
	}
	if reg1.Owner != "plugin:"+pluginName {
		t.Fatalf("expected owner 'plugin:%s', got %s", pluginName, reg1.Owner)
	}
	gen1 := reg1.Generation
	if gen1 == 0 {
		t.Fatal("expected non-zero generation")
	}

	// 6. Userbot issues handoff deep link for generation 1
	handoffSvc := appPresentation.NewHandoffService(presSvc, deeplinkSvc)
	actor := execution.NewActor(12345, -1001, false, false)
	handoffRes1, err := handoffSvc.Handoff(ctx, appPresentation.HandoffRequest{
		Screen:        screenKey,
		Actor:         actor,
		Source:        execution.SourceUserbot,
		ChatType:      appPresentation.ChatTypeGroup,
		PreferredMode: appPresentation.HandoffDeepLink,
	})
	if err != nil {
		t.Fatalf("handoff failed: %v", err)
	}
	u1, err := url.Parse(handoffRes1.DeepLinkURL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	token1 := u1.Query().Get("start")
	if token1 == "" {
		t.Fatalf("missing start token: %s", handoffRes1.DeepLinkURL)
	}

	// 7. Setup assistant router
	r := command.NewRouter(zap.NewNop())
	startTime := time.Now().Add(-10 * time.Minute)
	command.AttachDefaultCommands(r, func() string { return "GoUltroidBot" }, func() time.Time { return startTime }, presentation.RenderScreen)
	r.SetPresentation(presSvc)
	r.SetDeepLinks(deeplinkSvc)

	// 8. Disable plugin -> screen must be revoked, token1 must not be consumable
	if err := pluginMgr.Disable(ctx, pluginName); err != nil {
		t.Fatalf("disable plugin: %v", err)
	}

	// Registry must no longer have the screen
	if _, ok := presRegistry.Resolve(screenKey); ok {
		t.Fatal("screen remained in registry after plugin was disabled")
	}

	// Attempting to consume token1 while disabled must fail
	fakeDisabled := &fakeInteraction{}
	peer := &tg.InputPeerUser{UserID: 12345}
	_ = r.Dispatch(ctx, 12345, peer, "/start "+token1, fakeDisabled)
	if !strings.Contains(fakeDisabled.lastSentText, "This link has expired") && !strings.Contains(fakeDisabled.lastSentText, "unavailable") {
		t.Errorf("expected invalid/expired alert on disabled plugin token, got: %s", fakeDisabled.lastSentText)
	}

	// 9. Re-enable plugin -> new generation
	if err := pluginMgr.Enable(ctx, pluginName); err != nil {
		t.Fatalf("enable plugin: %v", err)
	}
	// Re-register screen under new generation
	_, err = rt.RegisterScreen(pluginName, appPresentation.Registration{
		Builder: &mockScreenBuilder{key: screenKey},
		Policy:  appPresentation.PublicPolicy(),
	})
	if err != nil {
		t.Fatalf("re-register screen: %v", err)
	}

	reg2, ok := presRegistry.Resolve(screenKey)
	if !ok {
		t.Fatal("screen not registered under new generation")
	}
	if reg2.Generation <= gen1 {
		t.Fatalf("expected generation > %d, got %d", gen1, reg2.Generation)
	}

	// 10. Attempting to consume the OLD token1 (from generation 1) must still fail!
	fakeStale := &fakeInteraction{}
	_ = r.Dispatch(ctx, 12345, peer, "/start "+token1, fakeStale)
	if !strings.Contains(fakeStale.lastSentText, "This link has expired") && !strings.Contains(fakeStale.lastSentText, "unavailable") {
		t.Errorf("expected stale token rejection, got: %s", fakeStale.lastSentText)
	}

	// 11. Issuing a NEW token under current generation must succeed!
	handoffRes2, err := handoffSvc.Handoff(ctx, appPresentation.HandoffRequest{
		Screen:        screenKey,
		Actor:         actor,
		Source:        execution.SourceUserbot,
		ChatType:      appPresentation.ChatTypeGroup,
		PreferredMode: appPresentation.HandoffDeepLink,
	})
	if err != nil {
		t.Fatalf("handoff generation 2 failed: %v", err)
	}
	u2, err := url.Parse(handoffRes2.DeepLinkURL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	token2 := u2.Query().Get("start")

	fakeFresh := &fakeInteraction{}
	if err := r.Dispatch(ctx, 12345, peer, "/start "+token2, fakeFresh); err != nil {
		t.Fatalf("dispatch fresh token: %v", err)
	}
	if !strings.Contains(fakeFresh.lastSentText, "Custom Screen Content") {
		t.Errorf("expected screen content delivered to user with fresh token, got: %s", fakeFresh.lastSentText)
	}
}
