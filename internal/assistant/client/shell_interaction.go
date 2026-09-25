package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	assistantdeeplink "github.com/inipew/goultroid/internal/assistant/deeplink"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

var (
	ErrShellUnavailable            = errors.New("assistant/client: assistant shell unavailable")
	ErrShellAdmission              = errors.New("assistant/client: assistant shell admission denied")
	ErrShellSettingBindingStale    = errors.New("assistant/client: setting binding is stale")
	ErrShellHelpSelectionStale     = errors.New("assistant/client: help selection is stale")
	ErrShellSettingsSelectionStale = errors.New("assistant/client: settings selection is stale")
)

func (c *AssistantClient) dispatchStart(ctx *command.Context) error {
	if ctx != nil && len(ctx.Args) > 0 && assistantdeeplink.LooksLikeToken(ctx.Args[0]) {
		return c.dispatchDeepLink(ctx, ctx.Args[0])
	}
	err := c.openShell(ctx)
	if err == nil {
		c.touchAudience(ctx.Ctx, ctx.SenderID, pmrelay.AudienceSourceStart)
		return nil
	}
	if errors.Is(err, ErrShellAdmission) {
		err = c.dispatchPublicStart(ctx)
		if err == nil && ctx != nil {
			c.touchAudience(ctx.Ctx, ctx.SenderID, pmrelay.AudienceSourceStart)
		}
		return err
	}
	if !shouldUseStartRecovery(err) {
		return err
	}
	c.logger.Debug("assistant: using static /start recovery response")
	err = command.NewUnavailableStartHandler()(ctx)
	if err == nil && ctx != nil {
		c.touchAudience(ctx.Ctx, ctx.SenderID, pmrelay.AudienceSourceStart)
	}
	return err
}

const assistantDeepLinkExecutionTimeout = 2 * time.Minute

func (c *AssistantClient) dispatchDeepLink(cmdCtx *command.Context, rawToken string) error {
	if cmdCtx == nil || cmdCtx.Ctx == nil || cmdCtx.Peer == nil || cmdCtx.SenderID == 0 {
		return ErrShellUnavailable
	}
	if !isPrivatePeer(cmdCtx.Peer) {
		return replyDeepLinkUnavailable(cmdCtx)
	}
	c.mu.RLock()
	router := c.deepLinks
	taskClient := c.tasks
	c.mu.RUnlock()
	if router == nil || taskClient == nil {
		c.logger.Warn("assistant: deep-link runtime unavailable")
		return replyDeepLinkUnavailable(cmdCtx)
	}

	prepared, err := router.Prepare(cmdCtx.Ctx, rawToken, cmdCtx.SenderID)
	if err != nil {
		c.logger.Debug("assistant: deep-link rejected",
			zap.Int64("sender_id", cmdCtx.SenderID),
			zap.Error(err),
		)
		return replyDeepLinkUnavailable(cmdCtx)
	}

	chatID := extractChatIDFromInputPeer(cmdCtx.Peer)
	if chatID == 0 {
		chatID = cmdCtx.SenderID
	}
	resources := prepared.Resources()
	pool := tasks.PoolID("interactive")
	for _, requirement := range resources {
		if requirement.Name == "media" && requirement.Amount > 0 {
			pool = tasks.PoolID("general")
			break
		}
	}
	sequence := c.deepLinkSeq.Add(1)
	taskID := tasks.TaskID(fmt.Sprintf("assistant:deeplink:%d:%d", cmdCtx.SenderID, sequence))
	ticket, err := taskClient.Submit(cmdCtx.Ctx, tasks.WorkSpec{
		ID:               taskID,
		Scope:            prepared.Scope(),
		QuotaOwner:       tasks.OwnerID(fmt.Sprintf("assistant:user:%d", cmdCtx.SenderID)),
		Pool:             pool,
		Class:            tasks.PriorityInteractive,
		OrderingKey:      fmt.Sprintf("assistant:deeplink:%d", chatID),
		ExecutionTimeout: assistantDeepLinkExecutionTimeout,
		Resources:        append([]tasks.ResourceRequirement(nil), resources...),
		Handler: func(taskCtx context.Context) error {
			runCtx, cancel := context.WithCancel(taskCtx)
			defer cancel()
			stopWatching := context.AfterFunc(cmdCtx.Ctx, cancel)
			defer stopWatching()
			return router.ExecutePrepared(runCtx, prepared, assistantdeeplink.Delivery{
				ActorID: cmdCtx.SenderID,
				ChatID:  chatID,
				SendMedia: func(mediaType, path, caption string) error {
					if cmdCtx.Interaction == nil || cmdCtx.Peer == nil {
						return ErrShellUnavailable
					}
					_, sendErr := cmdCtx.Interaction.SendMedia(runCtx, cmdCtx.Peer, mediaType, path, caption)
					return sendErr
				},
				SendText: func(text string) error {
					if cmdCtx.Interaction == nil || cmdCtx.Peer == nil {
						return ErrShellUnavailable
					}
					_, sendErr := cmdCtx.Interaction.SendMessage(runCtx, cmdCtx.Peer, text, nil)
					return sendErr
				},
			})
		},
	})
	if err != nil {
		c.logger.Warn("assistant: deep-link admission rejected",
			zap.Int64("sender_id", cmdCtx.SenderID),
			zap.Error(err),
		)
		return replyDeepLinkUnavailable(cmdCtx)
	}

	result, waitErr := ticket.Wait(cmdCtx.Ctx)
	if waitErr != nil {
		_, _ = taskClient.Cancel(ticket.TaskID(), tasks.CauseTimeout)
		return waitErr
	}
	if result.IsSuccess() {
		c.touchAudience(cmdCtx.Ctx, cmdCtx.SenderID, pmrelay.AudienceSourceDeepLink)
		return nil
	}
	if result.Failure.Message != "" {
		c.logger.Warn("assistant: deep-link execution failed",
			zap.Int64("sender_id", cmdCtx.SenderID),
			zap.String("failure", result.Failure.Message),
		)
	}
	return replyDeepLinkUnavailable(cmdCtx)
}

func replyDeepLinkUnavailable(ctx *command.Context) error {
	if ctx == nil {
		return nil
	}
	_, err := ctx.Reply("⚠️ This link is invalid, expired, or no longer available.", nil)
	return err
}

func shouldUseStartRecovery(err error) bool {
	return errors.Is(err, ErrShellUnavailable) ||
		errors.Is(err, rootinteraction.ErrInvalidFeature) ||
		errors.Is(err, rootinteraction.ErrScopeStale)
}

func (c *AssistantClient) openShell(cmdCtx *command.Context) error {
	if cmdCtx == nil || cmdCtx.Peer == nil || cmdCtx.SenderID == 0 {
		return ErrShellUnavailable
	}
	c.mu.RLock()
	ingress := c.interactionIngress
	catalog := c.featureCatalog
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
		TTL:       assistantshell.InteractionTTL,
		Target:    presentationtelegram.MessageTarget{Peer: cmdCtx.Peer, ChatID: chatID},
		View:      c.shellHomeView(cmdCtx.Ctx, cmdCtx.SenderID, chatID, assistantshell.InitialState()),
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

func (c *AssistantClient) syncShellActions(engine *orchestration.Engine, catalog feature.Catalog) error {
	if engine == nil || catalog == nil {
		return ErrShellUnavailable
	}
	scope, ok := catalog.FeatureScope(assistantshell.FeatureID)
	if !ok || scope.IsZero() {
		c.clearShellActions()
		return nil
	}
	return c.ensureShellActions(engine, catalog)
}

func (c *AssistantClient) clearShellActions() {
	if c == nil {
		return
	}
	c.shellMu.Lock()
	registrations := c.shellRegistrations
	c.shellRegistrations = nil
	c.shellScope = tasks.ScopeIdentity{}
	c.shellMu.Unlock()
	closeShellRegistrations(registrations)
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
	expectedRegistrations := 29 + assistantshell.HelpModuleSlotCount + assistantshell.HelpCommandSlotCount + assistantshell.SettingsCategorySlotCount + assistantshell.SettingSlotCount
	if c.shellScope == scope && len(c.shellRegistrations) == expectedRegistrations {
		return nil
	}
	for _, registration := range c.shellRegistrations {
		if registration != nil {
			registration.Close()
		}
	}
	c.shellRegistrations = nil
	c.shellScope = tasks.ScopeIdentity{}

	registrations := make([]*rootinteraction.HandlerRegistration, 0, expectedRegistrations)
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
		{id: assistantshell.ActionHelpPrev, handler: c.handleShellHelpPrev},
		{id: assistantshell.ActionHelpNext, handler: c.handleShellHelpNext},
		{id: assistantshell.ActionHelpCmdPrev, handler: c.handleShellHelpCmdPrev},
		{id: assistantshell.ActionHelpCmdNext, handler: c.handleShellHelpCmdNext},
		{id: assistantshell.ActionHelpBack, handler: c.handleShellHelpBack},
		{id: assistantshell.ActionHome, handler: c.handleShellHome},
		{id: assistantshell.ActionClose, handler: c.handleShellClose},
		{id: assistantshell.ActionStatusRefresh, handler: c.handleShellStatusRefresh},
		{id: assistantshell.ActionSettings, handler: c.handleShellSettings},
		{id: assistantshell.ActionLanguage, handler: c.handleShellLanguage},
		{id: assistantshell.ActionLanguageEnglish, handler: c.handleShellLanguageEnglish},
		{id: assistantshell.ActionLanguageIndonesian, handler: c.handleShellLanguageIndonesian},
		{id: assistantshell.ActionSettingsPrev, handler: c.handleShellSettingsPrev},
		{id: assistantshell.ActionSettingsNext, handler: c.handleShellSettingsNext},
		{id: assistantshell.ActionSettingPrev, handler: c.handleShellSettingPrev},
		{id: assistantshell.ActionSettingNext, handler: c.handleShellSettingNext},
		{id: assistantshell.ActionSettingBack, handler: c.handleShellSettingBack},
		{id: assistantshell.ActionSettingChange, handler: c.handleShellSettingChange},
		{id: assistantshell.ActionSettingDecrease, handler: c.handleShellSettingDecrease},
		{id: assistantshell.ActionSettingIncrease, handler: c.handleShellSettingIncrease},
		{id: assistantshell.ActionSettingReset, handler: c.handleShellSettingReset},
		{id: assistantshell.ActionSettingResetConfirm, handler: c.handleShellSettingResetConfirm},
		{id: assistantshell.ActionSettingResetCancel, handler: c.handleShellSettingResetCancel},
		{id: assistantshell.ActionSettingInput, handler: c.handleShellSettingInput},
		{id: assistantshell.ActionSettingInputCancel, handler: c.handleShellSettingInputCancel},
	} {
		if err := register(action.id, action.handler); err != nil {
			closeShellRegistrations(registrations)
			return err
		}
	}
	for slot, actionID := range assistantshell.HelpModuleSlotActionIDs() {
		slot := slot
		if err := register(actionID, func(ctx *orchestration.Context) error {
			return c.handleShellHelpModuleSlot(ctx, slot)
		}); err != nil {
			closeShellRegistrations(registrations)
			return err
		}
	}
	for slot, actionID := range assistantshell.HelpCommandSlotActionIDs() {
		slot := slot
		if err := register(actionID, func(ctx *orchestration.Context) error {
			return c.handleShellHelpCommandSlot(ctx, slot)
		}); err != nil {
			closeShellRegistrations(registrations)
			return err
		}
	}
	for slot, actionID := range assistantshell.SettingsCategorySlotActionIDs() {
		slot := slot
		if err := register(actionID, func(ctx *orchestration.Context) error {
			return c.handleShellSettingsCategorySlot(ctx, slot)
		}); err != nil {
			closeShellRegistrations(registrations)
			return err
		}
	}
	for slot, actionID := range assistantshell.SettingSlotActionIDs() {
		slot := slot
		if err := register(actionID, func(ctx *orchestration.Context) error {
			return c.handleShellSettingSlot(ctx, slot)
		}); err != nil {
			closeShellRegistrations(registrations)
			return err
		}
	}
	c.shellScope = scope
	c.shellRegistrations = registrations
	return nil
}

func (c *AssistantClient) admitShellAction(catalog feature.Catalog, actionID string, ctx *orchestration.Context) error {
	return c.admitShellContinuation(catalog, feature.InteractionAction, actionID, ctx)
}

func (c *AssistantClient) admitShellScreen(ctx *orchestration.Context, screenID string) error {
	if ctx == nil {
		return ErrShellUnavailable
	}
	c.mu.RLock()
	catalog := c.featureCatalog
	c.mu.RUnlock()
	return c.admitShellContinuation(catalog, feature.InteractionScreen, screenID, ctx)
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
	state = assistantshell.ScreenState(state, assistantshell.ScreenHome)
	session := ctx.Session()
	return ctx.Transition(state, assistantshell.InteractionTTL, c.shellHomeView(ctx.Context(), session.Binding.ActorID, session.Binding.ChatID, state))
}

func (*AssistantClient) handleShellPing(ctx *orchestration.Context) error {
	if err := ctx.Touch(assistantshell.InteractionTTL); err != nil {
		return err
	}
	return ctx.Answer("🏓 Pong!", false)
}

func (c *AssistantClient) handleShellStatus(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionStatus); err != nil {
		return err
	}
	state := assistantshell.ScreenState(ctx.State(), assistantshell.ScreenStatus)
	session := ctx.Session()
	return ctx.Transition(state, assistantshell.InteractionTTL, c.shellStatusView(ctx.Context(), session.Binding.ActorID, session.Binding.ChatID, state))
}

func (c *AssistantClient) handleShellStatusRefresh(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionStatus); err != nil {
		return err
	}
	state := assistantshell.NextRefreshState(ctx.State())
	state = assistantshell.ScreenState(state, assistantshell.ScreenStatus)
	session := ctx.Session()
	return ctx.Transition(state, assistantshell.InteractionTTL, c.shellStatusView(ctx.Context(), session.Binding.ActorID, session.Binding.ChatID, state))
}

func (c *AssistantClient) handleShellHelp(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionHelp); err != nil {
		return err
	}
	commands := c.shellHelpCommands(ctx)
	current := assistantshell.DecodeState(ctx.State())
	state := assistantshell.HelpState(ctx.State(), commands, current.Screen != assistantshell.ScreenHelp)
	decoded := assistantshell.DecodeState(state)
	view := assistantshell.HelpView(assistantshell.HelpModel{
		Commands: commands,
		Page:     int(decoded.CategoryIndex),
		Locale:   c.shellInteractionLocale(ctx),
	})
	return ctx.Transition(state, assistantshell.InteractionTTL, c.shellHelpPresentation(ctx, view))
}

func (c *AssistantClient) handleShellHome(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionHome); err != nil {
		return err
	}
	state := assistantshell.ScreenState(ctx.State(), assistantshell.ScreenHome)
	session := ctx.Session()
	return ctx.Transition(state, assistantshell.InteractionTTL, c.shellHomeView(ctx.Context(), session.Binding.ActorID, session.Binding.ChatID, state))
}

func (c *AssistantClient) handleShellClose(ctx *orchestration.Context) error {
	if ctx == nil {
		return ErrShellUnavailable
	}
	deleteErr := ctx.Delete()
	ctx.Cancel()
	return deleteErr
}

func (c *AssistantClient) handleShellSettings(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettings); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	session := ctx.Session()
	locale := c.shellInteractionLocale(ctx)
	categories := shellSettingsCategories(svc.Registry(), locale)
	current := assistantshell.DecodeState(ctx.State())
	reset := current.Screen != assistantshell.ScreenSettings &&
		current.Screen != assistantshell.ScreenSettingsCategory &&
		current.Screen != assistantshell.ScreenSettingDetail &&
		current.Screen != assistantshell.ScreenSettingInput
	state := assistantshell.SettingsHomeState(ctx.State(), categories, reset)
	view, err := c.shellSettingsHomeView(ctx.Context(), session.Binding.ActorID, session.Binding.ChatID, state)
	if err != nil {
		return err
	}
	return ctx.Transition(state, assistantshell.InteractionTTL, view)
}

func (c *AssistantClient) handleShellSettingsPrev(ctx *orchestration.Context) error {
	return c.stepShellSettingsCategoryPage(ctx, -1)
}

func (c *AssistantClient) handleShellSettingsNext(ctx *orchestration.Context) error {
	return c.stepShellSettingsCategoryPage(ctx, 1)
}

func (c *AssistantClient) stepShellSettingsCategoryPage(ctx *orchestration.Context, delta int) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettings); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	session := ctx.Session()
	locale := c.shellInteractionLocale(ctx)
	categories := shellSettingsCategories(svc.Registry(), locale)
	state, ok := assistantshell.StepSettingsCategoryPageState(ctx.State(), categories, delta)
	if !ok {
		return ErrShellSettingsSelectionStale
	}
	view, err := c.shellSettingsHomeView(ctx.Context(), session.Binding.ActorID, session.Binding.ChatID, state)
	if err != nil {
		return err
	}
	return ctx.Transition(state, assistantshell.InteractionTTL, view)
}

func (c *AssistantClient) handleShellSettingsCategorySlot(ctx *orchestration.Context, slot int) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingsCategory); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	reg := svc.Registry()
	locale := c.shellInteractionLocale(ctx)
	categories := shellSettingsCategories(reg, locale)
	category, categoryIndex, ok := assistantshell.ResolveSettingsCategorySlot(ctx.State(), categories, slot)
	if !ok {
		return ErrShellSettingsSelectionStale
	}
	defs := reg.ListByCategory(category.ID)
	category.Count = len(defs)
	state := assistantshell.SettingsCategoryState(ctx.State(), categoryIndex, category, defs, 0)
	view, err := c.shellSettingsCategoryView(ctx.Context(), ctx.Session().Binding.ActorID, ctx.Session().Binding.ChatID, state)
	if err != nil {
		return err
	}
	return ctx.Transition(state, assistantshell.InteractionTTL, view)
}

func (c *AssistantClient) handleShellSettingPrev(ctx *orchestration.Context) error {
	return c.stepShellSettingPage(ctx, -1)
}

func (c *AssistantClient) handleShellSettingNext(ctx *orchestration.Context) error {
	return c.stepShellSettingPage(ctx, 1)
}

func (c *AssistantClient) stepShellSettingPage(ctx *orchestration.Context, delta int) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingsCategory); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	reg := svc.Registry()
	stateDecoded := assistantshell.DecodeState(ctx.State())
	category, defs := selectedSettingsCategory(reg, stateDecoded)
	if category == "" {
		return ErrShellSettingsSelectionStale
	}
	locale := c.shellInteractionLocale(ctx)
	modelCategory := assistantshell.SettingsCategory{
		ID:    category,
		Label: assistantshell.CategoryLabel(category, locale),
		Count: len(defs),
	}
	state, ok := assistantshell.StepSettingPageState(ctx.State(), modelCategory, defs, delta)
	if !ok {
		return ErrShellSettingsSelectionStale
	}
	view, err := c.shellSettingsCategoryView(ctx.Context(), ctx.Session().Binding.ActorID, ctx.Session().Binding.ChatID, state)
	if err != nil {
		return err
	}
	return ctx.Transition(state, assistantshell.InteractionTTL, view)
}

func (c *AssistantClient) handleShellSettingSlot(ctx *orchestration.Context, slot int) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingDetail); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	reg := svc.Registry()
	decoded := assistantshell.DecodeState(ctx.State())
	category, defs := selectedSettingsCategory(reg, decoded)
	if category == "" {
		return ErrShellSettingsSelectionStale
	}
	locale := c.shellInteractionLocale(ctx)
	modelCategory := assistantshell.SettingsCategory{
		ID:    category,
		Label: assistantshell.CategoryLabel(category, locale),
		Count: len(defs),
	}
	def, index, ok := assistantshell.ResolveSettingSlot(ctx.State(), modelCategory, defs, slot)
	if !ok {
		return ErrShellSettingsSelectionStale
	}
	fresh, schemaVersion, ok := reg.GetVersioned(def.Namespace, def.Key)
	if !ok || fresh == nil || schemaVersion == 0 {
		return ErrShellSettingBindingStale
	}
	state := assistantshell.SettingDetailState(ctx.State(), index)
	state = assistantshell.BindSettingState(state, fresh.Namespace, fresh.Key, schemaVersion)
	view, err := c.shellSettingDetailView(ctx.Context(), ctx.Session().Binding.ActorID, ctx.Session().Binding.ChatID, state)
	if err != nil {
		return err
	}
	return ctx.Transition(state, assistantshell.InteractionTTL, view)
}

func (c *AssistantClient) handleShellSettingBack(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingsCategory); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	reg := svc.Registry()
	def, _, err := boundSettingDefinition(reg, ctx.State())
	if err != nil {
		return err
	}
	categories := reg.Categories()
	categoryIndex := settingsCategoryIndex(categories, def.Category)
	if categoryIndex < 0 {
		return ErrShellSettingsSelectionStale
	}
	defs := reg.ListByCategory(categories[categoryIndex])
	settingIndex := settingDefinitionIndex(defs, def.Namespace, def.Key)
	if settingIndex < 0 {
		return ErrShellSettingBindingStale
	}
	locale := c.shellInteractionLocale(ctx)
	category := assistantshell.SettingsCategory{
		ID:    categories[categoryIndex],
		Label: assistantshell.CategoryLabel(categories[categoryIndex], locale),
		Count: len(defs),
	}
	state := assistantshell.BackSettingsCategoryState(ctx.State(), categoryIndex, category, defs, settingIndex)
	view, err := c.shellSettingsCategoryView(ctx.Context(), ctx.Session().Binding.ActorID, ctx.Session().Binding.ChatID, state)
	if err != nil {
		return err
	}
	return ctx.Transition(state, assistantshell.InteractionTTL, view)
}

func (c *AssistantClient) handleShellSettingChange(ctx *orchestration.Context) error {
	return c.applyShellSettingMutation(ctx, assistantshell.MutationChange)
}

func (c *AssistantClient) handleShellSettingDecrease(ctx *orchestration.Context) error {
	return c.applyShellSettingMutation(ctx, assistantshell.MutationDecrease)
}

func (c *AssistantClient) handleShellSettingIncrease(ctx *orchestration.Context) error {
	return c.applyShellSettingMutation(ctx, assistantshell.MutationIncrease)
}

func (c *AssistantClient) handleShellSettingReset(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingResetConfirm); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	def, _, err := boundSettingDefinition(svc.Registry(), ctx.State())
	if err != nil {
		_ = ctx.Answer("Setting changed while open. Reopen Settings.", true)
		return &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Err: err}
	}
	state := assistantshell.BeginSettingResetConfirmState(ctx.State())
	return ctx.Transition(state, assistantshell.InteractionTTL, assistantshell.SettingResetConfirmView(assistantshell.SettingResetConfirmModel{
		Definition: *def,
		Locale:     c.shellInteractionLocale(ctx),
	}))
}

func (c *AssistantClient) handleShellSettingResetConfirm(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingResetConfirm); err != nil {
		return err
	}
	state := assistantshell.CompleteSettingResetConfirmState(ctx.State())
	if err := ctx.UpdateState(state, 0); err != nil {
		return &assistantshell.MutationError{Stage: assistantshell.MutationStageReserve, Err: err}
	}
	return c.applyShellSettingMutation(ctx, assistantshell.MutationReset)
}

func (c *AssistantClient) handleShellSettingResetCancel(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingResetConfirm); err != nil {
		return err
	}
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingDetail); err != nil {
		return err
	}
	state := assistantshell.CompleteSettingResetConfirmState(ctx.State())
	view, err := c.shellSettingDetailViewWithNotice(ctx.Context(), ctx.Session().Binding.ActorID, ctx.Session().Binding.ChatID, state, "Reset cancelled.")
	if err != nil {
		return err
	}
	return ctx.Transition(state, assistantshell.InteractionTTL, view)
}

func (c *AssistantClient) handleShellSettingInput(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingInput); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	def, _, err := boundSettingDefinition(svc.Registry(), ctx.State())
	if err != nil {
		_ = ctx.Answer("Setting changed while open. Reopen Settings.", true)
		return &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Err: err}
	}
	if def.Type != settings.TypeString {
		err := fmt.Errorf("%w: free-form input requires string setting", assistantshell.ErrMutationUnsupported)
		return &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Err: err}
	}
	state := assistantshell.BeginSettingInputState(ctx.State())
	if err := ctx.AwaitInput(state, assistantshell.SettingsInputTTL, assistantshell.SettingInputView(assistantshell.SettingInputModel{
		Definition: *def,
		Locale:     c.shellInteractionLocale(ctx),
	})); err != nil {
		_ = ctx.Answer("Unable to open setting input. Reopen Settings.", true)
		return &assistantshell.MutationError{Stage: assistantshell.MutationStageRender, Err: err}
	}
	return ctx.Answer("Waiting for your next message…", false)
}

func (c *AssistantClient) handleShellSettingInputCancel(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingInput); err != nil {
		return err
	}
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingDetail); err != nil {
		return err
	}
	state := assistantshell.CompleteSettingInputState(ctx.State())
	view, err := c.shellSettingDetailViewWithNotice(ctx.Context(), ctx.Session().Binding.ActorID, ctx.Session().Binding.ChatID, state, "Input cancelled.")
	if err != nil {
		return err
	}
	return ctx.Transition(state, assistantshell.InteractionTTL, view)
}

func (c *AssistantClient) handleInteractionTextInput(ctx *orchestration.Context, text string) error {
	if ctx == nil {
		return ErrInteractionUnavailable
	}
	featureID := ctx.Session().FeatureID
	if featureID == assistantshell.FeatureID {
		return c.handleShellSettingTextInput(ctx, text)
	}
	driver := c.featureDriver(featureID)
	if driver == nil {
		return ErrInteractionUnavailable
	}
	return driver.HandleAssistantInput(ctx, text)
}

func (c *AssistantClient) handleShellSettingTextInput(ctx *orchestration.Context, text string) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingInput); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	def, _, err := boundSettingDefinition(svc.Registry(), ctx.State())
	if err != nil {
		return &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Err: err}
	}
	if def.Type != settings.TypeString {
		err := fmt.Errorf("%w: pending input no longer targets a string setting", assistantshell.ErrMutationUnsupported)
		return &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Err: err}
	}

	trimmed := strings.TrimSpace(text)
	if strings.EqualFold(trimmed, "/cancel") {
		if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingDetail); err != nil {
			return err
		}
		state := assistantshell.CompleteSettingInputState(ctx.State())
		view, viewErr := c.shellSettingDetailViewWithNotice(ctx.Context(), ctx.Session().Binding.ActorID, ctx.Session().Binding.ChatID, state, "Input cancelled.")
		if viewErr != nil {
			return viewErr
		}
		return ctx.Transition(state, assistantshell.InteractionTTL, view)
	}
	if trimmed == "" {
		return c.rearmShellSettingInput(ctx, *def, "Value cannot be empty.")
	}
	if len([]byte(trimmed)) > assistantshell.MaxSettingsInputBytes {
		return c.rearmShellSettingInput(ctx, *def, "Value is too large. Send a shorter value.")
	}
	canonical, err := def.Canonicalize(trimmed)
	if err != nil {
		return c.rearmShellSettingInput(ctx, *def, "Invalid value. Check the setting requirements and try again.")
	}

	// Revalidate the exact detail-bound schema revision immediately before the
	// registered persistence boundary.
	def, schemaVersion, err := boundSettingDefinition(svc.Registry(), ctx.State())
	if err != nil {
		return &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Err: err}
	}
	userID := ctx.Session().Binding.ActorID
	chatID := ctx.Session().Binding.ChatID
	current, err := svc.Resolve(ctx.Context(), userID, chatID, def.Namespace, def.Key)
	if err != nil {
		return &assistantshell.MutationError{Stage: assistantshell.MutationStagePersist, Err: err}
	}
	result := assistantshell.MutationResult{
		Namespace: def.Namespace,
		Key:       def.Key,
		Operation: assistantshell.MutationInput,
		Previous:  assistantshell.SafeMutationValue(*def, current),
		Persisted: assistantshell.SafeMutationValue(*def, canonical),
	}

	commit, err := svc.SetRegisteredResult(ctx.Context(), settings.ScopeUser, userID, def.Namespace, def.Key, schemaVersion, canonical, userID)
	if err != nil {
		if errors.Is(err, settings.ErrDefinitionChanged) {
			return &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Result: result, Err: ErrShellSettingBindingStale}
		}
		recoveryErr := c.rearmShellSettingInput(ctx, *def, "Save failed. Send the value again to retry.")
		if recoveryErr == nil {
			fields := []zap.Field{
				zap.String("namespace", def.Namespace),
				zap.String("key", def.Key),
			}
			if !def.Sensitive {
				fields = append(fields, zap.Error(err))
			}
			c.logger.Warn("assistant: setting text persistence failed; input re-armed", fields...)
			return nil
		}
		return &assistantshell.MutationError{
			Stage:  assistantshell.MutationStagePersist,
			Result: result,
			Err:    errors.Join(err, fmt.Errorf("re-arm input recovery: %w", recoveryErr)),
		}
	}
	if commit.Changed {
		result.Outcome = assistantshell.MutationChanged
	} else {
		result.Outcome = assistantshell.MutationNoop
	}
	result.Persisted = assistantshell.SafeMutationValue(*def, commit.Persisted)
	effective, resolveErr := svc.Resolve(ctx.Context(), userID, chatID, def.Namespace, def.Key)
	if resolveErr != nil {
		result.Effective = assistantshell.SafeMutationValue(*def, canonical)
	} else {
		result.Effective = assistantshell.SafeMutationValue(*def, effective)
	}
	if source, sourceErr := settingValueSource(ctx.Context(), svc, userID, chatID, def.Namespace, def.Key); sourceErr == nil {
		result.Source = source
	}

	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingDetail); err != nil {
		return &assistantshell.MutationError{Stage: assistantshell.MutationStageRender, Committed: commit.Changed, Result: result, Err: err}
	}
	state := assistantshell.CompleteSettingInputState(ctx.State())
	notice := "User override saved."
	if !commit.Changed {
		notice = "No persistent change was needed."
	}
	view, viewErr := c.shellSettingDetailViewWithNotice(ctx.Context(), userID, chatID, state, notice)
	if viewErr == nil {
		viewErr = ctx.Transition(state, assistantshell.InteractionTTL, view)
	}
	if viewErr != nil {
		return &assistantshell.MutationError{
			Stage:     assistantshell.MutationStageRender,
			Committed: commit.Changed,
			Result:    result,
			Err:       viewErr,
		}
	}
	return nil
}

func (c *AssistantClient) rearmShellSettingInput(ctx *orchestration.Context, def settings.SettingDefinition, notice string) error {
	if ctx == nil {
		return ErrShellUnavailable
	}
	state := assistantshell.BeginSettingInputState(ctx.State())
	if err := ctx.AwaitInput(state, assistantshell.SettingsInputTTL, assistantshell.SettingInputView(assistantshell.SettingInputModel{
		Definition: def,
		Notice:     notice,
		Locale:     c.shellInteractionLocale(ctx),
	})); err != nil {
		return &assistantshell.MutationError{Stage: assistantshell.MutationStageRender, Err: err}
	}
	return nil
}

func (c *AssistantClient) applyShellSettingMutation(ctx *orchestration.Context, operation assistantshell.MutationOperation) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingDetail); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}

	state := ctx.State()
	def, _, err := boundSettingDefinition(svc.Registry(), state)
	if err != nil {
		_ = ctx.Answer("Setting changed while open. Reopen Settings.", true)
		return &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Err: err}
	}

	result := assistantshell.MutationResult{
		Namespace: def.Namespace,
		Key:       def.Key,
		Operation: operation,
	}
	if err := ctx.UpdateState(state, 0); err != nil {
		return &assistantshell.MutationError{Stage: assistantshell.MutationStageReserve, Result: result, Err: err}
	}

	userID := ctx.Session().Binding.ActorID
	chatID := ctx.Session().Binding.ChatID

	def, schemaVersion, err := boundSettingDefinition(svc.Registry(), ctx.State())
	if err != nil {
		mutationErr := &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Result: result, Err: err}
		_ = ctx.Answer("Setting changed while open. Reopen Settings.", true)
		return mutationErr
	}
	current, err := svc.Resolve(ctx.Context(), userID, chatID, def.Namespace, def.Key)
	if err != nil {
		mutationErr := &assistantshell.MutationError{Stage: assistantshell.MutationStagePersist, Result: result, Err: err}
		_ = ctx.Answer("Unable to read the latest setting value.", true)
		return mutationErr
	}
	explicit, err := svc.Get(ctx.Context(), settings.ScopeUser, userID, def.Namespace, def.Key)
	if err != nil {
		mutationErr := &assistantshell.MutationError{Stage: assistantshell.MutationStagePersist, Result: result, Err: err}
		_ = ctx.Answer("Unable to read the current user override.", true)
		return mutationErr
	}
	result.Previous = assistantshell.SafeMutationValue(*def, current)

	plan, err := assistantshell.PlanMutation(*def, current, explicit != nil, operation)
	if err != nil {
		mutationErr := &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Result: result, Err: err}
		_ = ctx.Answer("This setting requires the Assistant text-input workflow. Reopen the setting and use Change.", true)
		return mutationErr
	}
	result.Outcome = plan.Outcome
	result.Persisted = assistantshell.SafeMutationValue(*def, plan.Value)

	committed := false
	if plan.Outcome == assistantshell.MutationChanged {
		var commit settings.RegisteredMutationResult
		if operation == assistantshell.MutationReset {
			commit, err = svc.ResetRegisteredResult(ctx.Context(), settings.ScopeUser, userID, def.Namespace, def.Key, schemaVersion, userID)
		} else {
			commit, err = svc.SetRegisteredResult(ctx.Context(), settings.ScopeUser, userID, def.Namespace, def.Key, schemaVersion, plan.Value, userID)
		}
		if err != nil {
			if errors.Is(err, settings.ErrDefinitionChanged) {
				mutationErr := &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Result: result, Err: ErrShellSettingBindingStale}
				_ = ctx.Answer("Setting changed while open. Reopen Settings.", true)
				return mutationErr
			}
			recoveryErr := c.renderShellMutationRecovery(ctx, result, "Update failed; no change was committed.")
			if recoveryErr != nil {
				err = errors.Join(err, fmt.Errorf("render mutation recovery: %w", recoveryErr))
				_ = ctx.Answer("Setting update failed. Reopen Settings.", true)
			} else {
				_ = ctx.Answer("Setting update failed. You can retry safely.", true)
			}
			return &assistantshell.MutationError{Stage: assistantshell.MutationStagePersist, Result: result, Err: err}
		}
		committed = commit.Changed
		if commit.Changed {
			result.Outcome = assistantshell.MutationChanged
		} else {
			result.Outcome = assistantshell.MutationNoop
		}
		result.Persisted = assistantshell.SafeMutationValue(*def, commit.Persisted)
	}

	effective, resolveErr := svc.Resolve(ctx.Context(), userID, chatID, def.Namespace, def.Key)
	if resolveErr != nil {
		result.Effective = assistantshell.SafeMutationValue(*def, plan.Value)
	} else {
		result.Effective = assistantshell.SafeMutationValue(*def, effective)
	}
	source, sourceErr := settingValueSource(ctx.Context(), svc, userID, chatID, def.Namespace, def.Key)
	if sourceErr == nil {
		result.Source = source
	}

	notice := mutationNotice(result)
	view, viewErr := c.shellSettingDetailViewWithNotice(ctx.Context(), userID, chatID, ctx.State(), notice)
	if viewErr == nil {
		viewErr = ctx.Edit(view)
	}
	if viewErr != nil {
		mutationErr := &assistantshell.MutationError{
			Stage:     assistantshell.MutationStageRender,
			Committed: committed,
			Result:    result,
			Err:       viewErr,
		}
		if committed {
			_ = ctx.Answer("Saved, but the view could not refresh. Reopen Settings.", true)
		} else {
			_ = ctx.Answer("No persistent change was committed, but the view could not refresh.", true)
		}
		return mutationErr
	}
	if result.Outcome == assistantshell.MutationNoop {
		return ctx.Answer("No persistent setting change was needed.", false)
	}
	return ctx.Answer("Setting saved.", false)
}

func boundSettingDefinition(reg *settings.Registry, stateRaw []byte) (*settings.SettingDefinition, uint64, error) {
	if reg == nil {
		return nil, 0, ErrShellUnavailable
	}
	state := assistantshell.DecodeState(stateRaw)
	if state.Screen != assistantshell.ScreenSettingDetail && state.Screen != assistantshell.ScreenSettingInput && state.Screen != assistantshell.ScreenSettingResetConfirm {
		return nil, 0, ErrShellSettingBindingStale
	}
	_, defs := selectedSettingsCategory(reg, state)
	if len(defs) == 0 {
		return nil, 0, ErrShellSettingBindingStale
	}
	def := defs[selectionIndex(int(state.SettingIndex), len(defs))]
	if !assistantshell.SettingBindingMatches(stateRaw, def.Namespace, def.Key) {
		return nil, 0, ErrShellSettingBindingStale
	}
	fresh, version, ok := reg.GetVersioned(def.Namespace, def.Key)
	if !ok || fresh == nil || version == 0 || state.SchemaVersion == 0 ||
		state.SchemaVersion != version ||
		!assistantshell.SettingBindingMatches(stateRaw, fresh.Namespace, fresh.Key) {
		return nil, 0, ErrShellSettingBindingStale
	}
	return fresh, version, nil
}

func mutationNotice(result assistantshell.MutationResult) string {
	switch result.Outcome {
	case assistantshell.MutationNoop:
		return "No change was needed."
	case assistantshell.MutationChanged:
		if result.Operation == assistantshell.MutationReset {
			return "User override reset."
		}
		return "User override saved."
	default:
		return ""
	}
}

func (c *AssistantClient) renderShellMutationRecovery(ctx *orchestration.Context, result assistantshell.MutationResult, notice string) error {
	if ctx == nil {
		return ErrShellUnavailable
	}
	view, err := c.shellSettingDetailViewWithNotice(ctx.Context(), ctx.Session().Binding.ActorID, ctx.Session().Binding.ChatID, ctx.State(), notice)
	if err != nil {
		return err
	}
	return ctx.Edit(view)
}

func (c *AssistantClient) shellSettingDetailViewWithNotice(ctx context.Context, userID, chatID int64, stateRaw []byte, notice string) (presentation.View, error) {
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return presentation.View{}, ErrShellUnavailable
	}
	def, _, err := boundSettingDefinition(svc.Registry(), stateRaw)
	if err != nil {
		return presentation.View{}, err
	}
	value, err := svc.Resolve(ctx, userID, chatID, def.Namespace, def.Key)
	if err != nil {
		return presentation.View{}, err
	}
	source, err := settingValueSource(ctx, svc, userID, chatID, def.Namespace, def.Key)
	if err != nil {
		return presentation.View{}, err
	}
	explicit, err := svc.Get(ctx, settings.ScopeUser, userID, def.Namespace, def.Key)
	if err != nil {
		return presentation.View{}, err
	}
	locale := c.shellLocale(ctx, userID, chatID)
	return assistantshell.SettingDetailView(assistantshell.SettingDetailModel{
		Definition:   *def,
		Current:      value,
		Source:       source,
		ExplicitUser: explicit != nil,
		Notice:       notice,
		Locale:       locale,
	}), nil
}

func (c *AssistantClient) shellSettingsService() *settings.Service {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settingsSvc
}

func (c *AssistantClient) shellSettingsHomeView(ctx context.Context, userID, chatID int64, stateRaw []byte) (presentation.View, error) {
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return presentation.View{}, ErrShellUnavailable
	}
	locale := c.shellLocale(ctx, userID, chatID)
	categories := shellSettingsCategories(svc.Registry(), locale)
	state := assistantshell.DecodeState(stateRaw)
	return assistantshell.SettingsHomeView(assistantshell.SettingsHomeModel{
		Categories: categories,
		Page:       int(state.CategoryIndex),
		Locale:     locale,
	}), nil
}

func (c *AssistantClient) shellSettingsCategoryView(ctx context.Context, userID, chatID int64, stateRaw []byte) (presentation.View, error) {
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return presentation.View{}, ErrShellUnavailable
	}
	reg := svc.Registry()
	state := assistantshell.DecodeState(stateRaw)
	locale := c.shellLocale(ctx, userID, chatID)
	category, defs := selectedSettingsCategory(reg, state)
	if category == "" {
		return assistantshell.SettingsCategoryView(assistantshell.SettingsCategoryModel{Locale: locale}), nil
	}
	return assistantshell.SettingsCategoryView(assistantshell.SettingsCategoryModel{
		Category: assistantshell.SettingsCategory{
			ID:    category,
			Label: assistantshell.CategoryLabel(category, locale),
			Count: len(defs),
		},
		Definitions: defs,
		Page:        int(state.SettingIndex),
		Locale:      locale,
	}), nil
}

func (c *AssistantClient) shellSettingDetailView(ctx context.Context, userID, chatID int64, stateRaw []byte) (presentation.View, error) {
	return c.shellSettingDetailViewWithNotice(ctx, userID, chatID, stateRaw, "")
}

func shellSettingsCategories(reg *settings.Registry, locale string) []assistantshell.SettingsCategory {
	if reg == nil {
		return nil
	}
	categories := reg.Categories()
	out := make([]assistantshell.SettingsCategory, 0, len(categories))
	for _, category := range categories {
		out = append(out, assistantshell.SettingsCategory{
			ID:    category,
			Label: assistantshell.CategoryLabel(category, locale),
		})
	}
	return out
}

func settingsCategoryIndex(categories []string, category string) int {
	category = strings.TrimSpace(category)
	if category == "" {
		category = settings.CategoryGeneral
	}
	for index, candidate := range categories {
		if strings.EqualFold(candidate, category) {
			return index
		}
	}
	return -1
}

func settingDefinitionIndex(defs []settings.SettingDefinition, namespace, key string) int {
	for index, def := range defs {
		if strings.EqualFold(def.Namespace, namespace) && strings.EqualFold(def.Key, key) {
			return index
		}
	}
	return -1
}

func selectedSettingsCategory(reg *settings.Registry, state assistantshell.State) (string, []settings.SettingDefinition) {
	if reg == nil {
		return "", nil
	}
	categories := reg.Categories()
	if len(categories) == 0 {
		return "", nil
	}
	category := categories[selectionIndex(int(state.CategoryIndex), len(categories))]
	return category, reg.ListByCategory(category)
}

func selectionIndex(index, total int) int {
	if total <= 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= total {
		return total - 1
	}
	return index
}

func settingValueSource(ctx context.Context, svc *settings.Service, userID, chatID int64, namespace, key string) (string, error) {
	if chatID != 0 {
		item, err := svc.Get(ctx, settings.ScopeChat, chatID, namespace, key)
		if err != nil {
			return "", err
		}
		if item != nil {
			return "Chat override", nil
		}
	}
	if userID != 0 {
		item, err := svc.Get(ctx, settings.ScopeUser, userID, namespace, key)
		if err != nil {
			return "", err
		}
		if item != nil {
			return "User override", nil
		}
	}
	item, err := svc.Get(ctx, settings.ScopeGlobal, 0, namespace, key)
	if err != nil {
		return "", err
	}
	if item != nil {
		return "Global override", nil
	}
	return "Default", nil
}

func (c *AssistantClient) shellStatusView(ctx context.Context, userID, chatID int64, state []byte) presentation.View {
	return assistantshell.StatusView(assistantshell.StatusModel{
		Username:  c.Username(),
		Uptime:    time.Since(c.StartTime()),
		Engine:    "GoUltroid (MTProto)",
		Refreshes: assistantshell.RefreshCount(state),
		Locale:    c.shellLocale(ctx, userID, chatID),
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
