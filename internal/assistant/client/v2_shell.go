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
	c.mu.RUnlock()
	if ingress == nil || ingress.engine == nil || catalog == nil {
		return ErrShellUnavailable
	}

	private := isPrivatePeer(cmdCtx.Peer)
	if err := c.admitShellInteraction(catalog, feature.InteractionDeepLink, assistantshell.InteractionStart, cmdCtx.SenderID, private); err != nil {
		return err
	}
	if err := c.admitShellInteraction(catalog, feature.InteractionScreen, assistantshell.InteractionHome, cmdCtx.SenderID, private); err != nil {
		return err
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

func (c *AssistantClient) shellPermissions() *core.Permissions {
	c.mu.RLock()
	ownerID := c.ownerID
	sudoGetter := c.sudoGetter
	c.mu.RUnlock()
	var sudos []int64
	if sudoGetter != nil {
		sudos = sudoGetter()
	}
	return core.NewPermissions(ownerID, sudos)
}

func (c *AssistantClient) admitShellInteraction(catalog feature.Catalog, kind feature.InteractionKind, interactionID string, userID int64, private bool) error {
	if catalog == nil {
		return ErrShellUnavailable
	}
	interaction, ok := catalog.FindInteraction(assistantshell.FeatureID, kind, interactionID)
	if !ok {
		return ErrShellUnavailable
	}
	if err := feature.AdmitInteraction(interaction, execution.SourceAssistant, userID, private, c.shellPermissions()); err != nil {
		return fmt.Errorf("%w: %w", ErrShellAdmission, err)
	}
	return nil
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
	if c.shellScope == scope && len(c.shellRegistrations) == 7 {
		return nil
	}
	for _, registration := range c.shellRegistrations {
		if registration != nil {
			registration.Close()
		}
	}
	c.shellRegistrations = nil
	c.shellScope = tasks.ScopeIdentity{}

	registrations := make([]*rootinteraction.HandlerRegistration, 0, 7)
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
	for _, action := range []struct {
		id      string
		handler orchestration.Handler
	}{
		{id: assistantshell.ActionRefresh, handler: c.handleShellRefresh},
		{id: assistantshell.ActionPing, handler: c.handleShellPing},
		{id: assistantshell.ActionStatus, handler: c.handleShellStatus},
		{id: assistantshell.ActionHelp, handler: c.handleShellHelp},
		{id: assistantshell.ActionHome, handler: c.handleShellHome},
		{id: assistantshell.ActionStatusRefresh, handler: c.handleShellStatusRefresh},
		{id: assistantshell.ActionLegacy, handler: c.handleShellLegacy},
	} {
		if err := register(action.id, action.handler); err != nil {
			closeShellRegistrations(registrations)
			return err
		}
	}
	c.shellScope = scope
	c.shellRegistrations = registrations
	return nil
}

func (c *AssistantClient) admitShellAction(catalog feature.Catalog, actionID string, ctx *orchestration.Context) error {
	if ctx == nil {
		return ErrShellUnavailable
	}
	session := ctx.Session()
	private := false
	if target, ok := ctx.Target().(presentationtelegram.MessageTarget); ok {
		private = isPrivatePeer(target.Peer)
	}
	return c.admitShellInteraction(catalog, feature.InteractionAction, actionID, session.Binding.ActorID, private)
}

func (c *AssistantClient) admitShellScreen(ctx *orchestration.Context, screenID string) error {
	if ctx == nil {
		return ErrShellUnavailable
	}
	private := false
	if target, ok := ctx.Target().(presentationtelegram.MessageTarget); ok {
		private = isPrivatePeer(target.Peer)
	}
	c.mu.RLock()
	catalog := c.v2Catalog
	c.mu.RUnlock()
	return c.admitShellInteraction(catalog, feature.InteractionScreen, screenID, ctx.Session().Binding.ActorID, private)
}

func closeShellRegistrations(registrations []*rootinteraction.HandlerRegistration) {
	for _, registration := range registrations {
		if registration != nil {
			registration.Close()
		}
	}
}

func (c *AssistantClient) handleShellRefresh(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionHome); err != nil {
		return err
	}
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

func (c *AssistantClient) handleShellStatus(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionStatus); err != nil {
		return err
	}
	return ctx.Transition(ctx.State(), 0, c.shellStatusView(ctx.State()))
}

func (c *AssistantClient) handleShellStatusRefresh(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionStatus); err != nil {
		return err
	}
	state := assistantshell.NextRefreshState(ctx.State())
	return ctx.Transition(state, 0, c.shellStatusView(state))
}

func (c *AssistantClient) handleShellHelp(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionHelp); err != nil {
		return err
	}
	return ctx.Transition(ctx.State(), 0, assistantshell.HelpView(assistantshell.HelpModel{Commands: c.shellCommands()}))
}

func (c *AssistantClient) handleShellHome(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionHome); err != nil {
		return err
	}
	return ctx.Transition(ctx.State(), 0, assistantshell.HomeView(assistantshell.HomeModel{
		Username:  c.Username(),
		Uptime:    time.Since(c.StartTime()),
		Refreshes: assistantshell.RefreshCount(ctx.State()),
	}))
}

func (c *AssistantClient) shellStatusView(state []byte) presentation.View {
	return assistantshell.StatusView(assistantshell.StatusModel{
		Username:  c.Username(),
		Uptime:    time.Since(c.StartTime()),
		Engine:    "GoUltroid (MTProto)",
		Refreshes: assistantshell.RefreshCount(state),
	})
}

func (c *AssistantClient) shellCommands() []core.Command {
	if c == nil || c.cmdRouter == nil {
		return nil
	}
	router := c.cmdRouter.CoreRouter()
	if router == nil {
		return nil
	}
	return router.CommandsForSurface(execution.SourceAssistant)
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
