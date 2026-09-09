package plugin

import (
	"errors"
	"strings"
)

// Standard capability constants per Blueprint §17 and ADR 0004.
const (
	CapTelegramRead          = "telegram.read"
	CapTelegramSendMessage   = "telegram.send_message"
	CapTelegramEditMessage   = "telegram.edit_message"
	CapTelegramDeleteMessage = "telegram.delete_message"
	CapTelegramRaw           = "telegram.raw" // Privileged

	CapStorageRead  = "storage.read"
	CapStorageWrite = "storage.write"

	CapScheduler = "scheduler"
	CapWorkers   = "workers"
	CapEvents    = "events"
	CapTasks     = "tasks"

	CapHTTP      = "network.http"
	CapWebSocket = "network.websocket"

	CapFilesystemData  = "filesystem.data"
	CapFilesystemTemp  = "filesystem.temp"
	CapFilesystemCache = "filesystem.cache"

	CapProcessExecute = "process.execute" // Privileged
	CapSecretRead     = "secret.read"     // Privileged
)

// IsPrivilegedCapability returns true if a capability requires runtime-level privilege allowlisting.
func IsPrivilegedCapability(capName string) bool {
	switch capName {
	case CapTelegramRaw, CapProcessExecute, CapSecretRead:
		return true
	default:
		return false
	}
}

// Manifest represents the formal declaration of a plugin's identity, dependencies,
// capabilities, and storage namespace.
type Manifest struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Description  string         `json:"description,omitempty"`
	Dependencies []string       `json:"dependencies,omitempty"`
	Conflicts    []string       `json:"conflicts,omitempty"`
	Capabilities []string       `json:"capabilities"`
	Namespace    string         `json:"namespace,omitempty"`
	Quotas       map[string]int `json:"quotas,omitempty"`
}

// Validate checks that required fields on the manifest are present and well-formed.
func (m *Manifest) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return errors.New("manifest ID cannot be empty")
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("manifest Name cannot be empty")
	}
	if m.Namespace == "" {
		m.Namespace = "plugins." + m.ID
	}
	return nil
}

// HasCapability returns true if the manifest declares the given capability.
func (m *Manifest) HasCapability(capName string) bool {
	for _, c := range m.Capabilities {
		if c == capName {
			return true
		}
	}
	return false
}
