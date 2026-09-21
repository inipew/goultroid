package client

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"go.uber.org/zap"
)

type shellTestPort struct {
	sent     presentation.CompiledView
	edited   presentation.CompiledView
	answered presentation.Answer
	peer     tg.InputPeerClass
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
	return nil
}
func (p *shellTestPort) Answer(_ context.Context, answer presentation.Answer) error {
	p.answered = answer
	return nil
}

func TestAssistantShellRefreshStalesOldButtonAndReloadsGeneration(t *testing.T) {
	manager := plugin.NewManager(core.NewRouter("."))
	if err := manager.Register(assistantshell.NewFeature()); err != nil {
		t.Fatalf("Register(shell) error = %v", err)
	}
	defer manager.Shutdown()

	catalog := manager.FeatureCatalog()
	sessions := manager.InteractionRuntime()
	actions := manager.ActionDispatcher()
	port := &shellTestPort{}
	engine, err := orchestration.New(sessions, actions, port)
	if err != nil {
		t.Fatalf("orchestration.New() error = %v", err)
	}
	client := NewAssistantClient(1, "hash", "token", zap.NewNop())
	client.SetOwner(7, nil)
	if err := client.ensureShellActions(engine, catalog); err != nil {
		t.Fatalf("ensureShellActions() error = %v", err)
	}

	peer := &tg.InputPeerUser{UserID: 7}
	begin := func() []byte {
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
		return append([]byte(nil), port.sent.Rows[0][0].Data...)
	}

	oldRefresh := begin()
	if err := engine.Dispatch(context.Background(), orchestration.CallbackRequest{
		Data: oldRefresh, ActorID: 7, QueryID: 100,
		Target: presentationtelegram.MessageTarget{Peer: peer, ChatID: 7, MessageID: 77},
	}); err != nil {
		t.Fatalf("Dispatch(refresh) error = %v", err)
	}
	if assistantshell.RefreshCount([]byte{1}) != 0 {
		t.Fatal("invalid shell state unexpectedly decoded")
	}
	if len(port.edited.Rows) == 0 {
		t.Fatal("refresh did not render a transitioned view")
	}
	if err := engine.Dispatch(context.Background(), orchestration.CallbackRequest{
		Data: oldRefresh, ActorID: 7, QueryID: 101,
		Target: presentationtelegram.MessageTarget{Peer: peer, ChatID: 7, MessageID: 77},
	}); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("old button error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}

	before, ok := catalog.Get(assistantshell.FeatureID)
	if !ok {
		t.Fatal("shell feature missing before reload")
	}
	newRevisionButton := append([]byte(nil), port.edited.Rows[0][0].Data...)
	if err := manager.Disable(context.Background(), assistantshell.FeatureID); err != nil {
		t.Fatalf("Disable(shell) error = %v", err)
	}
	if err := engine.Dispatch(context.Background(), orchestration.CallbackRequest{
		Data: newRevisionButton, ActorID: 7, QueryID: 102,
		Target: presentationtelegram.MessageTarget{Peer: peer, ChatID: 7, MessageID: 77},
	}); err == nil {
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
	newRefresh := begin()
	if err := engine.Dispatch(context.Background(), orchestration.CallbackRequest{
		Data: newRefresh, ActorID: 7, QueryID: 103,
		Target: presentationtelegram.MessageTarget{Peer: peer, ChatID: 7, MessageID: 77},
	}); err != nil {
		t.Fatalf("Dispatch(new generation) error = %v", err)
	}
}

func TestAssistantShellAdmissionFallsBackToLegacyStart(t *testing.T) {
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
	client.mu.Lock()
	client.v2Catalog = manager.FeatureCatalog()
	client.v2Ingress = &v2Ingress{engine: engine, ack: &syntheticAck{}}
	legacyCalled := false
	client.legacyStart = func(*command.Context) error {
		legacyCalled = true
		return nil
	}
	client.mu.Unlock()

	err = client.dispatchStart(&command.Context{
		Ctx:      context.Background(),
		SenderID: 99,
		Peer:     &tg.InputPeerUser{UserID: 99},
	})
	if err != nil {
		t.Fatalf("dispatchStart(visitor) error = %v", err)
	}
	if !legacyCalled {
		t.Fatal("visitor was not routed to legacy compatibility start")
	}
	if len(port.sent.Rows) != 0 {
		t.Fatal("visitor unexpectedly created an a2 shell session")
	}
}

func TestShouldFallbackStartKeepsRuntimeBoundsFailClosed(t *testing.T) {
	if !shouldFallbackStart(ErrShellUnavailable) || !shouldFallbackStart(ErrShellAdmission) {
		t.Fatal("compatibility conditions must remain eligible for legacy fallback")
	}
	if shouldFallbackStart(rootinteraction.ErrCapacity) {
		t.Fatal("session capacity exhaustion must not bypass P1 bounds")
	}
	if shouldFallbackStart(rootinteraction.ErrClosed) {
		t.Fatal("closed interaction runtime must not reopen work through legacy fallback")
	}
}
