package addon

import (
	"fmt"
	"strings"
	"sync"
)

// CapabilityGate enforces capability permission boundaries for external addons.
type CapabilityGate struct {
	mu        sync.RWMutex
	addonCaps map[string]map[Capability]bool
}

// NewCapabilityGate creates an empty CapabilityGate.
func NewCapabilityGate() *CapabilityGate {
	return &CapabilityGate{
		addonCaps: make(map[string]map[Capability]bool),
	}
}

// Register registers authorized capabilities for an addon.
func (g *CapabilityGate) Register(name string, caps []Capability) {
	g.mu.Lock()
	defer g.mu.Unlock()

	cleanName := strings.ToLower(strings.TrimSpace(name))
	capMap := make(map[Capability]bool)
	for _, c := range caps {
		if ValidCapabilities[c] {
			capMap[c] = true
		}
	}
	g.addonCaps[cleanName] = capMap
}

// Unregister removes capability mappings for an uninstalled addon.
func (g *CapabilityGate) Unregister(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cleanName := strings.ToLower(strings.TrimSpace(name))
	delete(g.addonCaps, cleanName)
}

// HasCapability checks whether an addon is authorized for a capability.
func (g *CapabilityGate) HasCapability(name string, cap Capability) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	cleanName := strings.ToLower(strings.TrimSpace(name))
	if caps, ok := g.addonCaps[cleanName]; ok {
		return caps[cap]
	}
	return false
}

// Assert checks capability authorization and returns ErrUnauthorizedCapability if denied.
func (g *CapabilityGate) Assert(name string, cap Capability) error {
	if !g.HasCapability(name, cap) {
		return fmt.Errorf("%w: addon %q does not possess %q permission", ErrUnauthorizedCapability, name, cap)
	}
	return nil
}

// ListCapabilities returns the list of permissions granted to an addon.
func (g *CapabilityGate) ListCapabilities(name string) []Capability {
	g.mu.RLock()
	defer g.mu.RUnlock()

	cleanName := strings.ToLower(strings.TrimSpace(name))
	caps, ok := g.addonCaps[cleanName]
	if !ok {
		return nil
	}

	res := make([]Capability, 0, len(caps))
	for c := range caps {
		res = append(res, c)
	}
	return res
}
