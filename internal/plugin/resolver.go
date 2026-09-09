package plugin

import (
	"errors"
	"fmt"
	"sort"
)

var (
	ErrCyclicDependency  = errors.New("cyclic plugin dependency detected")
	ErrMissingDependency = errors.New("missing required plugin dependency")
	ErrPluginConflict    = errors.New("plugin conflict detected")
)

// ResolveOrder computes the deterministic topological startup order for plugins
// based on declared dependencies and verifies there are no conflicts or cycles.
func ResolveOrder(manifests []Manifest) ([]string, error) {
	manifestMap := make(map[string]Manifest, len(manifests))
	for _, m := range manifests {
		if err := m.Validate(); err != nil {
			return nil, err
		}
		if _, exists := manifestMap[m.ID]; exists {
			return nil, fmt.Errorf("duplicate plugin ID %q in dependency resolution", m.ID)
		}
		manifestMap[m.ID] = m
	}

	// 1. Conflict checking
	for _, m := range manifests {
		for _, conflictID := range m.Conflicts {
			if _, exists := manifestMap[conflictID]; exists {
				return nil, fmt.Errorf("%w: plugin %q conflicts with plugin %q", ErrPluginConflict, m.ID, conflictID)
			}
		}
	}

	// 2. Missing dependency checking
	for _, m := range manifests {
		for _, depID := range m.Dependencies {
			if _, exists := manifestMap[depID]; !exists {
				return nil, fmt.Errorf("%w: plugin %q requires plugin %q which is not registered", ErrMissingDependency, m.ID, depID)
			}
		}
	}

	// 3. Build adjacency list and in-degrees for Kahn's algorithm
	adj := make(map[string][]string, len(manifests))
	inDegree := make(map[string]int, len(manifests))
	for _, m := range manifests {
		adj[m.ID] = nil
		inDegree[m.ID] = 0
	}

	for _, m := range manifests {
		for _, depID := range m.Dependencies {
			// depID must be initialized before m.ID: edge depID -> m.ID
			adj[depID] = append(adj[depID], m.ID)
			inDegree[m.ID]++
		}
	}

	// Queue for nodes with in-degree 0
	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	// Sort initial queue for deterministic resolution order
	sort.Strings(queue)

	var order []string
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		order = append(order, curr)

		// Next neighbors to decrement
		var nextEligible []string
		for _, neighbor := range adj[curr] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				nextEligible = append(nextEligible, neighbor)
			}
		}

		if len(nextEligible) > 0 {
			sort.Strings(nextEligible)
			queue = append(queue, nextEligible...)
		}
	}

	if len(order) != len(manifests) {
		return nil, ErrCyclicDependency
	}

	return order, nil
}
