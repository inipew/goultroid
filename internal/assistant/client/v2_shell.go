package client

import (
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/menu"
	assistantpresentation "github.com/inipew/goultroid/internal/assistant/presentation"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
)

var (
	ErrShellUnavailable = errors.New("assistant/client: assistant shell unavailable")
	ErrShellAdmission   = errors.New("assistant/client: assistant shell admission denied")
)

func (c *AssistantClient) dispatchStart(ctx *command.Context) error {
	err := c.openShell(ctx)
	if err == nil {
		return nil
	}
	if !shouldFallbackStart(err) {
		return err
	}
	c.logger.Debug("assistant: using legacy /start compatibility fallback")
	c.mu.RLock()
	fallback := c.legacyStart
	c.mu.RUnlock()
	if fallback == nil {
		return err
	}
	return fallback(ctx)
}

func shouldFallbackStart(err error) bool {
	return errors.Is(err, ErrShellUnavailable) ||
		errors.Is(err, ErrShellAdmission) ||
		errors.Is(err, rootinteraction.ErrInvalidFeature) ||
		errors.Is(err, rootinteraction.ErrScopeStale)
}

func (c *AssistantClient) openShell(cmdCtx *command.Context) error {
	if cmdCtx == nil || cmdCtx.Peer == nil || cmdCtx.SenderID == 0 {
		return ErrShellUnavailable
	}
	c.mu.RLock()
	ingress := c.v2Ingress
	catalog := c.v2Catalog
	ownerID := c.ownerID
	sudoGetter := c.sudoGetter
	c.mu.RUnlock()
	if ingress == nil || ingress.engine == nil || catalog == nil {
		return ErrShellUnavailable
	}

	private := isPrivatePeer(cmdCtx.Peer)
	var sudos []int64
	if sudoGetter != nil {
		sudos = sudoGetter()
	}
	perms := core.NewPermissions(ownerID, sudos)
	start, ok := catalog.FindInteraction(assistantshell.FeatureID, feature.InteractionDeepLink, assistantshell.InteractionStart)
	if !ok {
		return ErrShellUnavailable
	}
	if err := feature.AdmitInteraction(start, execution.SourceAssistant, cmdCtx.SenderID, private, perms); err != nil {
		return fmt.Errorf("%w: %w", ErrShellAdmission, err)
	}
	home, ok := catalog.FindInteraction(assistantshell.FeatureID, feature.InteractionScreen, assistantshell.InteractionHome)
	if !ok {
		return ErrShellUnavailable
	}
	if err := feature.AdmitInteraction(home, execution.SourceAssistant, cmdCtx.SenderID, private, perms); err != nil {
		return fmt.Errorf("%w: %w", ErrShellAdmission, err)
	}
	if err := c.ensureShellActions(ingress.engine, catalog); err != nil {
		return err
	}

	chatID := extractChatIDFromInputPeer(cmdCtx.Peer)
	if chatID == 0 {
		chatID = cmdCtx.SenderID
	}
	_, err := ingress.engine.Begin(cmdCtx.Ctx, orchestration.BeginRequest{
		FeatureID: assistantshell.FeatureID,
		ActorID:   cmdCtx.SenderID,
		State:     assistantshell.InitialState(),
		Target:    presentationtelegram.MessageTarget{Peer: cmdCtx.Peer, ChatID: chatID},
		View: assistantshell.HomeView(assistantshell.HomeModel{
			Username: c.Username(),
			Uptime:   time.Since(c.StartTime()),
		}),
	})
	return err
}

func isPrivatePeer(peer tg.InputPeerClass) bool {
	switch peer.(type) {
	case *tg.InputPeerUser, *tg.InputPeerSelf:
		return true
	default:
		return false
	}
}

func (c *AssistantClient) ensureShellActions(engine *orchestration.Engine, catalog feature.Catalog) error {
	if engine == nil || catalog == nil {
		return ErrShellUnavailable
	}
	scope, ok := catalog.FeatureScope(assistantshell.FeatureID)
	if !ok || scope.IsZero() {
		return ErrShellUnavailable
	}

	c.shellMu.Lock()
	defer c.shellMu.Unlock()
	if c.shellScope == scope && len(c.shellRegistrations) == 3 {
		return nil
	}
	for _, registration := range c.shellRegistrations {
		if registration != nil {
			registration.Close()
		}
	}
	c.shellRegistrations = nil
	c.shellScope = tasks.ScopeIdentity{}

	registrations := make([]*rootinteraction.HandlerRegistration, 0, 3)
	register := func(actionID string, handler orchestration.Handler) error {
		guarded := func(ctx *orchestration.Context) error {
			if err := c.admitShellAction(catalog, actionID, ctx); err != nil {
				return err
			}
			return handler(ctx)
		}
		registration, err := engine.RegisterAction(scope, assistantshell.FeatureID, actionID, guarded)
		if err != nil {
			return err
		}
		registrations = append(registrations, registration)
		return nil
	}
	if err := register(assistantshell.ActionRefresh, c.handleShellRefresh); err != nil {
		closeShellRegistrations(registrations)
		return err
	}
	if err := register(assistantshell.ActionPing, c.handleShellPing); err != nil {
		closeShellRegistrations(registrations)
		return err
	}
	if err := register(assistantshell.ActionLegacy, c.handleShellLegacy); err != nil {
		closeShellRegistrations(registrations)
		return err
	}
	c.shellScope = scope
	c.shellRegistrations = registrations
	return nil
}

func (c *AssistantClient) admitShellAction(catalog feature.Catalog, actionID string, ctx *orchestration.Context) error {
	if catalog == nil || ctx == nil {
		return ErrShellUnavailable
	}
	interaction, ok := catalog.FindInteraction(assistantshell.FeatureID, feature.InteractionAction, actionID)
	if !ok {
		return ErrShellUnavailable
	}
	session := ctx.Session()
	private := false
	if target, ok := ctx.Target().(presentationtelegram.MessageTarget); ok {
		private = isPrivatePeer(target.Peer)
	}
	c.mu.RLock()
	ownerID := c.ownerID
	sudoGetter := c.sudoGetter
	c.mu.RUnlock()
	var sudos []int64
	if sudoGetter != nil {
		sudos = sudoGetter()
	}
	if err := feature.AdmitInteraction(interaction, execution.SourceAssistant, session.Binding.ActorID, private, core.NewPermissions(ownerID, sudos)); err != nil {
		return fmt.Errorf("%w: %w", ErrShellAdmission, err)
	}
	return nil
}

func closeShellRegistrations(registrations []*rootinteraction.HandlerRegistration) {
	for _, registration := range registrations {
		if registration != nil {
			registration.Close()
		}
	}
}

func (c *AssistantClient) handleShellRefresh(ctx *orchestration.Context) error {
	state := assistantshell.NextRefreshState(ctx.State())
	return ctx.Transition(state, 0, assistantshell.HomeView(assistantshell.HomeModel{
		Username:  c.Username(),
		Uptime:    time.Since(c.StartTime()),
		Refreshes: assistantshell.RefreshCount(state),
	}))
}

func (*AssistantClient) handleShellPing(ctx *orchestration.Context) error {
	return ctx.Answer("🏓 Pong!", false)
}

func (c *AssistantClient) handleShellLegacy(ctx *orchestration.Context) error {
	target, ok := ctx.Target().(presentationtelegram.MessageTarget)
	if !ok || target.Peer == nil || target.ChatID == 0 || target.MessageID <= 0 {
		return orchestration.ErrInvalidTarget
	}
	screen := menu.BuildStartScreen(c.Username(), time.Since(c.StartTime()))
	text, markup := assistantpresentation.RenderScreen(screen)

	c.mu.RLock()
	inter := c.interaction
	c.mu.RUnlock()
	if inter == nil {
		return ErrShellUnavailable
	}
	messageTarget := assistantinteraction.NewMessageTarget(target.Peer, target.MessageID, target.ChatID, 0)
	if err := inter.Edit(ctx.Context(), messageTarget, text, markup); err != nil {
		return err
	}
	if c.menuCtrl != nil && c.menuCtrl.Instances() != nil {
		actorID := ctx.Session().Binding.ActorID
		c.menuCtrl.Instances().Register(menu.MenuInstance{
			ID:        fmt.Sprintf("menu:%d:%d", target.ChatID, target.MessageID),
			ChatID:    target.ChatID,
			MessageID: target.MessageID,
			Screen:    menu.ScreenIDStart,
			OwnerID:   actorID,
		})
	}
	ctx.Cancel()
	return nil
}
