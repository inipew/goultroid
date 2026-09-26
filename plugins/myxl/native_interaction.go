package myxl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	nativeQuotaScreen        = "native_quota"
	nativeQuotaRefreshAction = "native_quota_refresh"
	nativeQuotaRefreshTTL    = 10 * time.Minute
	nativeQuotaRefreshExec   = 30 * time.Second
)

type nativeRuntimeState struct {
	mu      sync.RWMutex
	runtime nativeinteraction.DriverRuntime
}

func (p *Plugin) NativeFeatureID() string { return p.Name() }

func (p *Plugin) BindNative(rt nativeinteraction.DriverRuntime) (func(), error) {
	if p == nil || rt.Interactions == nil || rt.Catalog == nil || rt.Scope.IsZero() {
		return nil, nativeinteraction.ErrUnavailable
	}
	scope, ok := rt.Catalog.FeatureScope(p.Name())
	if !ok || scope != rt.Scope {
		return nil, fmt.Errorf("myxl: native feature scope unavailable")
	}

	registration, err := rt.Interactions.RegisterPreparedAction(
		rt.Scope,
		p.Name(),
		nativeQuotaRefreshAction,
		func(context.Context, rootinteraction.Action) (rootinteraction.ActionAdmission, error) {
			return rootinteraction.ActionAdmission{
				Scope: rt.Scope,
				Profile: tasks.ExecutionProfile{
					ExecutionTimeout: nativeQuotaRefreshExec,
				},
			}, nil
		},
		p.handleNativeQuotaRefresh,
	)
	if err != nil {
		return nil, fmt.Errorf("myxl: register native quota refresh: %w", err)
	}

	p.native.mu.Lock()
	p.native.runtime = rt
	p.native.mu.Unlock()

	return func() {
		registration.Close()
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

func (p *Plugin) nativeQuotaAvailable() bool {
	rt := p.currentNativeRuntime()
	return rt.Interactions != nil && !rt.Scope.IsZero()
}

func (p *Plugin) openNativeQuotaRefresh(cmd *core.Context, state quotaRefreshState, text string) (bool, error) {
	rt := p.currentNativeRuntime()
	if rt.Interactions == nil || rt.Scope.IsZero() {
		return false, nil
	}
	if cmd == nil || strings.TrimSpace(state.MSISDN) == "" {
		return true, nativeinteraction.ErrInvalidInvocation
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return true, fmt.Errorf("myxl: encode native quota state: %w", err)
	}
	_, err = rt.Interactions.Begin(cmd, nativeinteraction.BeginRequest{
		FeatureID: p.Name(),
		ScreenID:  nativeQuotaScreen,
		State:     raw,
		TTL:       nativeQuotaRefreshTTL,
		View:      nativeQuotaView(text),
	})
	return true, err
}

func (p *Plugin) handleNativeQuotaRefresh(ctx *orchestration.Context) error {
	if p == nil || ctx == nil {
		return orchestration.ErrInvalidEngine
	}
	state, err := decodeNativeQuotaState(ctx.State())
	if err != nil {
		return ctx.Answer("Tombol refresh tidak valid atau sudah kedaluwarsa.", true)
	}

	queryCtx, cancel := context.WithTimeout(ctx.Context(), 25*time.Second)
	defer cancel()

	acc, err := p.repo.GetByMSISDN(queryCtx, state.MSISDN)
	if err != nil {
		return ctx.Answer("Gagal membaca akun MyXL. Silakan coba lagi.", true)
	}
	if acc == nil {
		return ctx.Answer("Akun MyXL tidak ditemukan. Buka ulang .kuota.", true)
	}

	balance, balanceErr := p.client.GetBalance(queryCtx, acc)
	quota, quotaErr := p.client.GetQuotaDetails(queryCtx, acc)
	if balanceErr != nil && quotaErr != nil {
		return ctx.Answer("Gagal memperbarui pulsa dan kuota MyXL. Silakan coba lagi.", true)
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return ctx.Transition(raw, nativeQuotaRefreshTTL, nativeQuotaView(FormatQuotaResponse(acc, balance, quota, state.Masked)))
}

func decodeNativeQuotaState(raw []byte) (quotaRefreshState, error) {
	var state quotaRefreshState
	if len(raw) == 0 {
		return state, fmt.Errorf("myxl: empty native quota state")
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return quotaRefreshState{}, fmt.Errorf("myxl: decode native quota state: %w", err)
	}
	state.MSISDN = strings.TrimSpace(state.MSISDN)
	if state.MSISDN == "" {
		return quotaRefreshState{}, fmt.Errorf("myxl: native quota account is empty")
	}
	return state, nil
}

func nativeQuotaView(text string) presentation.View {
	return presentation.View{
		Text: text,
		Rows: []presentation.Row{{
			presentation.ActionButton("🔄 Perbarui Kuota", nativeQuotaRefreshAction),
		}},
	}
}

var _ nativeinteraction.FeatureDriver = (*Plugin)(nil)
