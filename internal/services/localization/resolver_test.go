package localization

import (
	"context"
	"fmt"
	"testing"

	"github.com/inipew/goultroid/internal/settings"
)

type localeRepository struct {
	items map[string]*settings.SettingItem
}

func newLocaleRepository() *localeRepository {
	return &localeRepository{items: make(map[string]*settings.SettingItem)}
}

func (r *localeRepository) itemKey(scopeType string, scopeID int64, namespace, key string) string {
	return fmt.Sprintf("%s:%d:%s:%s", scopeType, scopeID, namespace, key)
}

func (r *localeRepository) GetSetting(_ context.Context, scopeType string, scopeID int64, namespace, key string) (*settings.SettingItem, error) {
	item := r.items[r.itemKey(scopeType, scopeID, namespace, key)]
	if item == nil {
		return nil, nil
	}
	copy := *item
	return &copy, nil
}

func (r *localeRepository) GetEffectiveSetting(_ context.Context, namespace, key string, chatID, userID int64) (*settings.SettingItem, error) {
	for _, ref := range []struct {
		scope string
		id    int64
	}{
		{scope: string(settings.ScopeChat), id: chatID},
		{scope: string(settings.ScopeUser), id: userID},
		{scope: string(settings.ScopeGlobal), id: 0},
	} {
		if ref.scope != string(settings.ScopeGlobal) && ref.id == 0 {
			continue
		}
		if item := r.items[r.itemKey(ref.scope, ref.id, namespace, key)]; item != nil {
			copy := *item
			return &copy, nil
		}
	}
	return nil, nil
}

func (r *localeRepository) SetSetting(_ context.Context, item *settings.SettingItem) error {
	copy := *item
	r.items[r.itemKey(item.ScopeType, item.ScopeID, item.Namespace, item.Key)] = &copy
	return nil
}

func (r *localeRepository) SetSettingsBatch(ctx context.Context, items []*settings.SettingItem) error {
	for _, item := range items {
		if err := r.SetSetting(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (r *localeRepository) DeleteSetting(_ context.Context, scopeType string, scopeID int64, namespace, key string) error {
	delete(r.items, r.itemKey(scopeType, scopeID, namespace, key))
	return nil
}

func (*localeRepository) ListSettings(context.Context, string, int64, string) ([]settings.SettingItem, error) {
	return nil, nil
}

func (*localeRepository) GetSettingHistory(context.Context, string, string, int) ([]settings.SettingChangeRecord, error) {
	return nil, nil
}

func (*localeRepository) ListPendingOutbox(context.Context, int) ([]settings.SettingOutboxEntry, error) {
	return nil, nil
}

func (*localeRepository) MarkOutboxProcessed(context.Context, int64) error {
	return nil
}

func TestP2BResolveLocaleUsesCanonicalSettingsHierarchy(t *testing.T) {
	registry := settings.NewRegistry()
	if err := settings.RegisterDefaultDefinitions(registry); err != nil {
		t.Fatal(err)
	}
	repo := newLocaleRepository()
	service := settings.NewService(repo, registry, nil)
	ctx := context.Background()

	if got := ResolveLocale(ctx, service, 11, 22); got != LocaleEnglish {
		t.Fatalf("default locale=%q, want %q", got, LocaleEnglish)
	}

	if err := service.Set(ctx, settings.ScopeUser, 11, LocaleSettingNamespace, LocaleSettingKey, LocaleIndonesian, 11); err != nil {
		t.Fatal(err)
	}
	if got := ResolveLocale(ctx, service, 11, 22); got != LocaleIndonesian {
		t.Fatalf("user locale=%q, want %q", got, LocaleIndonesian)
	}

	if err := service.Set(ctx, settings.ScopeChat, 22, LocaleSettingNamespace, LocaleSettingKey, LocaleEnglish, 11); err != nil {
		t.Fatal(err)
	}
	if got := ResolveLocale(ctx, service, 11, 22); got != LocaleEnglish {
		t.Fatalf("chat locale=%q, want %q", got, LocaleEnglish)
	}
}

func TestP2BResolveLocaleFailsDeterministicallyToEnglish(t *testing.T) {
	if got := ResolveLocale(context.Background(), nil, 1, 2); got != LocaleEnglish {
		t.Fatalf("nil settings locale=%q", got)
	}
	if got := CanonicalLocale("unsupported-locale"); got != LocaleEnglish {
		t.Fatalf("unsupported locale=%q", got)
	}
}
