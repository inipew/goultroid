package localization

import "testing"

func TestBindKeepsSharedLocaleImmutable(t *testing.T) {
	svc := New(LocaleEnglish)
	id := Bind(svc, LocaleIndonesian)
	en := Bind(svc, LocaleEnglish)

	if got := id.T("common.cancelled"); got != "Operasi dibatalkan." {
		t.Fatalf("Indonesian translation=%q", got)
	}
	if got := en.T("common.cancelled"); got != "Operation cancelled." {
		t.Fatalf("English translation=%q", got)
	}
	if got := svc.GetLocale(); got != LocaleEnglish {
		t.Fatalf("shared locale mutated to %q", got)
	}
}
