package client

import (
	"context"
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
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/tasks"
)

var (
	ErrShellUnavailable         = errors.New("assistant/client: assistant shell unavailable")
	ErrShellAdmission           = errors.New("assistant/client: assistant shell admission denied")
	ErrShellSettingBindingStale = errors.New("assistant/client: setting binding is stale")
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
	if c.shellScope == scope && len(c.shellRegistrations) == 19 {
		return nil
	}
	for _, registration := range c.shellRegistrations {
		if registration != nil {
			registration.Close()
		}
	}
	c.shellRegistrations = nil
	c.shellScope = tasks.ScopeIdentity{}

	registrations := make([]*rootinteraction.HandlerRegistration, 0, 19)
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
		{id: assistantshell.ActionSettings, handler: c.handleShellSettings},
		{id: assistantshell.ActionSettingsPrev, handler: c.handleShellSettingsPrev},
		{id: assistantshell.ActionSettingsNext, handler: c.handleShellSettingsNext},
		{id: assistantshell.ActionSettingsOpen, handler: c.handleShellSettingsOpen},
		{id: assistantshell.ActionSettingPrev, handler: c.handleShellSettingPrev},
		{id: assistantshell.ActionSettingNext, handler: c.handleShellSettingNext},
		{id: assistantshell.ActionSettingOpen, handler: c.handleShellSettingOpen},
		{id: assistantshell.ActionSettingBack, handler: c.handleShellSettingBack},
		{id: assistantshell.ActionSettingChange, handler: c.handleShellSettingChange},
		{id: assistantshell.ActionSettingDecrease, handler: c.handleShellSettingDecrease},
		{id: assistantshell.ActionSettingIncrease, handler: c.handleShellSettingIncrease},
		{id: assistantshell.ActionSettingReset, handler: c.handleShellSettingReset},
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
	state = assistantshell.ScreenState(state, assistantshell.ScreenHome)
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
	state := assistantshell.ScreenState(ctx.State(), assistantshell.ScreenStatus)
	return ctx.Transition(state, 0, c.shellStatusView(state))
}

func (c *AssistantClient) handleShellStatusRefresh(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionStatus); err != nil {
		return err
	}
	state := assistantshell.NextRefreshState(ctx.State())
	state = assistantshell.ScreenState(state, assistantshell.ScreenStatus)
	return ctx.Transition(state, 0, c.shellStatusView(state))
}

func (c *AssistantClient) handleShellHelp(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionHelp); err != nil {
		return err
	}
	state := assistantshell.ScreenState(ctx.State(), assistantshell.ScreenHelp)
	return ctx.Transition(state, 0, assistantshell.HelpView(assistantshell.HelpModel{Commands: c.shellCommands()}))
}

func (c *AssistantClient) handleShellHome(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionHome); err != nil {
		return err
	}
	state := assistantshell.ScreenState(ctx.State(), assistantshell.ScreenHome)
	return ctx.Transition(state, 0, assistantshell.HomeView(assistantshell.HomeModel{
		Username:  c.Username(),
		Uptime:    time.Since(c.StartTime()),
		Refreshes: assistantshell.RefreshCount(state),
	}))
}

func (c *AssistantClient) handleShellSettings(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettings); err != nil {
		return err
	}
	state := assistantshell.ScreenState(ctx.State(), assistantshell.ScreenSettings)
	view, err := c.shellSettingsHomeView(state)
	if err != nil {
		return err
	}
	return ctx.Transition(state, 0, view)
}

func (c *AssistantClient) handleShellSettingsPrev(ctx *orchestration.Context) error {
	return c.stepShellSettingsCategory(ctx, -1)
}

func (c *AssistantClient) handleShellSettingsNext(ctx *orchestration.Context) error {
	return c.stepShellSettingsCategory(ctx, 1)
}

func (c *AssistantClient) stepShellSettingsCategory(ctx *orchestration.Context, delta int) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettings); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	categories := svc.Registry().Categories()
	state := assistantshell.StepCategoryState(ctx.State(), len(categories), delta)
	view, err := c.shellSettingsHomeView(state)
	if err != nil {
		return err
	}
	return ctx.Transition(state, 0, view)
}

func (c *AssistantClient) handleShellSettingsOpen(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingsCategory); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	categories := svc.Registry().Categories()
	state := assistantshell.OpenCategoryState(ctx.State(), len(categories))
	view, err := c.shellSettingsCategoryView(ctx.Context(), ctx.Session().Binding.ActorID, ctx.Session().Binding.ChatID, state)
	if err != nil {
		return err
	}
	return ctx.Transition(state, 0, view)
}

func (c *AssistantClient) handleShellSettingPrev(ctx *orchestration.Context) error {
	return c.stepShellSetting(ctx, -1)
}

func (c *AssistantClient) handleShellSettingNext(ctx *orchestration.Context) error {
	return c.stepShellSetting(ctx, 1)
}

func (c *AssistantClient) stepShellSetting(ctx *orchestration.Context, delta int) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingsCategory); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	category, defs := selectedSettingsCategory(svc.Registry(), assistantshell.DecodeState(ctx.State()))
	if category == "" {
		state := assistantshell.ScreenState(ctx.State(), assistantshell.ScreenSettings)
		view, err := c.shellSettingsHomeView(state)
		if err != nil {
			return err
		}
		return ctx.Transition(state, 0, view)
	}
	state := assistantshell.StepSettingState(ctx.State(), len(defs), delta)
	view, err := c.shellSettingsCategoryView(ctx.Context(), ctx.Session().Binding.ActorID, ctx.Session().Binding.ChatID, state)
	if err != nil {
		return err
	}
	return ctx.Transition(state, 0, view)
}

func (c *AssistantClient) handleShellSettingOpen(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingDetail); err != nil {
		return err
	}
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	_, defs := selectedSettingsCategory(svc.Registry(), assistantshell.DecodeState(ctx.State()))
	if len(defs) == 0 {
		return c.handleShellSettingBack(ctx)
	}
	state := assistantshell.OpenSettingState(ctx.State(), len(defs))
	index := selectionIndex(int(assistantshell.DecodeState(state).SettingIndex), len(defs))
	def := defs[index]
	state = assistantshell.BindSettingState(state, def.Namespace, def.Key)
	view, err := c.shellSettingDetailView(ctx.Context(), ctx.Session().Binding.ActorID, ctx.Session().Binding.ChatID, state)
	if err != nil {
		return err
	}
	return ctx.Transition(state, 0, view)
}

func (c *AssistantClient) handleShellSettingBack(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionSettingsCategory); err != nil {
		return err
	}
	state := assistantshell.ScreenState(ctx.State(), assistantshell.ScreenSettingsCategory)
	view, err := c.shellSettingsCategoryView(ctx.Context(), ctx.Session().Binding.ActorID, ctx.Session().Binding.ChatID, state)
	if err != nil {
		return err
	}
	return ctx.Transition(state, 0, view)
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
	return c.applyShellSettingMutation(ctx, assistantshell.MutationReset)
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

	// Re-resolve the stable schema identity after the optimistic revision fence,
	// immediately before planning and persistence.
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
	result.Previous = current
	plan, err := assistantshell.PlanMutation(*def, current, explicit != nil, operation)
	if err != nil {
		mutationErr := &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Result: result, Err: err}
		_ = ctx.Answer("This setting requires the Classic menu workflow.", true)
		return mutationErr
	}
	result.Outcome = plan.Outcome
	result.Persisted = plan.Value

	if plan.Outcome == assistantshell.MutationChanged {
		if operation == assistantshell.MutationReset {
			err = svc.ResetRegistered(ctx.Context(), settings.ScopeUser, userID, def.Namespace, def.Key, schemaVersion, userID)
		} else {
			err = svc.SetRegistered(ctx.Context(), settings.ScopeUser, userID, def.Namespace, def.Key, schemaVersion, plan.Value, userID)
		}
		if err != nil {
			if errors.Is(err, settings.ErrDefinitionChanged) {
				mutationErr := &assistantshell.MutationError{Stage: assistantshell.MutationStageBinding, Result: result, Err: ErrShellSettingBindingStale}
				_ = ctx.Answer("Setting changed while open. Reopen Settings.", true)
				return mutationErr
			}
			_ = c.renderShellMutationRecovery(ctx, result, "Update failed; no change was committed.")
			mutationErr := &assistantshell.MutationError{Stage: assistantshell.MutationStagePersist, Result: result, Err: err}
			_ = ctx.Answer("Setting update failed. You can retry safely.", true)
			return mutationErr
		}
	}

	effective, resolveErr := svc.Resolve(ctx.Context(), userID, chatID, def.Namespace, def.Key)
	if resolveErr != nil {
		result.Effective = plan.Value
	} else {
		result.Effective = effective
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
			Committed: plan.Outcome == assistantshell.MutationChanged,
			Result:    result,
			Err:       viewErr,
		}
		if mutationErr.Committed {
			_ = ctx.Answer("Saved, but the view could not refresh. Reopen Settings.", true)
		} else {
			_ = ctx.Answer("No change was needed, but the view could not refresh.", true)
		}
		return mutationErr
	}
	if result.Outcome == assistantshell.MutationNoop {
		return ctx.Answer("No setting change was needed.", false)
	}
	return ctx.Answer("Setting saved.", false)
}

func boundSettingDefinition(reg *settings.Registry, stateRaw []byte) (*settings.SettingDefinition, uint64, error) {
	if reg == nil {
		return nil, 0, ErrShellUnavailable
	}
	state := assistantshell.DecodeState(stateRaw)
	if state.Screen != assistantshell.ScreenSettingDetail {
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
	if !ok || fresh == nil || version == 0 || !assistantshell.SettingBindingMatches(stateRaw, fresh.Namespace, fresh.Key) {
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
	return assistantshell.SettingDetailView(assistantshell.SettingDetailModel{
		Definition:   *def,
		Current:      value,
		Source:       source,
		ExplicitUser: explicit != nil,
		Notice:       notice,
	}), nil
}

func (c *AssistantClient) shellSettingsService() *settings.Service {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settingsSvc
}

func (c *AssistantClient) shellSettingsHomeView(stateRaw []byte) (presentation.View, error) {
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return presentation.View{}, ErrShellUnavailable
	}
	categories := svc.Registry().Categories()
	state := assistantshell.DecodeState(stateRaw)
	if len(categories) == 0 {
		return assistantshell.SettingsHomeView(assistantshell.SettingsHomeModel{}), nil
	}
	index := selectionIndex(int(state.CategoryIndex), len(categories))
	category := categories[index]
	return assistantshell.SettingsHomeView(assistantshell.SettingsHomeModel{
		Category: assistantshell.SettingsCategory{
			ID:    category,
			Label: assistantshell.CategoryLabel(category),
			Count: len(svc.Registry().ListByCategory(category)),
		},
		Total:    len(categories),
		Selected: index,
	}), nil
}

func (c *AssistantClient) shellSettingsCategoryView(ctx context.Context, userID, chatID int64, stateRaw []byte) (presentation.View, error) {
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return presentation.View{}, ErrShellUnavailable
	}
	state := assistantshell.DecodeState(stateRaw)
	category, defs := selectedSettingsCategory(svc.Registry(), state)
	if category == "" {
		return assistantshell.SettingsCategoryView(assistantshell.SettingsCategoryModel{}), nil
	}
	index := selectionIndex(int(state.SettingIndex), len(defs))
	model := assistantshell.SettingsCategoryModel{
		Category: assistantshell.SettingsCategory{
			ID:    category,
			Label: assistantshell.CategoryLabel(category),
			Count: len(defs),
		},
		Total:    len(defs),
		Selected: index,
	}
	if len(defs) > 0 {
		def := defs[index]
		value, err := svc.Resolve(ctx, userID, chatID, def.Namespace, def.Key)
		if err != nil {
			return presentation.View{}, err
		}
		title := def.Title
		if title == "" {
			title = def.Namespace + ":" + def.Key
		}
		model.Current = assistantshell.SettingSummary{
			Title: title,
			Value: assistantshell.DisplaySettingValue(def.Sensitive, value),
		}
	}
	return assistantshell.SettingsCategoryView(model), nil
}

func (c *AssistantClient) shellSettingDetailView(ctx context.Context, userID, chatID int64, stateRaw []byte) (presentation.View, error) {
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return presentation.View{}, ErrShellUnavailable
	}
	state := assistantshell.DecodeState(stateRaw)
	_, defs := selectedSettingsCategory(svc.Registry(), state)
	if len(defs) == 0 {
		return presentation.View{}, ErrShellUnavailable
	}
	index := selectionIndex(int(state.SettingIndex), len(defs))
	def := defs[index]
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
	return assistantshell.SettingDetailView(assistantshell.SettingDetailModel{
		Definition:   def,
		Current:      value,
		Source:       source,
		ExplicitUser: explicit != nil,
	}), nil
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
