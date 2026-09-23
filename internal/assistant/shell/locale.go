package shell

import (
	"context"
	"strings"

	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/localization"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/ui"
)

const (
	LocaleSettingNamespace = "ui"
	LocaleSettingKey       = "locale"
)

func shellLocale(locale string) string {
	return localization.CanonicalLocale(locale)
}

// ResolveLocale reads the one canonical Assistant locale setting through the
// central bounded settings service. Callers never retain a second locale map.
func ResolveLocale(ctx context.Context, svc *settings.Service, userID, chatID int64) string {
	if svc == nil {
		return localization.DefaultLocale
	}
	if ctx == nil {
		ctx = context.Background()
	}
	value, err := svc.ResolveString(ctx, userID, chatID, LocaleSettingNamespace, LocaleSettingKey)
	if err != nil {
		return localization.DefaultLocale
	}
	return localization.CanonicalLocale(value)
}

func tr(locale, key string, args ...any) string {
	return localization.Translate(shellLocale(locale), key, args...)
}

func optionalLocale(locales []string) string {
	if len(locales) == 0 {
		return localization.DefaultLocale
	}
	return shellLocale(locales[0])
}

func LanguageName(locale string) string {
	switch shellLocale(locale) {
	case localization.LocaleIndonesian:
		return "Bahasa Indonesia"
	default:
		return "English"
	}
}

type LanguageModel struct {
	Locale string
	Notice string
}

func LanguageView(model LanguageModel) presentation.View {
	locale := shellLocale(model.Locale)
	card := ui.NewCard(tr(locale, "assistant.language.title")).
		WithIcon("🌐").
		WithHeader(tr(locale, "assistant.language.header")).
		AddField(tr(locale, "assistant.language.current"), ui.EscapeHTML(LanguageName(locale)))
	if notice := strings.TrimSpace(model.Notice); notice != "" {
		card.AddField(tr(locale, "assistant.settings.result"), ui.EscapeHTML(notice))
	}
	card.WithFooter(tr(locale, "assistant.language.footer"))
	return presentation.View{
		Text: card.Render(),
		Rows: []presentation.Row{
			{
				{Text: tr(locale, "assistant.language.english"), ActionID: ActionLanguageEnglish},
				{Text: tr(locale, "assistant.language.indonesian"), ActionID: ActionLanguageIndonesian},
			},
			{{Text: tr(locale, "assistant.button.home"), ActionID: ActionHome}},
		},
	}
}

func LocalizedSettingDefinition(locale string, def settings.SettingDefinition) settings.SettingDefinition {
	if strings.EqualFold(def.Namespace, LocaleSettingNamespace) && strings.EqualFold(def.Key, LocaleSettingKey) {
		def.Title = tr(locale, "assistant.settings.locale_title")
		def.Description = tr(locale, "assistant.settings.locale_desc")
	}
	return def
}

func localizedSettingSource(locale, source string) string {
	switch strings.TrimSpace(source) {
	case "Chat override":
		return tr(locale, "assistant.settings.chat_override")
	case "User override":
		return tr(locale, "assistant.settings.user_override")
	case "Global override":
		return tr(locale, "assistant.settings.global_override")
	case "Default":
		return tr(locale, "assistant.settings.default_source")
	case "Inherited/default":
		return tr(locale, "assistant.settings.inherited")
	default:
		return source
	}
}

func localizedNotice(locale, notice string) string {
	switch strings.TrimSpace(notice) {
	case "Input cancelled.":
		if shellLocale(locale) == localization.LocaleIndonesian {
			return "Input dibatalkan."
		}
	case "User override saved.":
		if shellLocale(locale) == localization.LocaleIndonesian {
			return "Override user tersimpan."
		}
	case "User override reset.":
		if shellLocale(locale) == localization.LocaleIndonesian {
			return "Override user direset."
		}
	case "No change was needed.", "No persistent change was needed.":
		if shellLocale(locale) == localization.LocaleIndonesian {
			return "Tidak ada perubahan yang diperlukan."
		}
	}
	return notice
}
