package secret

import (
	"os"
	"testing"

	"github.com/inipew/goultroid/internal/platform/audit"
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

func TestSecretManager_Auditor(t *testing.T) {
	auditor := audit.NewService(nil, 10)
	mgr := NewManager(map[string]string{
		"SECRET_TOKEN": "my-secret-val",
	})
	mgr.SetAuditor(auditor)

	// Read secret
	_, _ = mgr.Get("SECRET_TOKEN")
	recent := auditor.Recent(5)
	if len(recent) != 1 || recent[0].Action != "secret.read" {
		t.Fatalf("expected 1 secret.read audit event, got %+v", recent)
	}

	// Write secret
	mgr.Set("NEW_SECRET", "supersecret123")
	recent = auditor.Recent(5)
	if len(recent) != 2 || recent[0].Action != "secret.write" {
		t.Fatalf("expected secret.write audit event, got %+v", recent)
	}
}
