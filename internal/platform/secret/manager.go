package secret

import (
	"errors"
	"os"
	"strings"
	"sync"
)

var (
	ErrSecretNotFound = errors.New("secret key not found")
)

// Manager provides controlled access to secrets and credential stores,
// preventing secrets from leaking into logs or diagnostic snapshots.
type Manager struct {
	mu     sync.RWMutex
	values map[string]string
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

// Get retrieves a secret value by key, checking in-memory storage first and
// falling back to environment variables.
func (m *Manager) Get(key string) (string, error) {
	cleanKey := strings.TrimSpace(key)
	if cleanKey == "" {
		return "", errors.New("secret key cannot be empty")
	}

	m.mu.RLock()
	val, ok := m.values[cleanKey]
	m.mu.RUnlock()

	if ok && val != "" {
		return val, nil
	}

	envVal := os.Getenv(cleanKey)
	if envVal != "" {
		return envVal, nil
	}

	return "", ErrSecretNotFound
}

// Set stores or overrides a secret in memory.
func (m *Manager) Set(key, val string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[strings.TrimSpace(key)] = val
}

// Redact replaces sensitive characters of a secret with asterisks for safe display.
func Redact(secret string) string {
	if len(secret) <= 4 {
		return "****"
	}
	return secret[:2] + strings.Repeat("*", len(secret)-4) + secret[len(secret)-2:]
}
