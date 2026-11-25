package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigPropagatesCookiesKeyWithoutOverridingProcessEnv(t *testing.T) {
	const envKey = "GOULTROID_YTDLP_COOKIES_KEY"
	envPath := filepath.Join(t.TempDir(), ".env")
	content := "APP_ID=123456\nAPP_HASH=test_hash\nPHONE=+628123456789\nGOULTROID_YTDLP_COOKIES_KEY=dotenv_cookie_key_value\n"
	if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	// 1. When process env is empty, loadConfig populates GOULTROID_YTDLP_COOKIES_KEY from .env.
	t.Setenv(envKey, "")
	if _, err := loadConfig(envPath); err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if got := os.Getenv(envKey); got != "dotenv_cookie_key_value" {
		t.Fatalf("os.Getenv(%s) = %q, want %q", envKey, got, "dotenv_cookie_key_value")
	}

	// 2. When process env is already set, .env must NOT override it.
	t.Setenv(envKey, "process_secret_key_value")
	if _, err := loadConfig(envPath); err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if got := os.Getenv(envKey); got != "process_secret_key_value" {
		t.Fatalf("os.Getenv(%s) = %q, want %q", envKey, got, "process_secret_key_value")
	}
}
