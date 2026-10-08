package pmpermit

import (
	"context"
	"fmt"
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
	nativePMPermitScreen  = "dashboard"
	nativePMPermitRefresh = "refresh"
	nativePMPermitToggle  = "toggle"
	nativePMPermitClose   = "close"
	nativePMPermitTTL     = 10 * time.Minute
)

type nativeRuntimeState struct {
	mu      sync.RWMutex
	runtime nativeinteraction.DriverRuntime
}

func (p *Plugin) FeatureSpec() feature.Spec {
	policy := feature.OwnerPolicy(execution.SurfaceUserbot)
	return feature.Spec{
		ID:                p.Name(),
		Name:              "PM Permit",
		Description:       p.Description(),
		Category:          "Security",
		DurabilityVersion: "1",
		Interactions: []feature.Interaction{
			{ID: nativePMPermitScreen, Kind: feature.InteractionScreen, Description: "Owner PM Permit dashboard", Surfaces: execution.SurfaceUserbot, Policy: policy},
			{ID: nativePMPermitRefresh, Kind: feature.InteractionAction, Description: "Refresh PM Permit state", Surfaces: execution.SurfaceUserbot, Policy: policy},
			{ID: nativePMPermitToggle, Kind: feature.InteractionAction, Description: "Toggle PM Permit protection", Surfaces: execution.SurfaceUserbot, Policy: policy},
			{ID: nativePMPermitClose, Kind: feature.InteractionAction, Description: "Close PM Permit dashboard", Surfaces: execution.SurfaceUserbot, Policy: policy},
		},
	}
}

func (p *Plugin) NativeFeatureID() string { return p.Name() }

// PMPermit's text dashboard and commands remain available without a2.
func (*Plugin) NativeOptional() bool { return true }

func (p *Plugin) BindNative(rt nativeinteraction.DriverRuntime) (func(), error) {
	if p == nil || p.svc == nil || rt.Interactions == nil || rt.Catalog == nil || rt.Scope.IsZero() {
		return nil, nativeinteraction.ErrUnavailable
	}
	scope, ok := rt.Catalog.FeatureScope(p.Name())
	if !ok || scope != rt.Scope {
		return nil, fmt.Errorf("pmpermit: native feature scope unavailable")
	}
	handlers := []struct {
		id      string
		handler orchestration.Handler
	}{
		{nativePMPermitRefresh, p.handleNativePMPermitRefresh},
		{nativePMPermitToggle, p.handleNativePMPermitToggle},
		{nativePMPermitClose, p.handleNativePMPermitClose},
	}
	registrations := make([]interface{ Close() }, 0, len(handlers))
	for _, entry := range handlers {
		reg, err := rt.Interactions.RegisterAction(rt.Scope, p.Name(), entry.id, entry.handler)
		if err != nil {
			for i := len(registrations) - 1; i >= 0; i-- {
				registrations[i].Close()
			}
			return nil, fmt.Errorf("pmpermit: register native %s: %w", entry.id, err)
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

func (p *Plugin) currentNativeRuntime() nativeinteraction.DriverRuntime {
	if p == nil {
		return nativeinteraction.DriverRuntime{}
	}
	p.native.mu.RLock()
	rt := p.native.runtime
	p.native.mu.RUnlock()
	return rt
}

// openNativePMPermit returns opened=false when the native a2 transport is not
// installed. Text commands retain their existing fallback in that case.
func (p *Plugin) openNativePMPermit(cmd *core.Context) (opened bool, err error) {
	rt := p.currentNativeRuntime()
	if rt.Interactions == nil || rt.Scope.IsZero() {
		return false, nil
	}
	if cmd == nil || cmd.SenderID() != p.svc.OwnerID() {
		return true, nativeinteraction.ErrInvalidInvocation
	}
	view, err := p.nativePMPermitView(cmd.Ctx)
	if err != nil {
		return true, err
	}
	_, err = rt.Interactions.Begin(cmd, nativeinteraction.BeginRequest{
		FeatureID: p.Name(),
		ScreenID:  nativePMPermitScreen,
		State:     []byte{1},
		TTL:       nativePMPermitTTL,
		View:      view,
	})
	return true, err
}

func (p *Plugin) nativePMPermitView(ctx context.Context) (presentation.View, error) {
	if p == nil || p.svc == nil {
		return presentation.View{}, fmt.Errorf("pmpermit: service unavailable")
	}
	pending, approved, blocked, err := p.svc.GetStats(ctx)
	if err != nil {
		return presentation.View{}, fmt.Errorf("pmpermit: read dashboard: %w", err)
	}
	status, label := "🔴 OFF", "🛡️ Enable"
	if p.svc.IsEnabled() {
		status, label = "🟢 ON", "⏸ Disable"
	}
	return presentation.View{
		Text: fmt.Sprintf("🛡️ <b>PM Permit</b>\n\nStatus: %s\nMax warnings: <code>%d</code>\nCooldown: <code>%s</code>\n\nApproved: <code>%d</code>\nPending: <code>%d</code>\nBlocked: <code>%d</code>", status, p.svc.MaxWarns(), p.svc.WarnCooldown(), approved, pending, blocked),
		Rows: []presentation.Row{
			{{Text: label, ActionID: nativePMPermitToggle}, {Text: "🔄 Refresh", ActionID: nativePMPermitRefresh}},
			{{Text: "✖ Close", ActionID: nativePMPermitClose}},
		},
	}, nil
}

func (p *Plugin) validNativePMPermitActor(ctx *orchestration.Context) bool {
	return p != nil && p.svc != nil && ctx != nil &&
		ctx.Session().Binding.ActorID == p.svc.OwnerID() &&
		len(ctx.State()) == 1 && ctx.State()[0] == 1
}

func (p *Plugin) handleNativePMPermitRefresh(ctx *orchestration.Context) error {
	if !p.validNativePMPermitActor(ctx) {
		return ctx.Answer("Sesi PM Permit tidak valid.", true)
	}
	view, err := p.nativePMPermitView(ctx.Context())
	if err != nil {
		return ctx.Answer("Tidak dapat memuat status PM Permit.", true)
	}
	return ctx.Transition([]byte{1}, nativePMPermitTTL, view)
}

func (p *Plugin) handleNativePMPermitToggle(ctx *orchestration.Context) error {
	if !p.validNativePMPermitActor(ctx) {
		return ctx.Answer("Sesi PM Permit tidak valid.", true)
	}
	actor := ctx.Session().Binding.ActorID
	if err := p.setEnabledForActor(ctx.Context(), actor, !p.svc.IsEnabled()); err != nil {
		return ctx.Answer("Gagal memperbarui PM Permit. Silakan coba lagi.", true)
	}
	view, err := p.nativePMPermitView(ctx.Context())
	if err != nil {
		return ctx.Answer("Status diubah, tetapi pembacaan dashboard gagal. Buka ulang .pmpermit.", true)
	}
	return ctx.Transition([]byte{1}, nativePMPermitTTL, view)
}

func (p *Plugin) handleNativePMPermitClose(ctx *orchestration.Context) error {
	if !p.validNativePMPermitActor(ctx) {
		return ctx.Answer("Sesi PM Permit tidak valid.", true)
	}
	return ctx.Terminate(presentation.View{Text: "🛡️ Dashboard PM Permit ditutup."})
}

var _ nativeinteraction.FeatureDriver = (*Plugin)(nil)
