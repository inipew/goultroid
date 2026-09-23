package client

import (
	"context"
	"errors"
	"time"

	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/localization"
	"github.com/inipew/goultroid/internal/settings"
)

func (c *AssistantClient) shellLocale(ctx context.Context, userID, chatID int64) string {
	return assistantshell.ResolveLocale(ctx, c.shellSettingsService(), userID, chatID)
}

func (c *AssistantClient) shellInteractionLocale(ctx *orchestration.Context) string {
	if ctx == nil {
		return localization.DefaultLocale
	}
	session := ctx.Session()
	return c.shellLocale(ctx.Context(), session.Binding.ActorID, session.Binding.ChatID)
}

func (c *AssistantClient) shellHomeView(ctx context.Context, userID, chatID int64, state []byte) presentation.View {
	return assistantshell.HomeView(assistantshell.HomeModel{
		Username:  c.Username(),
		Uptime:    time.Since(c.StartTime()),
		Refreshes: assistantshell.RefreshCount(state),
		Locale:    c.shellLocale(ctx, userID, chatID),
	})
}

func (c *AssistantClient) handleShellLanguage(ctx *orchestration.Context) error {
	if err := c.admitShellScreen(ctx, assistantshell.InteractionLanguage); err != nil {
		return err
	}
	state := assistantshell.ScreenState(ctx.State(), assistantshell.ScreenLanguage)
	return ctx.Transition(state, 0, assistantshell.LanguageView(assistantshell.LanguageModel{
		Locale: c.shellInteractionLocale(ctx),
	}))
}

func (c *AssistantClient) handleShellLanguageEnglish(ctx *orchestration.Context) error {
	return c.setShellLanguage(ctx, localization.LocaleEnglish)
}

func (c *AssistantClient) handleShellLanguageIndonesian(ctx *orchestration.Context) error {
	return c.setShellLanguage(ctx, localization.LocaleIndonesian)
}

func (c *AssistantClient) setShellLanguage(ctx *orchestration.Context, locale string) error {
	if ctx == nil {
		return ErrShellUnavailable
	}
	if err := c.admitShellScreen(ctx, assistantshell.InteractionLanguage); err != nil {
		return err
	}
	locale = localization.CanonicalLocale(locale)
	svc := c.shellSettingsService()
	if svc == nil || svc.Registry() == nil {
		return ErrShellUnavailable
	}
	def, schemaVersion, ok := svc.Registry().GetVersioned(
		assistantshell.LocaleSettingNamespace,
		assistantshell.LocaleSettingKey,
	)
	if !ok || def == nil || schemaVersion == 0 || def.Type != settings.TypeEnum {
		return ErrShellSettingBindingStale
	}
	canonical, err := def.Canonicalize(locale)
	if err != nil {
		return err
	}

	state := assistantshell.ScreenState(ctx.State(), assistantshell.ScreenLanguage)
	if err := ctx.UpdateState(state, 0); err != nil {
		return err
	}
	session := ctx.Session()
	result, err := svc.SetRegisteredResult(
		ctx.Context(),
		settings.ScopeUser,
		session.Binding.ActorID,
		assistantshell.LocaleSettingNamespace,
		assistantshell.LocaleSettingKey,
		schemaVersion,
		canonical,
		session.Binding.ActorID,
	)
	if err != nil {
		if errors.Is(err, settings.ErrDefinitionChanged) {
			return ErrShellSettingBindingStale
		}
		_ = ctx.Edit(assistantshell.LanguageView(assistantshell.LanguageModel{
			Locale: c.shellLocale(ctx.Context(), session.Binding.ActorID, session.Binding.ChatID),
			Notice: "Language update failed. Retry safely.",
		}))
		return err
	}

	effective := canonical
	if resolved, resolveErr := svc.ResolveString(
		ctx.Context(),
		session.Binding.ActorID,
		session.Binding.ChatID,
		assistantshell.LocaleSettingNamespace,
		assistantshell.LocaleSettingKey,
	); resolveErr == nil {
		effective = localization.CanonicalLocale(resolved)
	}
	notice := localization.Translate(effective, "assistant.language.saved")
	if !result.Changed {
		notice = ""
	}
	return ctx.Edit(assistantshell.LanguageView(assistantshell.LanguageModel{
		Locale: effective,
		Notice: notice,
	}))
}
