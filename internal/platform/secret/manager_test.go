package secret

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/platform/audit"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
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

func TestA6SecretAuditDoesNotRetainRedactedCredentialFragments(t *testing.T) {
	const secretValue = "private-secret-credential-value"
	coreLogger, observed := observer.New(zap.InfoLevel)
	auditor := audit.NewService(zap.New(coreLogger), 5)
	manager := NewManager(nil)
	manager.SetAuditor(auditor)
	manager.Set("BOT_TOKEN", secretValue)
	value, err := manager.Get("BOT_TOKEN")
	if err != nil || value != secretValue {
		t.Fatalf("secret manager did not preserve original value: %v", err)
	}
	events := auditor.Recent(5)
	if len(events) != 2 || events[1].Action != "secret.write" || events[0].Action != "secret.read" {
		t.Fatalf("missing secret audit action: %+v", events)
	}
	if events[1].Details["secret_present"] != true || events[0].Details["found"] != true {
		t.Fatalf("safe secret audit metadata missing: %+v", events)
	}
	for _, event := range events {
		if _, exists := event.Details["redacted"]; exists {
			t.Fatalf("partial secret retained in audit: %+v", event.Details)
		}
		if strings.Contains(fmt.Sprint(event.Details), secretValue) {
			t.Fatalf("secret retained in metadata: %+v", event.Details)
		}
	}
	for _, entry := range observed.All() {
		if strings.Contains(fmt.Sprint(entry.Message, entry.ContextMap()), secretValue) {
			t.Fatalf("secret leaked into structured logger: %v", entry.ContextMap())
		}
	}
}
