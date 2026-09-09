package module

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var validIDRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ResolveOrder performs dependency validation, cycle detection, and topological sorting
// on a list of feature modules based on their Manifest() definitions.
// Returns modules in deterministic initialization order (dependencies before dependents).
func ResolveOrder(modules []Module) ([]Module, error) {
	if len(modules) == 0 {
		return nil, nil
	}

	byID := make(map[string]Module, len(modules))
	deps := make(map[string][]string, len(modules))
	conflicts := make(map[string][]string, len(modules))

	// 1. Validate individual module manifests and check ID uniqueness
	for _, m := range modules {
		if m == nil {
			return nil, ErrNilModule
		}
		manifest := m.Manifest()
		id := strings.TrimSpace(manifest.ID)
		if id == "" {
			return nil, ErrEmptyModuleID
		}
		if !validIDRegex.MatchString(id) {
			return nil, fmt.Errorf("invalid module ID %q: must match %s", id, validIDRegex.String())
		}
		if manifest.Version == "" {
			return nil, fmt.Errorf("module %q has empty version: %w", id, ErrEmptyModuleVersion)
		}
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("duplicate module ID %q", id)
		}
		byID[id] = m

		// Validate dependencies inside manifest
		seenDeps := make(map[string]struct{}, len(manifest.Dependencies))
		var cleanDeps []string
		for _, dep := range manifest.Dependencies {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if dep == id {
				return nil, fmt.Errorf("module %q cannot depend on itself", id)
			}
			if _, exists := seenDeps[dep]; exists {
				return nil, fmt.Errorf("module %q has duplicate dependency %q", id, dep)
			}
			seenDeps[dep] = struct{}{}
			cleanDeps = append(cleanDeps, dep)
		}
		// Sort dependencies deterministically
		sort.Strings(cleanDeps)
		deps[id] = cleanDeps

		seenConflicts := make(map[string]struct{}, len(manifest.Conflicts))
		var cleanConflicts []string
		for _, conflict := range manifest.Conflicts {
			conflict = strings.TrimSpace(conflict)
			if conflict == "" {
				continue
			}
			if conflict == id {
				return nil, fmt.Errorf("module %q cannot conflict with itself", id)
			}
			if _, exists := seenConflicts[conflict]; exists {
				return nil, fmt.Errorf("module %q has duplicate conflict %q", id, conflict)
			}
			seenConflicts[conflict] = struct{}{}
			cleanConflicts = append(cleanConflicts, conflict)
		}
		sort.Strings(cleanConflicts)
		conflicts[id] = cleanConflicts
	}

	allIDs := make([]string, 0, len(modules))
	for id := range byID {
		allIDs = append(allIDs, id)
	}
	sort.Strings(allIDs)

	// 2. Validate all declared dependencies and active conflicts.
	for _, id := range allIDs {
		depList := deps[id]
		for _, dep := range depList {
			if _, exists := byID[dep]; !exists {
				return nil, fmt.Errorf("module %q depends on unknown module %q", id, dep)
			}
		}
	}
	for _, id := range allIDs {
		conflictList := conflicts[id]
		for _, conflict := range conflictList {
			if _, exists := byID[conflict]; exists {
				return nil, fmt.Errorf("module %q conflicts with module %q", id, conflict)
			}
		}
	}

	// 3. Cycle detection using Depth-First Search with recursion stack tracking
	type visitState int
	const (
		unvisited visitState = iota
		visiting
		visited
	)

	state := make(map[string]visitState, len(modules))
	var path []string

	var dfsCycle func(curr string) error
	dfsCycle = func(curr string) error {
		state[curr] = visiting
		path = append(path, curr)

		for _, next := range deps[curr] {
			if state[next] == visiting {
				// Cycle detected! Construct cycle path string
				cycleStart := -1
				for i, p := range path {
					if p == next {
						cycleStart = i
						break
					}
				}
				cycleNodes := append(path[cycleStart:], next)
				return fmt.Errorf("dependency cycle detected: %s", strings.Join(cycleNodes, " -> "))
			}
			if state[next] == unvisited {
				if err := dfsCycle(next); err != nil {
					return err
				}
			}
		}

		path = path[:len(path)-1]
		state[curr] = visited
		return nil
	}

	// Deterministic traversal order for cycle check
	for _, id := range allIDs {
		if state[id] == unvisited {
			if err := dfsCycle(id); err != nil {
				return nil, err
			}
		}
	}

	// 4. Kahn's Algorithm / in-degree for deterministic topological sort
	// Note: in our graph, if module A depends on module B (dep[A] = [B]),
	// then B must be registered BEFORE A.
	// That means edge is B -> A (dependency -> dependent).
	inDegree := make(map[string]int, len(modules))
	dependents := make(map[string][]string, len(modules)) // B -> list of A that depend on B

	for _, id := range allIDs {
		inDegree[id] = len(deps[id])
		for _, dep := range deps[id] {
			dependents[dep] = append(dependents[dep], id)
		}
	}

	// Ready queue contains modules with 0 remaining dependencies
	var queue []string
	for _, id := range allIDs {
		if inDegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	// Sort queue for deterministic tie-breaking
	sort.Strings(queue)

	result := make([]Module, 0, len(modules))
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		result = append(result, byID[curr])

		// For all modules that depend on curr, decrement inDegree
		for _, dep := range dependents[curr] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
		sort.Strings(queue)
	}

	if len(result) != len(modules) {
		return nil, fmt.Errorf("unresolved dependencies in topological sort: resolved %d of %d modules", len(result), len(modules))
	}

	return result, nil
}
