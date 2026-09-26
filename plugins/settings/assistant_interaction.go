package settings

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
)

const assistantSettingsScreenDashboard = "assistant_dashboard"

type assistantRuntimeState struct {
	mu      sync.RWMutex
	runtime assistantinteraction.DriverRuntime
}

func (p *Plugin) AssistantFeatureID() string { return p.Name() }

func assistantSettingsSlotID(slot int) string {
	return fmt.Sprintf("assistant_slot_%02d", slot)
}

func (p *Plugin) BindAssistant(rt assistantinteraction.DriverRuntime) (func(), error) {
	if p == nil || rt.Engine == nil || rt.Catalog == nil || rt.Admit == nil {
		return nil, orchestration.ErrInvalidEngine
	}
	scope, ok := rt.Catalog.FeatureScope(p.Name())
	if !ok || scope.IsZero() {
		return nil, fmt.Errorf("settings: assistant feature scope unavailable")
	}

	registrations := make([]interface{ Close() }, 0, nativeSettingsActionSlotCount)
	for i := 0; i < nativeSettingsActionSlotCount; i++ {
		slot := i
		actionID := assistantSettingsSlotID(slot)
		registration, err := rt.Engine.RegisterAction(scope, p.Name(), actionID, func(ctx *orchestration.Context) error {
			if ctx == nil {
				return orchestration.ErrInvalidEngine
			}
			session := ctx.Session()
			if err := rt.Admit(p.Name(), feature.InteractionAction, actionID, session.Binding.ActorID, ctx.Target()); err != nil {
				return err
			}
			return p.handleNativeSettingsSlot(ctx, slot)
		})
		if err != nil {
			for j := len(registrations) - 1; j >= 0; j-- {
				registrations[j].Close()
			}
			return nil, fmt.Errorf("settings: register assistant action %s: %w", actionID, err)
		}
		registrations = append(registrations, registration)
	}

	p.assistant.mu.Lock()
	p.assistant.runtime = rt
	p.assistant.mu.Unlock()

	return func() {
		for i := len(registrations) - 1; i >= 0; i-- {
			registrations[i].Close()
		}
		p.assistant.mu.Lock()
		if p.assistant.runtime.Engine == rt.Engine {
			p.assistant.runtime = assistantinteraction.DriverRuntime{}
		}
		p.assistant.mu.Unlock()
	}, nil
}

func (p *Plugin) currentAssistantRuntime() assistantinteraction.DriverRuntime {
	if p == nil {
		return assistantinteraction.DriverRuntime{}
	}
	p.assistant.mu.RLock()
	rt := p.assistant.runtime
	p.assistant.mu.RUnlock()
	return rt
}

func (p *Plugin) openAssistantSettings(cmd *core.Context, state MenuState) error {
	if p == nil || cmd == nil || cmd.PeerID == nil || cmd.SenderID() == 0 {
		return fmt.Errorf("settings: assistant command target unavailable")
	}
	rt := p.currentAssistantRuntime()
	if rt.Engine == nil || rt.Admit == nil {
		return fmt.Errorf("settings: assistant runtime unavailable")
	}
	chatID := cmd.ChatID()
	if chatID == 0 {
		chatID = cmd.SenderID()
	}
	target := presentationtelegram.MessageTarget{Peer: cmd.PeerID, ChatID: chatID}
	if err := rt.Admit(
		p.Name(),
		feature.InteractionScreen,
		assistantSettingsScreenDashboard,
		cmd.SenderID(),
		target,
	); err != nil {
		return err
	}
	raw, view, err := p.assistantSettingsView(cmd.Ctx, state)
	if err != nil {
		return err
	}
	_, err = rt.Engine.Begin(cmd.Ctx, orchestration.BeginRequest{
		FeatureID: p.Name(),
		ActorID:   cmd.SenderID(),
		State:     raw,
		TTL:       nativeSettingsTTL,
		Target:    target,
		View:      view,
	})
	return err
}

func (p *Plugin) assistantSettingsView(ctx context.Context, state MenuState) ([]byte, presentation.View, error) {
	raw, view, err := p.nativeSettingsView(ctx, state)
	if err != nil {
		return nil, presentation.View{}, err
	}
	for rowIndex := range view.Rows {
		for buttonIndex := range view.Rows[rowIndex] {
			actionID := view.Rows[rowIndex][buttonIndex].ActionID
			if !strings.HasPrefix(actionID, "slot_") {
				return nil, presentation.View{}, fmt.Errorf("settings: unexpected shared action id %q", actionID)
			}
			slot, err := strconv.Atoi(strings.TrimPrefix(actionID, "slot_"))
			if err != nil || slot < 0 || slot >= nativeSettingsActionSlotCount {
				return nil, presentation.View{}, fmt.Errorf("settings: invalid shared action id %q", actionID)
			}
			view.Rows[rowIndex][buttonIndex].ActionID = assistantSettingsSlotID(slot)
		}
	}
	return raw, view, nil
}

func (*Plugin) HandleAssistantInput(*orchestration.Context, string) error {
	return fmt.Errorf("settings: assistant free-form input unsupported")
}

var _ assistantinteraction.FeatureDriver = (*Plugin)(nil)
