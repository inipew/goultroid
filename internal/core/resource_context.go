package core

import (
	"context"
	"strings"
)

type heldResourcesContextKey struct{}

type heldResources struct {
	names map[string]struct{}
}

// WithHeldResource marks ctx as already holding an authoritative TaskEngine
// resource lease. The marker is immutable and safe to share across goroutines.
func WithHeldResource(ctx context.Context, name string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ctx
	}

	names := map[string]struct{}{name: {}}
	if existing, ok := ctx.Value(heldResourcesContextKey{}).(heldResources); ok {
		names = make(map[string]struct{}, len(existing.names)+1)
		for resource := range existing.names {
			names[resource] = struct{}{}
		}
		names[name] = struct{}{}
	}
	return context.WithValue(ctx, heldResourcesContextKey{}, heldResources{names: names})
}

// HasHeldResource reports whether ctx already holds the named authoritative
// resource lease.
func HasHeldResource(ctx context.Context, name string) bool {
	if ctx == nil {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	resources, ok := ctx.Value(heldResourcesContextKey{}).(heldResources)
	if !ok {
		return false
	}
	_, ok = resources.names[name]
	return ok
}
