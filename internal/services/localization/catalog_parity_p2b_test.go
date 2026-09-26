package localization

import "testing"

func TestP2BBuiltinEnglishIndonesianCatalogParity(t *testing.T) {
	svc := New(DefaultLocale)
	svc.mu.RLock()
	defer svc.mu.RUnlock()

	en := svc.catalogs[LocaleEnglish]
	id := svc.catalogs[LocaleIndonesian]
	if len(en) == 0 || len(id) == 0 {
		t.Fatalf("empty built-in catalog: en=%d id=%d", len(en), len(id))
	}
	for key := range en {
		if _, ok := id[key]; !ok {
			t.Errorf("Indonesian catalog missing %q", key)
		}
	}
	for key := range id {
		if _, ok := en[key]; !ok {
			t.Errorf("English catalog missing %q", key)
		}
	}
}

func TestP2BRepresentativeUserbotUXIsLocalized(t *testing.T) {
	svc := New(DefaultLocale)
	tests := []struct {
		key string
		args []any
	}{
		{key: "settings.cli.updated", args: []any{"ui", "locale", "id"}},
		{key: "admin.ban.success", args: []any{"Alice", ""}},
		{key: "media.info.title"},
		{key: "profile.me.title"},
	}
	for _, tt := range tests {
		en := svc.TLocale(LocaleEnglish, tt.key, tt.args...)
		id := svc.TLocale(LocaleIndonesian, tt.key, tt.args...)
		if en == tt.key || id == tt.key {
			t.Errorf("%s fell back to raw key: en=%q id=%q", tt.key, en, id)
		}
		if en == id {
			t.Errorf("%s has identical English/Indonesian output %q", tt.key, en)
		}
	}
}
