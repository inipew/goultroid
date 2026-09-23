package localization

import "testing"

func TestCanonicalLocaleUsesBoundedAssistantVocabulary(t *testing.T) {
	tests := map[string]string{
		"":      LocaleEnglish,
		"en":    LocaleEnglish,
		"en-US": LocaleEnglish,
		"id":    LocaleIndonesian,
		"id-ID": LocaleIndonesian,
		"in":    LocaleIndonesian,
		"fr":    LocaleEnglish,
	}
	for input, want := range tests {
		if got := CanonicalLocale(input); got != want {
			t.Fatalf("CanonicalLocale(%q)=%q, want %q", input, got, want)
		}
	}
	if got := SupportedLocales(); len(got) != 2 || got[0] != LocaleEnglish || got[1] != LocaleIndonesian {
		t.Fatalf("SupportedLocales()=%v", got)
	}
}

func TestAssistantBuiltinTranslationsAreLocaleExplicit(t *testing.T) {
	if got := Translate(LocaleIndonesian, "assistant.button.settings"); got != "⚙️ Pengaturan" {
		t.Fatalf("Indonesian settings label=%q", got)
	}
	if got := Translate(LocaleEnglish, "assistant.button.settings"); got != "⚙️ Settings" {
		t.Fatalf("English settings label=%q", got)
	}
	svc := New(LocaleEnglish)
	_ = svc.TLocale(LocaleIndonesian, "assistant.button.home")
	if got := svc.GetLocale(); got != LocaleEnglish {
		t.Fatalf("explicit translation mutated selected locale to %q", got)
	}
}
