package secret

import (
	"os"
	"testing"
)

func TestSecretManager_GetAndRedact(t *testing.T) {
	mgr := NewManager(map[string]string{
		"API_KEY": "secret-12345678",
	})

	val, err := mgr.Get("API_KEY")
	if err != nil || val != "secret-12345678" {
		t.Errorf("expected secret-12345678, got: %s, err: %v", val, err)
	}

	// Env fallback
	os.Setenv("TEST_BOT_TOKEN", "12345:TOKEN_XYZ")
	defer os.Unsetenv("TEST_BOT_TOKEN")

	envVal, err := mgr.Get("TEST_BOT_TOKEN")
	if err != nil || envVal != "12345:TOKEN_XYZ" {
		t.Errorf("expected env value, got: %s, err: %v", envVal, err)
	}

	// Redact
	redacted := Redact("abcdefghijkl")
	if redacted != "ab********kl" {
		t.Errorf("expected ab********kl, got: %s", redacted)
	}
}
