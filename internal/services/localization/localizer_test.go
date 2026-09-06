package localization

import (
	"testing"
)

func TestLocalizer_DefaultLocale(t *testing.T) {
	loc := New("en")

	// 1. Simple key
	val := loc.T("common.success")
	if val != "Operation completed successfully." {
		t.Errorf("unexpected translated text: %s", val)
	}

	// 2. Key with parameters
	val = loc.T("common.failed", "disk full")
	if val != "Operation failed: disk full" {
		t.Errorf("unexpected formatted text: %s", val)
	}

	// 3. Missing key fallback
	val = loc.T("missing.key")
	if val != "missing.key" {
		t.Errorf("expected raw key fallback, got: %s", val)
	}
}

func TestLocalizer_IndonesianLocale(t *testing.T) {
	loc := New("id")

	val := loc.T("common.success")
	if val != "Operasi berhasil dijalankan." {
		t.Errorf("unexpected indonesian text: %s", val)
	}

	val = loc.T("admin.warned", "User1", 1, 3, "spamming")
	if val != "⚠️ Peringatan ditambahkan untuk <b>User1</b> (1/3). Alasan: spamming" {
		t.Errorf("unexpected indonesian warn text: %s", val)
	}

	// Fallback to English if key only in English
	loc.AddTranslations("en", map[string]string{
		"only.in.en": "English only text",
	})
	val = loc.T("only.in.en")
	if val != "English only text" {
		t.Errorf("expected fallback to english, got: %s", val)
	}
}

func TestLocalizer_SwitchLocale(t *testing.T) {
	loc := New("en")
	if loc.GetLocale() != "en" {
		t.Errorf("expected initial locale en, got %s", loc.GetLocale())
	}

	loc.SetLocale("id")
	if loc.GetLocale() != "id" {
		t.Errorf("expected switched locale id, got %s", loc.GetLocale())
	}

	val := loc.T("ui.confirm")
	if val != "✅ Konfirmasi" {
		t.Errorf("unexpected text after locale switch: %s", val)
	}
}
