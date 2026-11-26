package app

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/services/storage"
)

const (
	HealthHealthy   = "healthy"
	HealthDegraded  = "degraded"
	HealthUnhealthy = "unhealthy"
	HealthUnknown   = "unknown"
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

// sanitizeHealthIdentifier normalizes an arbitrary identifier (such as a task,
// plugin, or owner name) into a stable, bounded, machine-readable label.
// It strips punctuation, limits length to 48 chars, and prevents raw errors,
// tokens, paths, URLs, or payloads from entering health reasons.
func sanitizeHealthIdentifier(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	if idx := strings.Index(s, "("); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}
	if idx := strings.Index(s, "?"); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}
	var b strings.Builder
	b.Grow(len(s))
	lastDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	res := strings.Trim(b.String(), "-")
	if len(res) > 48 {
		res = strings.TrimRight(res[:48], "-")
	}
	if res == "" {
		return "unknown"
	}
	return res
}

// fsProbeSlot enforces a hard concurrency bound of at most 1 active filesystem probe goroutine.
// When full, additional probe calls do not spawn goroutines and return a bounded capacity status.
var fsProbeSlot = make(chan struct{}, 1)

var (
	fsProbeMu     sync.RWMutex
	fsProbeStatFn = os.Stat
)

func getFSProbeStatFn() func(string) (os.FileInfo, error) {
	fsProbeMu.RLock()
	defer fsProbeMu.RUnlock()
	return fsProbeStatFn
}

func setFSProbeStatFn(fn func(string) (os.FileInfo, error)) func() {
	fsProbeMu.Lock()
	orig := fsProbeStatFn
	fsProbeStatFn = fn
	fsProbeMu.Unlock()
	return func() {
		fsProbeMu.Lock()
		fsProbeStatFn = orig
		fsProbeMu.Unlock()
	}
}

func probeFilesystem(ctx context.Context, basePath string) (string, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return HealthUnknown, "filesystem:probe-timeout"
	}

	select {
	case fsProbeSlot <- struct{}{}:
	default:
		// Hard capacity limit: another probe is already running/hung.
		// Enforce capacity policy: do NOT spawn a new goroutine.
		return HealthUnknown, "filesystem:probe-saturated"
	}

	statFn := getFSProbeStatFn()
	done := make(chan error, 1)
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { <-fsProbeSlot })
	}
	go func() {
		defer release()
		_, err := statFn(basePath)
		release()
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			return HealthUnhealthy, "filesystem:unavailable"
		}
		return HealthHealthy, ""
	case <-ctx.Done():
		return HealthUnknown, "filesystem:probe-timeout"
	}
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
		health.Reasons = append(health.Reasons, "runtime:not-running:"+sanitizeHealthIdentifier(diagnostics.Lifecycle))
		if diagnostics.Lifecycle == "failed" || diagnostics.Lifecycle == "stopped" {
			health.Status = HealthUnhealthy
		} else {
			health.Status = HealthDegraded
		}
	}
	if diagnostics.EventBus.Dropped > 0 {
		health.Reasons = append(health.Reasons, "eventbus:dropped-events")
	}
	if diagnostics.EventBus.Panics > 0 {
		health.Reasons = append(health.Reasons, "eventbus:handler-panics")
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
	if a.processRunner != nil {
		process := a.processRunner.Diagnostics()
		if process.Capacity > 0 && process.Active >= process.Capacity {
			health.Subsystems["process"] = HealthDegraded
			health.Reasons = append(health.Reasons, "process:concurrency-saturated")
		} else {
			health.Subsystems["process"] = HealthHealthy
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

	// Subsystems managed by Runtime are queried via Runtime.Health when runtime is present.
	// When runtime is nil (e.g. lightweight isolated testing), evaluate fallback state directly.
	if a.runtime == nil {
		if a.db != nil {
			if err := a.db.PingContext(ctx); err != nil {
				health.Subsystems["database"] = HealthUnhealthy
				health.Status = HealthUnhealthy
				health.Ready = false
				health.Reasons = append(health.Reasons, "database:ping-failed")
			} else {
				health.Subsystems["database"] = HealthHealthy
			}
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
		if a.sched != nil {
			if a.sched.IsRunning() {
				health.Subsystems["scheduler"] = HealthHealthy
			} else {
				health.Subsystems["scheduler"] = HealthDegraded
				health.Reasons = append(health.Reasons, "scheduler:not-running")
			}
		}
	}

	// Storage and filesystem probes: evaluate owner in-memory state and physical mount
	// independently without allowing physical probe to overwrite storage degradation.
	var store storage.Storage = a.storage
	if store == nil && a.media != nil {
		store = a.media.Storage()
	}
	if store != nil {
		basePath := store.BasePath()

		// 1. Storage owner state (in-memory snapshot, lock-free, zero I/O)
		if sh, ok := store.(interface{ StorageHealth() (string, string) }); ok {
			status, reason := sh.StorageHealth()
			health.Subsystems["storage"] = status
			if reason != "" {
				health.Reasons = append(health.Reasons, reason)
			}
			if status == HealthUnhealthy || status == HealthDegraded {
				if health.Status == HealthHealthy {
					health.Status = HealthDegraded
				}
			}
		} else {
			health.Subsystems["storage"] = HealthHealthy
		}

		// 2. Physical filesystem path probe with hard concurrency limit
		if basePath == "" || basePath == "memory://" {
			health.Subsystems["filesystem"] = HealthHealthy
		} else {
			status, reason := probeFilesystem(ctx, basePath)
			health.Subsystems["filesystem"] = status
			if reason != "" {
				health.Reasons = append(health.Reasons, reason)
			}
			if status == HealthUnhealthy || status == HealthUnknown {
				if health.Status == HealthHealthy {
					health.Status = HealthDegraded
				}
			}
		}
	}
	for _, task := range diagnostics.PeriodicTasks {
		if task.Failures > 0 {
			health.Reasons = append(health.Reasons, "scheduler:task-failures:"+sanitizeHealthIdentifier(task.Name))
		}
	}
	if diagnostics.Lifecycle == "stopped" || diagnostics.Lifecycle == "failed" {
		for _, plugin := range diagnostics.Plugins {
			if len(plugin.Resources) > 0 {
				health.Reasons = append(health.Reasons, "plugin:resource-leak:"+sanitizeHealthIdentifier(plugin.Name))
			}
		}
		if len(diagnostics.Media.ActiveTempDirectories) > 0 {
			health.Reasons = append(health.Reasons, "media:temporary-resource-leak")
		}
	}
	if a.runtime != nil {
		rtHealth := a.runtime.Health(ctx)
		for comp, status := range rtHealth.Components {
			health.Subsystems[comp] = status
		}
		health.Reasons = append(health.Reasons, rtHealth.Reasons...)

		// Unified readiness and status projection:
		if !rtHealth.Ready {
			health.Ready = false
		}
		if rtHealth.Status == runtime.HealthUnhealthy {
			health.Status = HealthUnhealthy
		} else if rtHealth.Status == runtime.HealthDegraded && health.Status != HealthUnhealthy {
			health.Status = HealthDegraded
		}
	}
	if a.resources != nil {
		leaks := a.resources.AllSnapshots()
		for _, snap := range leaks {
			if snap.Leaked > 0 {
				health.Reasons = append(health.Reasons, "resources:leak:"+sanitizeHealthIdentifier(snap.Owner))
				if health.Status == HealthHealthy {
					health.Status = HealthDegraded
				}
			}
		}
	}
	if len(health.Reasons) > 0 {
		seen := make(map[string]struct{}, len(health.Reasons))
		deduped := make([]string, 0, len(health.Reasons))
		for _, r := range health.Reasons {
			if _, exists := seen[r]; !exists {
				seen[r] = struct{}{}
				deduped = append(deduped, r)
			}
		}
		health.Reasons = deduped
		if health.Status == HealthHealthy {
			health.Status = HealthDegraded
		}
	}
	if health.Status == HealthUnhealthy {
		health.Ready = false
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
