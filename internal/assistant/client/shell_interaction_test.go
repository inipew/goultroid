package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"go.uber.org/zap"
)

type shellTestPort struct {
	sent      presentation.CompiledView
	edited    presentation.CompiledView
	answered  presentation.Answer
	peer      tg.InputPeerClass
	editErr   error
	deleteErr error
	deleted   bool
}

func (p *shellTestPort) Send(_ context.Context, target presentation.Target, view presentation.CompiledView) (presentation.Target, error) {
	p.sent = view
	messageTarget, _ := target.(presentationtelegram.MessageTarget)
	p.peer = messageTarget.Peer
	messageTarget.MessageID = 77
	return messageTarget, nil
}
func (p *shellTestPort) Edit(_ context.Context, _ presentation.Target, view presentation.CompiledView) error {
	p.edited = view
	return p.editErr
}
func (p *shellTestPort) Answer(_ context.Context, answer presentation.Answer) error {
	p.answered = answer
	return nil
}

func (p *shellTestPort) Delete(_ context.Context, _ presentation.Target) error {
	if p.deleteErr != nil {
		return p.deleteErr
	}
	p.deleted = true
	return nil
}

func callbackForAction(t *testing.T, view presentation.CompiledView, actionID string) []byte {
	t.Helper()
	for _, row := range view.Rows {
		for _, button := range row {
			token, err := rootinteraction.ParseCallbackToken(button.Data)
			if err != nil {
				t.Fatalf("ParseCallbackToken(%q) error = %v", button.Data, err)
			}
			if token.ActionID == actionID {
				return append([]byte(nil), button.Data...)
			}
		}
	}
	t.Fatalf("action %q not found in compiled view", actionID)
	return nil
}

func newShellEngine(t *testing.T) (*plugin.Manager, *AssistantClient, *shellTestPort, *orchestration.Engine) {
	t.Helper()
	manager := plugin.NewManager(core.NewRouter("."))
	if err := manager.Register(assistantshell.NewFeature()); err != nil {
		t.Fatalf("Register(shell) error = %v", err)
	}
	port := &shellTestPort{}
	engine, err := orchestration.New(manager.InteractionRuntime(), manager.ActionDispatcher(), port)
	if err != nil {
		manager.Shutdown()
		t.Fatalf("orchestration.New() error = %v", err)
	}
	client := NewAssistantClient(1, "hash", "token", zap.NewNop())
	client.SetOwner(7, nil)
	client.SetInteractionFoundation(manager.FeatureCatalog(), manager.InteractionRuntime(), manager.ActionDispatcher())
	if err := client.ensureShellActions(engine, manager.FeatureCatalog()); err != nil {
		manager.Shutdown()
		t.Fatalf("ensureShellActions() error = %v", err)
	}
	return manager, client, port, engine
}

func beginShell(t *testing.T, engine *orchestration.Engine, port *shellTestPort, peer tg.InputPeerClass) {
	t.Helper()
	_, err := engine.Begin(context.Background(), orchestration.BeginRequest{
		FeatureID: assistantshell.FeatureID,
		ActorID:   7,
		State:     assistantshell.InitialState(),
		Target:    presentationtelegram.MessageTarget{Peer: peer, ChatID: 7},
		View:      assistantshell.HomeView(assistantshell.HomeModel{Username: "TestBot"}),
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
}

func dispatchShell(t *testing.T, engine *orchestration.Engine, data []byte, queryID int64, peer tg.InputPeerClass) error {
	t.Helper()
	return engine.Dispatch(context.Background(), orchestration.CallbackRequest{
		Data: data, ActorID: 7, QueryID: queryID,
		Target: presentationtelegram.MessageTarget{Peer: peer, ChatID: 7, MessageID: 77},
	})
}

func TestAssistantShellRefreshStalesOldButtonAndReloadsGeneration(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()
	catalog := manager.FeatureCatalog()
	peer := &tg.InputPeerUser{UserID: 7}

	beginShell(t, engine, port, peer)
	oldRefresh := callbackForAction(t, port.sent, assistantshell.ActionRefresh)
	if err := dispatchShell(t, engine, oldRefresh, 100, peer); err != nil {
		t.Fatalf("Dispatch(refresh) error = %v", err)
	}
	if assistantshell.RefreshCount([]byte{1}) != 0 {
		t.Fatal("invalid shell state unexpectedly decoded")
	}
	if len(port.edited.Rows) == 0 {
		t.Fatal("refresh did not render a transitioned view")
	}
	if err := dispatchShell(t, engine, oldRefresh, 101, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("old button error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}

	before, ok := catalog.Get(assistantshell.FeatureID)
	if !ok {
		t.Fatal("shell feature missing before reload")
	}
	newRevisionButton := callbackForAction(t, port.edited, assistantshell.ActionRefresh)
	if err := manager.Disable(context.Background(), assistantshell.FeatureID); err != nil {
		t.Fatalf("Disable(shell) error = %v", err)
	}
	if err := dispatchShell(t, engine, newRevisionButton, 102, peer); err == nil {
		t.Fatal("callback from disabled generation unexpectedly executed")
	}
	if err := manager.Enable(context.Background(), assistantshell.FeatureID); err != nil {
		t.Fatalf("Enable(shell) error = %v", err)
	}
	after, ok := catalog.Get(assistantshell.FeatureID)
	if !ok {
		t.Fatal("shell feature missing after reload")
	}
	if after.Owner.Scope.Generation == before.Owner.Scope.Generation {
		t.Fatalf("generation = %d, want replacement generation", after.Owner.Scope.Generation)
	}
	if err := client.ensureShellActions(engine, catalog); err != nil {
		t.Fatalf("ensureShellActions(reload) error = %v", err)
	}
	beginShell(t, engine, port, peer)
	newRefresh := callbackForAction(t, port.sent, assistantshell.ActionRefresh)
	if err := dispatchShell(t, engine, newRefresh, 103, peer); err != nil {
		t.Fatalf("Dispatch(new generation) error = %v", err)
	}
}

func TestAssistantShellReadOnlyNavigationUsesOneRevisionFencedSession(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	router := core.NewRouter(".")
	if err := router.RegisterBatch([]core.Command{
		{Name: "alive", Category: "System", Surfaces: execution.SurfaceAssistant, Handler: func(*core.Context) error { return nil }},
		{Name: "download", Category: "Media", Surfaces: execution.SurfaceAssistant, Handler: func(*core.Context) error { return nil }},
		{Name: "hidden", Category: "Hidden", Surfaces: execution.SurfaceUserbot, Handler: func(*core.Context) error { return nil }},
	}); err != nil {
		t.Fatalf("RegisterBatch() error = %v", err)
	}
	client.SetCoreRouter(router)

	peer := &tg.InputPeerUser{UserID: 7}
	beginShell(t, engine, port, peer)
	statusFromHome := callbackForAction(t, port.sent, assistantshell.ActionStatus)
	if err := dispatchShell(t, engine, statusFromHome, 200, peer); err != nil {
		t.Fatalf("Dispatch(status) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "System Status") {
		t.Fatalf("status view not rendered: %q", port.edited.Text)
	}
	if err := dispatchShell(t, engine, statusFromHome, 201, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("pre-navigation status token error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}

	statusRefresh := callbackForAction(t, port.edited, assistantshell.ActionStatusRefresh)
	if err := dispatchShell(t, engine, statusRefresh, 202, peer); err != nil {
		t.Fatalf("Dispatch(status refresh) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Session refreshes") {
		t.Fatalf("status refresh did not preserve state: %q", port.edited.Text)
	}

	homeFromStatus := callbackForAction(t, port.edited, assistantshell.ActionHome)
	if err := dispatchShell(t, engine, homeFromStatus, 203, peer); err != nil {
		t.Fatalf("Dispatch(home) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "GoUltroid Assistant") {
		t.Fatalf("home view not rendered: %q", port.edited.Text)
	}

	helpFromHome := callbackForAction(t, port.edited, assistantshell.ActionHelp)
	if err := dispatchShell(t, engine, helpFromHome, 204, peer); err != nil {
		t.Fatalf("Dispatch(help) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Command Browser") || !strings.Contains(port.edited.Text, "Media") {
		t.Fatalf("help view missing canonical command navigator: %q", port.edited.Text)
	}
	if strings.Contains(port.edited.Text, "Hidden") {
		t.Fatalf("help view leaked userbot-only command category: %q", port.edited.Text)
	}

	homeFromHelp := callbackForAction(t, port.edited, assistantshell.ActionHome)
	if err := dispatchShell(t, engine, homeFromHelp, 205, peer); err != nil {
		t.Fatalf("Dispatch(home from help) error = %v", err)
	}
	if got := manager.InteractionRuntime().Stats().Sessions; got != 1 {
		t.Fatalf("navigation sessions = %d, want 1", got)
	}
}

func TestAssistantShellActionAdmissionTracksOwnerChanges(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()
	peer := &tg.InputPeerUser{UserID: 7}
	beginShell(t, engine, port, peer)
	status := callbackForAction(t, port.sent, assistantshell.ActionStatus)

	client.SetOwner(8, nil)
	if err := dispatchShell(t, engine, status, 300, peer); !errors.Is(err, ErrShellAdmission) {
		t.Fatalf("Dispatch(after owner change) error = %v, want %v", err, ErrShellAdmission)
	}
}

func TestAssistantShellOwnerStartUsesA2Canary(t *testing.T) {
	manager := plugin.NewManager(core.NewRouter("."))
	if err := manager.Register(assistantshell.NewFeature()); err != nil {
		t.Fatalf("Register(shell) error = %v", err)
	}
	defer manager.Shutdown()

	port := &shellTestPort{}
	engine, err := orchestration.New(manager.InteractionRuntime(), manager.ActionDispatcher(), port)
	if err != nil {
		t.Fatalf("orchestration.New() error = %v", err)
	}
	client := NewAssistantClient(1, "hash", "token", zap.NewNop())
	client.SetOwner(7, nil)
	audience, audienceRepo := newAssistantAudienceRegistry(t)
	client.SetAudienceRegistry(audience)
	client.mu.Lock()
	client.featureCatalog = manager.FeatureCatalog()
	client.interactionIngress = &interactionIngress{engine: engine}
	client.mu.Unlock()

	err = client.dispatchStart(&command.Context{
		Ctx:      context.Background(),
		SenderID: 7,
		Peer:     &tg.InputPeerUser{UserID: 7},
	})
	if err != nil {
		t.Fatalf("dispatchStart(owner) error = %v", err)
	}
	if len(port.sent.Rows) == 0 || len(port.sent.Rows[0]) == 0 {
		t.Fatal("owner/private start did not render the a2 shell")
	}
	if !isInteractionCallback(port.sent.Rows[0][0].Data) {
		t.Fatalf("owner/private callback data = %q, want a2", port.sent.Rows[0][0].Data)
	}
	if got := manager.InteractionRuntime().Stats().Sessions; got != 1 {
		t.Fatalf("live shell sessions = %d, want 1", got)
	}
	member, err := audienceRepo.GetAudience(context.Background(), 7)
	if err != nil {
		t.Fatalf("owner start audience missing: %v", err)
	}
	if member.Sources != pmrelay.AudienceSourceStart {
		t.Fatalf("owner start audience sources=%d, want start", member.Sources)
	}
}

func TestAssistantShellVisitorStartUsesPublicReadOnlyPath(t *testing.T) {
	manager := plugin.NewManager(core.NewRouter("."))
	if err := manager.Register(assistantshell.NewFeature()); err != nil {
		t.Fatalf("Register(shell) error = %v", err)
	}
	defer manager.Shutdown()

	port := &shellTestPort{}
	engine, err := orchestration.New(manager.InteractionRuntime(), manager.ActionDispatcher(), port)
	if err != nil {
		t.Fatalf("orchestration.New() error = %v", err)
	}
	client := NewAssistantClient(1, "hash", "token", zap.NewNop())
	client.SetOwner(7, nil)
	audience, audienceRepo := newAssistantAudienceRegistry(t)
	client.SetAudienceRegistry(audience)
	public := &publicStartInteraction{}
	client.mu.Lock()
	client.featureCatalog = manager.FeatureCatalog()
	client.interactionIngress = &interactionIngress{engine: engine}
	client.mu.Unlock()

	err = client.dispatchStart(&command.Context{
		Ctx:         context.Background(),
		SenderID:    99,
		Peer:        &tg.InputPeerUser{UserID: 99},
		Interaction: public,
	})
	if err != nil {
		t.Fatalf("dispatchStart(visitor) error = %v", err)
	}
	if !strings.Contains(public.sent, "Assistant endpoint is online") {
		t.Fatalf("public start text = %q", public.sent)
	}
	if got := manager.InteractionRuntime().Stats().Sessions; got != 0 {
		t.Fatalf("visitor created a2 session = %d, want 0", got)
	}
	member, err := audienceRepo.GetAudience(context.Background(), 99)
	if err != nil {
		t.Fatalf("visitor start audience missing: %v", err)
	}
	if member.Sources != pmrelay.AudienceSourceStart {
		t.Fatalf("visitor start audience sources=%d, want start", member.Sources)
	}
}

func TestAssistantShellUnavailableUsesStaticRecoveryResponse(t *testing.T) {
	client := NewAssistantClient(1, "hash", "token", zap.NewNop())
	client.SetOwner(7, nil)
	public := &publicStartInteraction{}
	if err := client.dispatchStart(&command.Context{
		Ctx:         context.Background(),
		SenderID:    7,
		Peer:        &tg.InputPeerUser{UserID: 7},
		Interaction: public,
	}); err != nil {
		t.Fatalf("dispatchStart(unavailable) error = %v", err)
	}
	if !strings.Contains(public.sent, "temporarily unavailable") {
		t.Fatalf("recovery response = %q", public.sent)
	}
}

func TestShouldUseStartRecoveryKeepsRuntimeBoundsFailClosed(t *testing.T) {
	if !shouldUseStartRecovery(ErrShellUnavailable) {
		t.Fatal("unavailable shell must remain eligible for static recovery")
	}
	if shouldUseStartRecovery(ErrShellAdmission) {
		t.Fatal("normal admission denial must use public start, not static recovery")
	}
	if shouldUseStartRecovery(rootinteraction.ErrCapacity) {
		t.Fatal("session capacity exhaustion must not bypass P1 bounds")
	}
	if shouldUseStartRecovery(rootinteraction.ErrClosed) {
		t.Fatal("closed interaction runtime must not reopen work through static recovery")
	}
}
