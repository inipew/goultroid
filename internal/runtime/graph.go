package runtime

import (
	"context"
	"fmt"
	"strings"
)

// DependencyGraph manages component dependencies, cycle detection, and execution ordering.
type DependencyGraph struct {
	components map[string]Component
}

// NewDependencyGraph creates an empty dependency graph.
func NewDependencyGraph() *DependencyGraph {
	return &DependencyGraph{
		components: make(map[string]Component),
	}
}

// Add adds a component to the graph. Returns an error if a component with the same name already exists.
func (g *DependencyGraph) Add(c Component) error {
	if c == nil {
		return fmt.Errorf("cannot add nil component")
	}
	name := strings.TrimSpace(c.Name())
	if name == "" {
		return fmt.Errorf("component name cannot be empty")
	}
	if _, exists := g.components[name]; exists {
		return fmt.Errorf("component %q already registered", name)
	}
	g.components[name] = c
	return nil
}

// Get returns the component with the given name, if found.
func (g *DependencyGraph) Get(name string) (Component, bool) {
	c, ok := g.components[name]
	return c, ok
}

// Validate checks that all declared dependencies exist and there are no cyclic dependencies.
func (g *DependencyGraph) Validate() error {
	// Check for missing dependencies
	for name, comp := range g.components {
		for _, dep := range comp.Dependencies() {
			dep = strings.TrimSpace(dep)
			if _, exists := g.components[dep]; !exists {
				return fmt.Errorf("component %q depends on missing component %q", name, dep)
			}
			if dep == name {
				return fmt.Errorf("component %q cannot depend on itself", name)
			}
		}
	}

	// Detect cycles via topological sort
	_, err := g.StartupOrder()
	return err
}

// StartupOrder returns the components topologically sorted such that dependencies appear before dependents.
func (g *DependencyGraph) StartupOrder() ([]Component, error) {
	// Kahn's algorithm
	inDegree := make(map[string]int)
	adjList := make(map[string][]string) // dep -> list of components that depend on it

	for name := range g.components {
		inDegree[name] = 0
		adjList[name] = nil
	}

	for name, comp := range g.components {
		deps := comp.Dependencies()
		for _, dep := range deps {
			dep = strings.TrimSpace(dep)
			if _, exists := g.components[dep]; !exists {
				return nil, fmt.Errorf("component %q depends on missing component %q", name, dep)
			}
			adjList[dep] = append(adjList[dep], name)
			inDegree[name]++
		}
	}

	var queue []string
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}

	var ordered []Component
	visitedCount := 0

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		visitedCount++
		ordered = append(ordered, g.components[curr])

		for _, dependent := range adjList[curr] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if visitedCount != len(g.components) {
		// Cycle detected! Trace cycle
		var cycleNodes []string
		for name, deg := range inDegree {
			if deg > 0 {
				cycleNodes = append(cycleNodes, name)
			}
		}
		return nil, fmt.Errorf("cyclic dependency detected among components: %v", cycleNodes)
	}

	return ordered, nil
}

// ShutdownOrder returns the reverse of StartupOrder so that dependents stop before dependencies.
func (g *DependencyGraph) ShutdownOrder() ([]Component, error) {
	startOrder, err := g.StartupOrder()
	if err != nil {
		return nil, err
	}

	n := len(startOrder)
	reverse := make([]Component, n)
	for i, c := range startOrder {
		reverse[n-1-i] = c
	}
	return reverse, nil
}

// Rollback stops previously started components in reverse order of startup.
func (g *DependencyGraph) Rollback(ctx context.Context, started []Component) []error {
	var errs []error
	for i := len(started) - 1; i >= 0; i-- {
		comp := started[i]
		if err := comp.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("rollback component %q: %w", comp.Name(), err))
		}
	}
	return errs
}
