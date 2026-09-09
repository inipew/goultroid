package secret

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/inipew/goultroid/internal/platform/audit"
)

var (
	ErrSecretNotFound = errors.New("secret key not found")
)

// Manager provides controlled access to secrets and credential stores,
// preventing secrets from leaking into logs or diagnostic snapshots.
type Manager struct {
	mu      sync.RWMutex
	values  map[string]string
	auditor audit.Auditor
}

// NewManager creates a SecretManager with optional initial static secrets.
func NewManager(initial map[string]string) *Manager {
	m := &Manager{
		values: make(map[string]string),
	}
	for k, v := range initial {
		m.values[strings.TrimSpace(k)] = v
	}
	return m
}

// SetAuditor attaches an audit logger to record secret access.
func (m *Manager) SetAuditor(a audit.Auditor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auditor = a
}

// Get retrieves a secret value by key, checking in-memory storage first and
// falling back to environment variables.
func (m *Manager) Get(key string) (string, error) {
	cleanKey := strings.TrimSpace(key)
	if cleanKey == "" {
		return "", errors.New("secret key cannot be empty")
	}

	m.mu.RLock()
	val, ok := m.values[cleanKey]
	auditor := m.auditor
	m.mu.RUnlock()

	found := ok && val != ""
	envVal := ""
	if !found {
		envVal = os.Getenv(cleanKey)
		found = envVal != ""
	}

	if auditor != nil {
		_ = auditor.Record(context.Background(), audit.AuditEvent{
			Action: "secret.read",
			Target: cleanKey,
			Details: map[string]any{
				"found": found,
			},
		})
	}

	if ok && val != "" {
		return val, nil
	}
	if envVal != "" {
		return envVal, nil
	}

	return "", ErrSecretNotFound
}

// Set stores or overrides a secret in memory.
func (m *Manager) Set(key, val string) {
	cleanKey := strings.TrimSpace(key)
	m.mu.Lock()
	m.values[cleanKey] = val
	auditor := m.auditor
	m.mu.Unlock()

	if auditor != nil {
		_ = auditor.Record(context.Background(), audit.AuditEvent{
			Action: "secret.write",
			Target: cleanKey,
			Details: map[string]any{
				"redacted": Redact(val),
			},
		})
	}
}

// Keys returns the list of secret keys stored in memory (for diagnostics without leaking values).
func (m *Manager) Keys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.values))
	for k := range m.values {
		keys = append(keys, k)
	}
	return keys
}

// Redact replaces sensitive characters of a secret with asterisks for safe display.
func Redact(secret string) string {
	if len(secret) <= 4 {
		return "****"
	}
	return secret[:2] + strings.Repeat("*", len(secret)-4) + secret[len(secret)-2:]
}
