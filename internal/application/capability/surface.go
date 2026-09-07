package capability

import "github.com/inipew/goultroid/internal/execution"

// FilterBySurface returns all capabilities from the given list supported on the specified source.
func FilterBySurface(caps []Capability, source execution.Source) []Capability {
	var result []Capability
	for _, c := range caps {
		if c.Surfaces.Supports(source) {
			result = append(result, c)
		}
	}
	return result
}
