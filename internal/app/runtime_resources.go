package app

import (
	"context"
	"strings"

	"github.com/inipew/goultroid/internal/runtime"
)

// dependencyComponent embeds a runtime.Component to apply composition-root dependencies
// while preserving subsystem isolation.
//
// Additive Dependency Contract:
//
//  1. Intrinsic Dependencies (Component.Dependencies()):
//     Subsystem packages declare the foundational components their domain logic
//     strictly requires to execute (e.g., jobs requires taskengine; dispatcher requires
//     eventbus and taskengine). This allows standalone packages to be tested in
//     isolation without knowing about the wider application wiring.
//
//  2. Composition-Level Dependencies (dependencyComponent.dependencies):
//     The composition root (internal/app) binds cross-cutting concerns, ordering
//     rules, and feature-level wiring (e.g., wiring dispatcher to settings-live,
//     or plugins to database and addon-runtimes).
//
//  3. Additive Resolution and Determinism:
//     dependencyComponent merges composition-level edges with intrinsic edges,
//     preserving declared order (composition edges first, followed by any unlisted
//     intrinsic edges), deduplicating identifiers, and trimming whitespace. This
//     guarantees that:
//     - No intrinsic prerequisite is ever dropped by composition-level omissions.
//     - The composition root remains the authoritative wiring plane.
//     - Graph traversal and topological sorting remain deterministic.
type dependencyComponent struct {
	runtime.Component
	dependencies []string
}

func (c dependencyComponent) Dependencies() []string {
	seen := make(map[string]struct{}, len(c.dependencies))
	var result []string
	for _, d := range c.dependencies {
		d = strings.TrimSpace(d)
		if d != "" {
			if _, exists := seen[d]; !exists {
				seen[d] = struct{}{}
				result = append(result, d)
			}
		}
	}
	if c.Component != nil {
		for _, d := range c.Component.Dependencies() {
			d = strings.TrimSpace(d)
			if d != "" {
				if _, exists := seen[d]; !exists {
					seen[d] = struct{}{}
					result = append(result, d)
				}
			}
		}
	}
	return result
}

func (c dependencyComponent) Quiesce(ctx context.Context) error {
	if component, ok := c.Component.(runtime.Quiescer); ok {
		return component.Quiesce(ctx)
	}
	return nil
}

func (c dependencyComponent) Drain(ctx context.Context) error {
	if component, ok := c.Component.(runtime.Drainer); ok {
		return component.Drain(ctx)
	}
	return nil
}

// ForceStop preserves emergency lifecycle capability across the composition
// wrapper. Components that do not expose an explicit forced path remain a
// no-op here; Runtime must never call an arbitrary potentially blocking Stop
// after the graceful budget has expired.
func (c dependencyComponent) ForceStop(ctx context.Context) error {
	if component, ok := c.Component.(runtime.ForcedStopper); ok {
		return component.ForceStop(ctx)
	}
	return nil
}

// IsCritical preserves optional component contract across the composition wrapper.
// If the wrapped component defines IsCritical(), its value is forwarded; otherwise
// defaults to true (critical).
func (c dependencyComponent) IsCritical() bool {
	if component, ok := c.Component.(runtime.CriticalComponent); ok {
		return component.IsCritical()
	}
	return true
}

type resourceComponent struct {
	name         string
	dependencies []string
	start        func(context.Context) error
	stop         func() error
	stopContext  func(context.Context) error
}

func (c resourceComponent) Name() string { return c.name }
func (c resourceComponent) Dependencies() []string {
	return append([]string(nil), c.dependencies...)
}
func (c resourceComponent) Start(ctx context.Context) error {
	if c.start != nil {
		return c.start(ctx)
	}
	return nil
}
func (c resourceComponent) Health(context.Context) runtime.ComponentHealth {
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}
func (c resourceComponent) Stop(ctx context.Context) error {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if c.stopContext != nil {
		return c.stopContext(ctx)
	}
	if c.stop == nil {
		return nil
	}
	return c.stop()
}
