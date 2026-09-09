package runtime

import (
	"context"
)

const (
	HealthHealthy   = "healthy"
	HealthDegraded  = "degraded"
	HealthUnhealthy = "unhealthy"
)

// ComponentHealth describes the health condition of a single component.
type ComponentHealth struct {
	Status  string
	Details string
	Error   error
}

// Component defines the lifecycle and dependency contract for all runtime-managed components.
type Component interface {
	// Name returns the unique identifier for the component.
	Name() string

	// Dependencies returns the names of other components that must start before this component.
	Dependencies() []string

	// Start starts the component using the provided context.
	Start(ctx context.Context) error

	// Stop gracefully stops the component using the provided context.
	Stop(ctx context.Context) error

	// Health probes the health status of the component.
	Health(ctx context.Context) ComponentHealth
}

// CriticalComponent can be implemented by components to specify whether startup
// failure should abort the runtime (critical) or only log/mark degraded (optional).
// Default is critical if not implemented.
type CriticalComponent interface {
	Component
	IsCritical() bool
}
