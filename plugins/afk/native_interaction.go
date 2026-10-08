package afk

import (
	"context"
	"fmt"
	"html"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
)

const (
	nativeAFKScreen  = "dashboard"
	nativeAFKRefresh = "refresh"
	nativeAFKEnable  = "enable"
	nativeAFKDisable = "disable"
	nativeAFKClose   = "close"
	nativeAFKTTL     = 10 * time.Minute
)

type nativeAFKRuntimeState struct {
	mu      sync.RWMutex
	runtime nativeinteraction.DriverRuntime
}

func (p *Plugin) FeatureSpec() feature.Spec {
	policy := feature.OwnerPolicy(execution.SurfaceUserbot)
	return feature.Spec{
		ID: p.Name(), Name: "AFK", Description: p.Description(),
		Category: "Utility", DurabilityVersion: "1",
		Interactions: []feature.Interaction{
			{ID: nativeAFKScreen, Kind: feature.InteractionScreen, Description: "Owner AFK dashboard", Surfaces: execution.SurfaceUserbot, Policy: policy},
			{ID: nativeAFKRefresh, Kind: feature.InteractionAction, Description: "Refresh AFK status", Surfaces: execution.SurfaceUserbot, Policy: policy},
			{ID: nativeAFKEnable, Kind: feature.InteractionAction, Description: "Enable AFK", Surfaces: execution.SurfaceUserbot, Policy: policy},
			{ID: nativeAFKDisable, Kind: feature.InteractionAction, Description: "Disable AFK", Surfaces: execution.SurfaceUserbot, Policy: policy},
			{ID: nativeAFKClose, Kind: feature.InteractionAction, Description: "Close AFK menu", Surfaces: execution.SurfaceUserbot, Policy: policy},
		},
	}
}

func (p *Plugin) NativeFeatureID() string { return p.Name() }

func (p *Plugin) BindNative(rt nativeinteraction.DriverRuntime) (func(), error) {
	if p == nil || rt.Interactions == nil || rt.Catalog == nil || rt.Scope.IsZero() {
		return nil, nativeinteraction.ErrUnavailable
	}
	scope, ok := rt.Catalog.FeatureScope(p.Name())
	if !ok || scope != rt.Scope {
		return nil, fmt.Errorf("afk: native feature scope unavailable")
	}
	handlers := []struct {
		id      string
		handler orchestration.Handler
	}{
		{nativeAFKRefresh, p.handleNativeAFKRefresh},
		{nativeAFKEnable, p.handleNativeAFKEnable},
		{nativeAFKDisable, p.handleNativeAFKDisable},
		{nativeAFKClose, p.handleNativeAFKClose},
	}
	registrations := make([]interface{ Close() }, 0, len(handlers))
	for _, entry := range handlers {
		reg, err := rt.Interactions.RegisterAction(rt.Scope, p.Name(), entry.id, entry.handler)
		if err != nil {
			for i := len(registrations) - 1; i >= 0; i-- {
				registrations[i].Close()
			}
			return nil, fmt.Errorf("afk: register native %s: %w", entry.id, err)
		}
		registrations = append(registrations, reg)
	}
	p.native.mu.Lock()
	p.native.runtime = rt
	p.native.mu.Unlock()
	return func() {
		for i := len(registrations) - 1; i >= 0; i-- {
			registrations[i].Close()
		}
		p.native.mu.Lock()
		if p.native.runtime.Interactions == rt.Interactions && p.native.runtime.Scope == rt.Scope {
			p.native.runtime = nativeinteraction.DriverRuntime{}
		}
		p.native.mu.Unlock()
	}, nil
}

func (p *Plugin) currentNativeAFK() nativeinteraction.DriverRuntime {
	if p == nil {
		return nativeinteraction.DriverRuntime{}
	}
	p.native.mu.RLock()
	rt := p.native.runtime
	p.native.mu.RUnlock()
	return rt
}

func (p *Plugin) openNativeAFK(cmd *core.Context) (bool, error) {
	rt := p.currentNativeAFK()
	if rt.Interactions == nil || rt.Scope.IsZero() {
		return false, nil
	}
	if cmd == nil || p == nil || p.ownerID == 0 || cmd.SenderID() != p.ownerID {
		return true, nativeinteraction.ErrInvalidInvocation
	}
	_, err := rt.Interactions.Begin(cmd, nativeinteraction.BeginRequest{
		FeatureID: p.Name(), ScreenID: nativeAFKScreen,
		State: []byte{1}, TTL: nativeAFKTTL, View: p.nativeAFKView(),
	})
	return true, err
}

func (p *Plugin) nativeAFKView() presentation.View {
	status := "🟢 Inactive"
	detail := "Send <code>.afk reason</code> to set a custom reason."
	action := presentation.Button{Text: "🌙 Enable", ActionID: nativeAFKEnable}
	if st := p.state.Load(); st != nil && st.isAFK {
		status = "🌙 Active"
		reason := []rune(st.reason)
		if len(reason) > 180 {
			reason = append(reason[:180], []rune("…")...)
		}
		detail = fmt.Sprintf("Reason: %s\nElapsed: %s", html.EscapeString(string(reason)), formatDuration(time.Since(st.since)))
		action = presentation.Button{Text: "☀️ Disable", ActionID: nativeAFKDisable}
	}
	return presentation.View{
		Text: fmt.Sprintf("🌙 <b>AFK Manager</b>\n\nStatus: %s\n%s", status, detail),
		Rows: []presentation.Row{
			{action, {Text: "🔄 Refresh", ActionID: nativeAFKRefresh}},
			{{Text: "✖ Close", ActionID: nativeAFKClose}},
		},
	}
}

func (p *Plugin) validNativeAFKActor(ctx *orchestration.Context) bool {
	if p == nil || ctx == nil || p.ownerID == 0 {
		return false
	}
	state := ctx.State()
	return ctx.Session().Binding.ActorID == p.ownerID && len(state) == 1 && state[0] == 1
}

func (p *Plugin) handleNativeAFKRefresh(ctx *orchestration.Context) error {
	if !p.validNativeAFKActor(ctx) {
		return ctx.Answer("Sesi AFK tidak valid.", true)
	}
	return ctx.Transition([]byte{1}, nativeAFKTTL, p.nativeAFKView())
}

func (p *Plugin) handleNativeAFKEnable(ctx *orchestration.Context) error {
	if !p.validNativeAFKActor(ctx) {
		return ctx.Answer("Sesi AFK tidak valid.", true)
	}
	if err := p.enableAFK(ctx.Context(), "Away from keyboard"); err != nil {
		return ctx.Answer("Gagal mengaktifkan AFK. Coba lagi.", true)
	}
	return ctx.Transition([]byte{1}, nativeAFKTTL, p.nativeAFKView())
}

func (p *Plugin) handleNativeAFKDisable(ctx *orchestration.Context) error {
	if !p.validNativeAFKActor(ctx) {
		return ctx.Answer("Sesi AFK tidak valid.", true)
	}
	if _, _, err := p.disableAFK(ctx.Context()); err != nil {
		return ctx.Answer("Gagal menonaktifkan AFK. Coba lagi.", true)
	}
	return ctx.Transition([]byte{1}, nativeAFKTTL, p.nativeAFKView())
}

func (p *Plugin) handleNativeAFKClose(ctx *orchestration.Context) error {
	if !p.validNativeAFKActor(ctx) {
		return ctx.Answer("Sesi AFK tidak valid.", true)
	}
	return ctx.Terminate(presentation.View{Text: "🌙 Menu AFK ditutup."})
}

var _ nativeinteraction.FeatureDriver = (*Plugin)(nil)
