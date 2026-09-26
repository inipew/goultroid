package localization

import (
	"context"

	"github.com/inipew/goultroid/internal/settings"
)

// ResolveLocale reads the canonical ui:locale setting for one invocation.
// Settings hierarchy remains the locale-selection authority; lookup failure
// falls back deterministically to English.
func ResolveLocale(ctx context.Context, svc *settings.Service, userID, chatID int64) string {
	if svc == nil {
		return DefaultLocale
	}
	if ctx == nil {
		ctx = context.Background()
	}
	value, err := svc.ResolveString(ctx, userID, chatID, LocaleSettingNamespace, LocaleSettingKey)
	if err != nil {
		return DefaultLocale
	}
	return CanonicalLocale(value)
}
