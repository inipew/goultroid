package client

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"go.uber.org/zap"
)

const (
	generationDriverFeatureID = "generation_driver_test"
	generationDriverActionID  = "advance"
)

type generationDriverPlugin struct {
	dispatches int
}

func (*generationDriverPlugin) Name() string { return generationDriverFeatureID }

func (*generationDriverPlugin) Commands() []core.Command { return nil }

func (*generationDriverPlugin) Init() error { return nil }

func (*generationDriverPlugin) FeatureSpec() feature.Spec {
	return feature.Spec{
		ID:   generationDriverFeatureID,
		Name: "Generation Driver Test",
		Interactions: []feature.Interaction{{
			ID:          generationDriverActionID,
			Kind:        feature.InteractionAction,
			Description: "generation-bound test action",
			Surfaces:    execution.SurfaceAssistant,
			Policy:      feature.OwnerPolicy(execution.SurfaceAssistant),
		}},
	}
}

func (*generationDriverPlugin) AssistantFeatureID() string { return generationDriverFeatureID }

func (p *generationDriverPlugin) BindAssistant(rt assistantinteraction.DriverRuntime) (func(), error) {
	scope, ok := rt.Catalog.FeatureScope(generationDriverFeatureID)
	if !ok || scope.IsZero() {
		return nil, rootinteraction.ErrScopeStale
	}
	registration, err := rt.Engine.RegisterAction(
		scope,
		generationDriverFeatureID,
		generationDriverActionID,
		func(*orchestration.Context) error {
			p.dispatches++
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	return registration.Close, nil
}

func (*generationDriverPlugin) HandleAssistantInput(*orchestration.Context, string) error { return nil }

func beginGenerationDriverSession(t *testing.T, engine *orchestration.Engine, port *shellTestPort) []byte {
	t.Helper()
	peer := &tg.InputPeerUser{UserID: 7}
	_, err := engine.Begin(context.Background(), orchestration.BeginRequest{
		FeatureID: generationDriverFeatureID,
		ActorID:   7,
		Target:    presentationtelegram.MessageTarget{Peer: peer, ChatID: 7},
		View: presentation.View{
			Text: "generation driver",
			Rows: []presentation.Row{{
				{Text: "Advance", ActionID: generationDriverActionID},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Begin(generation driver) error = %v", err)
	}
	return callbackForAction(t, port.sent, generationDriverActionID)
}

func dispatchGenerationDriver(engine *orchestration.Engine, data []byte, queryID int64) error {
	return engine.Dispatch(context.Background(), orchestration.CallbackRequest{
		Data:    data,
		ActorID: 7,
		QueryID: queryID,
		Target: presentationtelegram.MessageTarget{
			Peer:      &tg.InputPeerUser{UserID: 7},
			ChatID:    7,
			MessageID: 77,
		},
	})
}

func TestRefreshInteractionBindingsTracksPluginGeneration(t *testing.T) {
	ctx := context.Background()
	manager := plugin.NewManager(core.NewRouter("."))
	driver := &generationDriverPlugin{}
	if err := manager.RegisterWithContext(ctx, driver); err != nil {
		t.Fatalf("RegisterWithContext(driver) error = %v", err)
	}
	defer manager.Shutdown()

	port := &shellTestPort{}
	engine, err := orchestration.New(manager.InteractionRuntime(), manager.ActionDispatcher(), port)
	if err != nil {
		t.Fatalf("orchestration.New() error = %v", err)
	}

	client := NewAssistantClient(1, "hash", "token", zap.NewNop())
	client.SetOwner(7, nil)
	client.SetInteractionDrivers([]assistantinteraction.FeatureDriver{driver})
	client.SetInteractionFoundation(manager.FeatureCatalog(), manager.InteractionRuntime(), manager.ActionDispatcher())
	client.interactionIngress = &interactionIngress{
		engine: engine,
		ack:    newInteractionPresentationServicer(nil),
	}

	if err := client.RefreshInteractionBindings(); err != nil {
		t.Fatalf("RefreshInteractionBindings(initial) error = %v", err)
	}
	manager.SetRegistrationValidator(func(context.Context) error {
		return client.RefreshInteractionBindings()
	})

	firstScope, ok := manager.FeatureCatalog().FeatureScope(generationDriverFeatureID)
	if !ok {
		t.Fatal("driver scope missing before reload")
	}
	oldCallback := beginGenerationDriverSession(t, engine, port)
	if err := dispatchGenerationDriver(engine, oldCallback, 1); err != nil {
		t.Fatalf("Dispatch(initial) error = %v", err)
	}
	if driver.dispatches != 1 {
		t.Fatalf("initial dispatches = %d, want 1", driver.dispatches)
	}

	staleCallback := beginGenerationDriverSession(t, engine, port)
	if err := manager.Disable(ctx, generationDriverFeatureID); err != nil {
		t.Fatalf("Disable(driver) error = %v", err)
	}
	if err := client.RefreshInteractionBindings(); err != nil {
		t.Fatalf("RefreshInteractionBindings(disabled) error = %v", err)
	}
	if err := dispatchGenerationDriver(engine, staleCallback, 2); err == nil {
		t.Fatal("disabled-generation callback unexpectedly executed")
	}

	if err := manager.Enable(ctx, generationDriverFeatureID); err != nil {
		t.Fatalf("Enable(driver) error = %v", err)
	}
	secondScope, ok := manager.FeatureCatalog().FeatureScope(generationDriverFeatureID)
	if !ok {
		t.Fatal("driver scope missing after reload")
	}
	if secondScope == firstScope {
		t.Fatalf("driver scope after reload = %+v, want new generation", secondScope)
	}

	freshCallback := beginGenerationDriverSession(t, engine, port)
	if err := dispatchGenerationDriver(engine, freshCallback, 3); err != nil {
		t.Fatalf("Dispatch(reloaded) error = %v", err)
	}
	if driver.dispatches != 2 {
		t.Fatalf("reloaded dispatches = %d, want 2", driver.dispatches)
	}
	if err := dispatchGenerationDriver(engine, staleCallback, 4); err == nil || errors.Is(err, rootinteraction.ErrHandlerUnavailable) {
		t.Fatalf("stale callback after reload error = %v, want stale session/token rejection", err)
	}
}
