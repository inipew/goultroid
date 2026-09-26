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
