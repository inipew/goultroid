package plugin

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrCapabilityDenied = errors.New("plugin capability denied")
)

// CapabilityGate enforces runtime capability checks for plugins.
type CapabilityGate struct {
	mu            sync.RWMutex
	manifests     map[string]Manifest
	privilegedMap map[string]map[string]bool // pluginID -> capability -> allowed
}

// NewCapabilityGate creates an empty capability gate.
func NewCapabilityGate() *CapabilityGate {
	return &CapabilityGate{
		manifests:     make(map[string]Manifest),
		privilegedMap: make(map[string]map[string]bool),
	}
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
		// Legacy plugins without manifest: allow non-privileged capabilities
		if IsPrivilegedCapability(capName) {
			return fmt.Errorf("%w: plugin %q has no manifest and cannot use privileged capability %s", ErrCapabilityDenied, pluginID, capName)
		}
		return nil
	}

	if !m.HasCapability(capName) {
		return fmt.Errorf("%w: plugin %q does not declare capability %s", ErrCapabilityDenied, pluginID, capName)
	}

	if IsPrivilegedCapability(capName) {
		allowed := g.privilegedMap[pluginID] != nil && g.privilegedMap[pluginID][capName]
		if !allowed {
			return fmt.Errorf("%w: privileged capability %s is not allowlisted for plugin %q", ErrCapabilityDenied, capName, pluginID)
		}
	}

	return nil
}
