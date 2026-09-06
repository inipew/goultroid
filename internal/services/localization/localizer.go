package localization

import (
	"fmt"
	"strings"
	"sync"
)

const (
	// DefaultLocale is the fallback locale when a translation is missing.
	DefaultLocale = "en"
	// LocaleIndonesian identifier.
	LocaleIndonesian = "id"
)

// Localizer provides internationalization and localized string resolution.
type Localizer interface {
	T(key string, args ...any) string
	TLocale(locale, key string, args ...any) string
	SetLocale(locale string)
	GetLocale() string
	AddTranslations(locale string, dict map[string]string)
}

// Service implements Localizer with thread-safe translation catalogs.
type Service struct {
	mu            sync.RWMutex
	currentLocale string
	catalogs      map[string]map[string]string
}

// New creates a new Localizer pre-populated with standard built-in English and Indonesian translations.
func New(initialLocale string) *Service {
	if initialLocale == "" {
		initialLocale = DefaultLocale
	}

	s := &Service{
		currentLocale: strings.ToLower(initialLocale),
		catalogs:      make(map[string]map[string]string),
	}

	// Load standard catalogs
	s.loadBuiltinTranslations()
	return s
}

func (s *Service) SetLocale(locale string) {
	if locale == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentLocale = strings.ToLower(locale)
}

func (s *Service) GetLocale() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentLocale
}

func (s *Service) AddTranslations(locale string, dict map[string]string) {
	if len(dict) == 0 {
		return
	}
	locale = strings.ToLower(locale)

	s.mu.Lock()
	defer s.mu.Unlock()

	cat, exists := s.catalogs[locale]
	if !exists {
		cat = make(map[string]string)
		s.catalogs[locale] = cat
	}
	for k, v := range dict {
		cat[k] = v
	}
}

// T translates a key using the currently configured locale with safe fallback.
func (s *Service) T(key string, args ...any) string {
	s.mu.RLock()
	loc := s.currentLocale
	s.mu.RUnlock()

	return s.TLocale(loc, key, args...)
}

// TLocale translates a key using an explicit locale, falling back to default locale, then raw key.
func (s *Service) TLocale(locale, key string, args ...any) string {
	locale = strings.ToLower(locale)

	s.mu.RLock()
	defer s.mu.RUnlock()

	// 1. Try explicit locale
	if cat, ok := s.catalogs[locale]; ok {
		if val, found := cat[key]; found {
			return interpolate(val, args...)
		}
	}

	// 2. Fallback to default locale (en) if different
	if locale != DefaultLocale {
		if cat, ok := s.catalogs[DefaultLocale]; ok {
			if val, found := cat[key]; found {
				return interpolate(val, args...)
			}
		}
	}

	// 3. Fallback to raw key (never panic)
	if len(args) == 0 {
		return key
	}
	var sb strings.Builder
	sb.WriteString(key)
	sb.WriteString(" [")
	for i, a := range args {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprint(a))
	}
	sb.WriteString("]")
	return sb.String()
}

func interpolate(pattern string, args ...any) string {
	if len(args) == 0 {
		return pattern
	}
	return fmt.Sprintf(pattern, args...)
}

func (s *Service) loadBuiltinTranslations() {
	en := map[string]string{
		// Common
		"common.success":       "Operation completed successfully.",
		"common.failed":        "Operation failed: %s",
		"common.unknown_error": "An unknown error occurred.",
		"common.cancelled":     "Operation cancelled.",
		"common.processing":    "Processing, please wait...",

		// Errors
		"errors.permission_denied": "You do not have permission to execute this command.",
		"errors.rate_limited":      "Slow down! Rate limit active for %v.",
		"errors.not_found":         "Requested entity or target was not found.",
		"errors.invalid_args":      "Invalid arguments provided. Usage: %s",
		"errors.resource_limit":    "Resource limit exceeded: %s",

		// Moderation
		"admin.banned":                 "User <b>%s</b> has been banned from the group.",
		"admin.unbanned":               "User <b>%s</b> has been unbanned.",
		"admin.kicked":                 "User <b>%s</b> has been kicked from the group.",
		"admin.muted":                  "User <b>%s</b> has been muted.",
		"admin.unmuted":                "User <b>%s</b> has been unmuted.",
		"admin.warned":                 "⚠️ Warning added for <b>%s</b> (%d/%d). Reason: %s",
		"admin.warn_threshold_reached": "⚠️ User <b>%s</b> reached %d warnings and has been %s.",
		"admin.warns_cleared":          "Warnings cleared for user <b>%s</b>.",
		"admin.warns_count":            "User <b>%s</b> has %d active warning(s).",

		// UI & Buttons
		"ui.confirm":      "✅ Confirm",
		"ui.cancel":       "❌ Cancel",
		"ui.unauthorized": "⚠️ You are not authorized to use this button.",
	}

	id := map[string]string{
		// Common
		"common.success":       "Operasi berhasil dijalankan.",
		"common.failed":        "Operasi gagal: %s",
		"common.unknown_error": "Terjadi kesalahan yang tidak diketahui.",
		"common.cancelled":     "Operasi dibatalkan.",
		"common.processing":    "Sedang memproses, mohon tunggu...",

		// Errors
		"errors.permission_denied": "Anda tidak memiliki izin untuk menjalankan perintah ini.",
		"errors.rate_limited":      "Terlalu cepat! Batas laju aktif selama %v.",
		"errors.not_found":         "Entitas atau target yang diminta tidak ditemukan.",
		"errors.invalid_args":      "Argumen tidak valid. Penggunaan: %s",
		"errors.resource_limit":    "Batas penggunaan resource terlampaui: %s",

		// Moderation
		"admin.banned":                 "Pengguna <b>%s</b> telah diblokir dari grup.",
		"admin.unbanned":               "Pengguna <b>%s</b> telah dibuka blokirnya.",
		"admin.kicked":                 "Pengguna <b>%s</b> telah dikeluarkan dari grup.",
		"admin.muted":                  "Pengguna <b>%s</b> telah dibisukan.",
		"admin.unmuted":                "Pengguna <b>%s</b> telah diaktifkan kembali suaranya.",
		"admin.warned":                 "⚠️ Peringatan ditambahkan untuk <b>%s</b> (%d/%d). Alasan: %s",
		"admin.warn_threshold_reached": "⚠️ Pengguna <b>%s</b> mencapai %d peringatan dan telah di-%s.",
		"admin.warns_cleared":          "Peringatan telah dibersihkan untuk pengguna <b>%s</b>.",
		"admin.warns_count":            "Pengguna <b>%s</b> memiliki %d peringatan aktif.",

		// UI & Buttons
		"ui.confirm":      "✅ Konfirmasi",
		"ui.cancel":       "❌ Batal",
		"ui.unauthorized": "⚠️ Anda tidak memiliki izin untuk menggunakan tombol ini.",
	}

	s.AddTranslations(DefaultLocale, en)
	s.AddTranslations(LocaleIndonesian, id)
}
