package addon

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// CapabilityGate enforces capability permission boundaries for external addons.
type CapabilityGate struct {
	mu         sync.RWMutex
	addonCaps  map[string]map[Capability]bool
	privileged map[string]map[Capability]bool
}

// NewCapabilityGate creates an empty CapabilityGate.
func NewCapabilityGate() *CapabilityGate {
	return &CapabilityGate{
		addonCaps:  make(map[string]map[Capability]bool),
		privileged: make(map[string]map[Capability]bool),
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
	delete(g.privileged, cleanName)
}

// HasCapability checks whether an addon is authorized for a capability.
// Privileged capabilities must be both declared in the manifest and explicitly
// allowlisted by the host.
func (g *CapabilityGate) HasCapability(name string, cap Capability) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	cleanName := strings.ToLower(strings.TrimSpace(name))
	caps, ok := g.addonCaps[cleanName]
	if !ok || !caps[cap] {
		return false
	}
	if IsPrivilegedCapability(cap) {
		return g.privileged[cleanName] != nil && g.privileged[cleanName][cap]
	}
	return true
}


// AllowPrivileged explicitly grants a declared privileged capability.
func (g *CapabilityGate) AllowPrivileged(name string, capability Capability) error {
	if !IsPrivilegedCapability(capability) {
		return fmt.Errorf("%w: capability %q is not privileged", ErrUnauthorizedCapability, capability)
	}
	cleanName := strings.ToLower(strings.TrimSpace(name))
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.addonCaps[cleanName] == nil || !g.addonCaps[cleanName][capability] {
		return fmt.Errorf("%w: addon %q did not declare %q", ErrUnauthorizedCapability, cleanName, capability)
	}
	if g.privileged[cleanName] == nil {
		g.privileged[cleanName] = make(map[Capability]bool)
	}
	g.privileged[cleanName][capability] = true
	return nil
}

// RevokePrivileged removes one explicit privileged grant.
func (g *CapabilityGate) RevokePrivileged(name string, capability Capability) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cleanName := strings.ToLower(strings.TrimSpace(name))
	if grants := g.privileged[cleanName]; grants != nil {
		delete(grants, capability)
		if len(grants) == 0 {
			delete(g.privileged, cleanName)
		}
	}
}

// ClearPrivileged removes every explicit privileged grant for an addon.
func (g *CapabilityGate) ClearPrivileged(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.privileged, strings.ToLower(strings.TrimSpace(name)))
}

// ListDeclaredCapabilities returns capabilities declared by the addon manifest,
// including privileged capabilities that are not currently approved.
func (g *CapabilityGate) ListDeclaredCapabilities(name string) []Capability {
	g.mu.RLock()
	defer g.mu.RUnlock()
	caps := g.addonCaps[strings.ToLower(strings.TrimSpace(name))]
	res := make([]Capability, 0, len(caps))
	for capability := range caps {
		res = append(res, capability)
	}
	sort.Slice(res, func(i, j int) bool { return res[i] < res[j] })
	return res
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
	for capability := range caps {
		if !IsPrivilegedCapability(capability) ||
			(g.privileged[cleanName] != nil && g.privileged[cleanName][capability]) {
			res = append(res, capability)
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i] < res[j] })
	return res
}
