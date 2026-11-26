package config

import "testing"

func TestLoadFrom_LogEnvAndFormatDefaultsAndValidation(t *testing.T) {
	base := map[string]string{
		"APP_ID":   "123",
		"APP_HASH": "hash",
		"PHONE":    "+628123456789",
	}

	// Default: production + json
	cfg, err := LoadFrom(func(k string) string { return base[k] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LogEnv != "production" || cfg.LogFormat != "json" {
		t.Fatalf("expected production/json defaults, got %s/%s", cfg.LogEnv, cfg.LogFormat)
	}

	// Development default format -> console
	devMap := map[string]string{
		"APP_ID":   "123",
		"APP_HASH": "hash",
		"PHONE":    "+628123456789",
		"LOG_ENV":  "development",
	}
	cfg, err = LoadFrom(func(k string) string { return devMap[k] })
	if err != nil {
		t.Fatalf("unexpected error for development: %v", err)
	}
	if cfg.LogEnv != "development" || cfg.LogFormat != "console" {
		t.Fatalf("expected development/console defaults, got %s/%s", cfg.LogEnv, cfg.LogFormat)
	}

	// Explicit LOG_FORMAT overrides environment default
	overrideMap := map[string]string{
		"APP_ID":     "123",
		"APP_HASH":   "hash",
		"PHONE":      "+628123456789",
		"LOG_ENV":    "production",
		"LOG_FORMAT": "console",
	}
	cfg, err = LoadFrom(func(k string) string { return overrideMap[k] })
	if err != nil {
		t.Fatalf("unexpected error for format override: %v", err)
	}
	if cfg.LogFormat != "console" {
		t.Fatalf("expected console override, got %s", cfg.LogFormat)
	}

	// Invalid LOG_ENV rejected
	badEnv := map[string]string{
		"APP_ID":   "123",
		"APP_HASH": "hash",
		"PHONE":    "+628123456789",
		"LOG_ENV":  "staging",
	}
	if _, err := LoadFrom(func(k string) string { return badEnv[k] }); err == nil {
		t.Fatal("expected error for invalid LOG_ENV")
	}

	// Invalid LOG_FORMAT rejected
	badFmt := map[string]string{
		"APP_ID":     "123",
		"APP_HASH":   "hash",
		"PHONE":      "+628123456789",
		"LOG_FORMAT": "xml",
	}
	if _, err := LoadFrom(func(k string) string { return badFmt[k] }); err == nil {
		t.Fatal("expected error for invalid LOG_FORMAT")
	}
}
