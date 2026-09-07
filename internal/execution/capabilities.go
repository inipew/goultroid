package execution

// Capability represents an explicit feature declaration in the GoUltroid system.
type Capability struct {
	ID          string
	Name        string
	Description string
	Category    string
	Surfaces    SurfaceMask
}

// CapabilityProvider is an optional interface plugins can implement to declare capabilities.
type CapabilityProvider interface {
	Capabilities() []Capability
}

// CapabilitySet holds a set of capability IDs.
type CapabilitySet map[string]struct{}

// NewCapabilitySet creates an empty CapabilitySet.
func NewCapabilitySet(ids ...string) CapabilitySet {
	set := make(CapabilitySet, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// Has reports whether the set contains the given capability ID.
func (s CapabilitySet) Has(id string) bool {
	if s == nil {
		return false
	}
	_, ok := s[id]
	return ok
}
