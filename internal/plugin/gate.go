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

func (g *CapabilityGate) recordAudit(pluginID, capName string, granted bool, reason string) {
	if g.auditor == nil {
		return
	}
	action := "capability.granted"
	if !granted {
		action = "capability.denied"
	}
	_ = g.auditor.Record(context.Background(), audit.AuditEvent{
		Action: action,
		Target: pluginID,
		Details: map[string]any{
			"capability": capName,
			"granted":    granted,
			"reason":     reason,
		},
	})
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
	defer g.mu.RUnlock()

	m, ok := g.manifests[pluginID]
	if !ok {
		if g.failClosed {
			err := fmt.Errorf("%w: plugin %q has no manifest", ErrCapabilityDenied, pluginID)
			g.recordAudit(pluginID, capName, false, err.Error())
			return err
		}
		// Legacy plugins without manifest: allow non-privileged capabilities
		if IsPrivilegedCapability(capName) {
			err := fmt.Errorf("%w: plugin %q has no manifest and cannot use privileged capability %s", ErrCapabilityDenied, pluginID, capName)
			g.recordAudit(pluginID, capName, false, err.Error())
			return err
		}
		g.recordAudit(pluginID, capName, true, "legacy allow non-privileged")
		return nil
	}

	if !m.HasCapability(capName) {
		err := fmt.Errorf("%w: plugin %q does not declare capability %s", ErrCapabilityDenied, pluginID, capName)
		g.recordAudit(pluginID, capName, false, err.Error())
		return err
	}

	if IsPrivilegedCapability(capName) {
		allowed := g.privilegedMap[pluginID] != nil && g.privilegedMap[pluginID][capName]
		if !allowed {
			err := fmt.Errorf("%w: privileged capability %s is not allowlisted for plugin %q", ErrCapabilityDenied, capName, pluginID)
			g.recordAudit(pluginID, capName, false, err.Error())
			return err
		}
	}

	g.recordAudit(pluginID, capName, true, "granted")
	return nil
}
