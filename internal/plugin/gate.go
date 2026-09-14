package plugin

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/inipew/goultroid/internal/platform/audit"
)

var (
	ErrCapabilityDenied = errors.New("plugin capability denied")
)

// CapabilityGate enforces runtime capability checks for plugins.
type CapabilityGate struct {
	mu            sync.RWMutex
	manifests     map[string]Manifest
	privilegedMap map[string]map[string]bool // pluginID -> capability -> allowed
	failClosed    bool
	auditor       audit.Auditor
}

// NewCapabilityGate creates an empty capability gate.
func NewCapabilityGate() *CapabilityGate {
	return &CapabilityGate{
		manifests:     make(map[string]Manifest),
		privilegedMap: make(map[string]map[string]bool),
		failClosed:    false,
	}
}

// SetAuditor attaches an audit service to record capability checks.
func (g *CapabilityGate) SetAuditor(a audit.Auditor) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.auditor = a
}

// SetFailClosed configures whether plugins without a registered manifest are rejected outright.
func (g *CapabilityGate) SetFailClosed(failClosed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failClosed = failClosed
}

// IsFailClosed returns whether fail-closed mode is enabled.
func (g *CapabilityGate) IsFailClosed() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.failClosed
}

// RegisterManifest registers a plugin's manifest with the gate.
func (g *CapabilityGate) RegisterManifest(m Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.manifests[m.ID] = m
	return nil
}

// UnregisterManifest removes a manifest installed during a failed or removed
// plugin registration.
func (g *CapabilityGate) UnregisterManifest(pluginID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.manifests, pluginID)
	delete(g.privilegedMap, pluginID)
}

// StageManifest atomically replaces a manifest and returns a lossless rollback
// that restores all gate state owned by the plugin.
func (g *CapabilityGate) StageManifest(m Manifest) (func(), error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	previous, existed := g.manifests[m.ID]
	previousPrivileged := clonePrivileges(g.privilegedMap[m.ID])
	g.manifests[m.ID] = m
	g.mu.Unlock()

	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if existed {
			g.manifests[m.ID] = previous
		} else {
			delete(g.manifests, m.ID)
		}
		if previousPrivileged == nil {
			delete(g.privilegedMap, m.ID)
		} else {
			g.privilegedMap[m.ID] = previousPrivileged
		}
	}, nil
}

func clonePrivileges(source map[string]bool) map[string]bool {
	if source == nil {
		return nil
	}
	clone := make(map[string]bool, len(source))
	for capability, allowed := range source {
		clone[capability] = allowed
	}
	return clone
}

// Register registers a plugin ID with a list of capabilities.
func (g *CapabilityGate) Register(pluginID string, capabilities []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	m := g.manifests[pluginID]
	m.ID = pluginID
	m.Capabilities = capabilities
	g.manifests[pluginID] = m
}

// AllowPrivileged grants explicit runtime permission for a privileged capability to a plugin.
func (g *CapabilityGate) AllowPrivileged(pluginID, capName string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.privilegedMap[pluginID] == nil {
		g.privilegedMap[pluginID] = make(map[string]bool)
	}
	g.privilegedMap[pluginID][capName] = true
}

// Check verifies that the plugin declares the capability and, if privileged, is allowlisted.
func (g *CapabilityGate) Check(pluginID, capName string) error {
	g.mu.RLock()
	m, ok := g.manifests[pluginID]
	failClosed := g.failClosed
	allowedPrivileged := g.privilegedMap[pluginID] != nil && g.privilegedMap[pluginID][capName]
	auditor := g.auditor
	g.mu.RUnlock()
	record := func(granted bool, reason string) {
		if auditor == nil {
			return
		}
		action := "capability.granted"
		if !granted {
			action = "capability.denied"
		}
		_ = auditor.Record(context.Background(), audit.AuditEvent{Action: action, Target: pluginID, Details: map[string]any{"capability": capName, "granted": granted, "reason": reason}})
	}
	if !ok {
		if failClosed {
			err := fmt.Errorf("%w: plugin %q has no manifest", ErrCapabilityDenied, pluginID)
			record(false, err.Error())
			return err
		}
		// Legacy plugins without manifest: allow non-privileged capabilities
		if IsPrivilegedCapability(capName) {
			err := fmt.Errorf("%w: plugin %q has no manifest and cannot use privileged capability %s", ErrCapabilityDenied, pluginID, capName)
			record(false, err.Error())
			return err
		}
		record(true, "legacy allow non-privileged")
		return nil
	}

	if !m.HasCapability(capName) {
		err := fmt.Errorf("%w: plugin %q does not declare capability %s", ErrCapabilityDenied, pluginID, capName)
		record(false, err.Error())
		return err
	}

	if IsPrivilegedCapability(capName) {
		if !allowedPrivileged {
			err := fmt.Errorf("%w: privileged capability %s is not allowlisted for plugin %q", ErrCapabilityDenied, capName, pluginID)
			record(false, err.Error())
			return err
		}
	}

	record(true, "granted")
	return nil
}
