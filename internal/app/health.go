package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
)

const (
	HealthHealthy   = "healthy"
	HealthDegraded  = "degraded"
	HealthUnhealthy = "unhealthy"
)

// HealthSnapshot is the operational health/readiness result derived from a
// diagnostics snapshot. Reasons are stable, machine-readable prefixes with a
// short human-readable suffix.
type HealthSnapshot struct {
	Status     string
	Ready      bool
	Reasons    []string
	Subsystems map[string]string
}

// Health evaluates readiness and degradation without probing external systems
// synchronously. External subsystem probes remain responsible for their own
// health contracts and feed counters/state into Diagnostics.
func (a *App) Health() HealthSnapshot {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	return a.HealthContext(ctx)
}

// HealthContext evaluates runtime and external subsystem state with a caller
// supplied timeout. Database and filesystem checks are bounded and read-only.
func (a *App) HealthContext(ctx context.Context) HealthSnapshot {
	if ctx == nil {
		ctx = context.Background()
	}
	diagnostics := a.Diagnostics()
	health := HealthSnapshot{Status: HealthHealthy, Subsystems: make(map[string]string)}
	if diagnostics.Lifecycle == "running" {
		health.Ready = true
	} else {
		health.Ready = false
		health.Reasons = append(health.Reasons, "runtime:not-running:"+diagnostics.Lifecycle)
		if diagnostics.Lifecycle == "failed" || diagnostics.Lifecycle == "stopped" {
			health.Status = HealthUnhealthy
		} else {
			health.Status = HealthDegraded
		}
	}
	if diagnostics.EventBus.Dropped > 0 {
		health.Reasons = append(health.Reasons, "eventbus:dropped-events")
	}
	if a.eventBus != nil {
		switch state := a.eventBus.LifecycleState(); state {
		case "started":
			health.Subsystems["eventbus"] = HealthHealthy
		case "closed":
			health.Subsystems["eventbus"] = HealthUnhealthy
			health.Reasons = append(health.Reasons, "eventbus:closed")
		default:
			health.Subsystems["eventbus"] = HealthDegraded
			health.Reasons = append(health.Reasons, "eventbus:not-started")
		}
	}
	if diagnostics.EventBus.QueueCapacity > 0 {
		utilization := diagnostics.EventBus.QueueDepth * 100 / diagnostics.EventBus.QueueCapacity
		if utilization >= 100 {
			health.Reasons = append(health.Reasons, "eventbus:queue-full")
		} else if utilization >= 80 {
			health.Reasons = append(health.Reasons, "eventbus:queue-pressure-high")
		}
	}
	if diagnostics.Commands.Capacity > 0 && diagnostics.Commands.Active >= diagnostics.Commands.Capacity {
		health.Subsystems["commands"] = HealthDegraded
		health.Reasons = append(health.Reasons, "commands:concurrency-saturated")
	} else if diagnostics.Commands.Capacity > 0 {
		health.Subsystems["commands"] = HealthHealthy
	}
	if a.db != nil {
		if err := a.db.PingContext(ctx); err != nil {
			health.Subsystems["database"] = "unhealthy"
			health.Reasons = append(health.Reasons, "database:unhealthy:"+err.Error())
		} else {
			health.Subsystems["database"] = HealthHealthy
		}
	}
	if a.client != nil {
		if a.client.IsReady() {
			health.Subsystems["telegram"] = HealthHealthy
		} else {
			health.Subsystems["telegram"] = HealthDegraded
			health.Ready = false
			health.Reasons = append(health.Reasons, "telegram:not-ready")
		}
	}
	if a.sched != nil {
		if a.sched.IsRunning() {
			health.Subsystems["scheduler"] = HealthHealthy
		} else {
			health.Subsystems["scheduler"] = HealthDegraded
			health.Reasons = append(health.Reasons, "scheduler:not-running")
		}
	}
	if a.processRunner != nil {
		process := a.processRunner.Diagnostics()
		if process.Capacity > 0 && process.Active >= process.Capacity {
			health.Subsystems["process"] = HealthDegraded
			health.Reasons = append(health.Reasons, "process:concurrency-saturated")
		} else {
			health.Subsystems["process"] = HealthHealthy
		}
	}
	if a.media != nil && a.media.Storage() != nil {
		basePath := a.media.Storage().BasePath()
		if basePath != "" {
			if _, err := os.Stat(basePath); err != nil {
				health.Subsystems["filesystem"] = "unhealthy"
				health.Reasons = append(health.Reasons, fmt.Sprintf("filesystem:unhealthy:%v", err))
			} else {
				health.Subsystems["filesystem"] = HealthHealthy
			}
		}
	}
	if diagnostics.EventBus.Panics > 0 {
		health.Reasons = append(health.Reasons, "eventbus:handler-panics")
	}
	for _, task := range diagnostics.PeriodicTasks {
		if task.Failures > 0 {
			health.Reasons = append(health.Reasons, "scheduler:task-failures:"+task.Name)
		}
	}
	if diagnostics.Lifecycle == "stopped" || diagnostics.Lifecycle == "failed" {
		for _, plugin := range diagnostics.Plugins {
			if len(plugin.Resources) > 0 {
				health.Reasons = append(health.Reasons, "plugin:resource-leak:"+plugin.Name)
			}
		}
		if len(diagnostics.Media.ActiveTempDirectories) > 0 {
			health.Reasons = append(health.Reasons, "media:temporary-resource-leak")
		}
	}
	if a.workers != nil {
		wHealth := a.workers.Health(ctx)
		switch wHealth.Status {
		case runtime.HealthHealthy:
			health.Subsystems["workers"] = HealthHealthy
		case runtime.HealthDegraded:
			health.Subsystems["workers"] = HealthDegraded
			health.Reasons = append(health.Reasons, "workers:degraded:"+wHealth.Details)
		case runtime.HealthUnhealthy:
			health.Subsystems["workers"] = HealthUnhealthy
			health.Reasons = append(health.Reasons, "workers:unhealthy:"+wHealth.Details)
		}
	}
	if a.resources != nil {
		leaks := a.resources.AllSnapshots()
		for _, snap := range leaks {
			if snap.Leaked > 0 {
				health.Reasons = append(health.Reasons, fmt.Sprintf("resources:leak:%s:%d", snap.Owner, snap.Leaked))
				if health.Status == HealthHealthy {
					health.Status = HealthDegraded
				}
			}
		}
	}
	if len(health.Reasons) > 0 && health.Status == HealthHealthy {
		health.Status = HealthDegraded
	}
	return health
}

// Healthy reports whether the current status is healthy.
func (h HealthSnapshot) Healthy() bool { return h.Status == HealthHealthy }

// HasReason reports whether a reason prefix is present.
func (h HealthSnapshot) HasReason(prefix string) bool {
	for _, reason := range h.Reasons {
		if strings.HasPrefix(reason, prefix) {
			return true
		}
	}
	return false
}
