package app

import (
	"sort"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/media"
	processSvc "github.com/inipew/goultroid/internal/services/process"
)

// DiagnosticsSnapshot is a read-only view of runtime coordination state.
// It intentionally contains metadata and counters, not mutable subsystem
// references.
type DiagnosticsSnapshot struct {
	Lifecycle     string
	Uptime        time.Duration
	EventBus      EventBusDiagnostics
	Commands      CommandDiagnostics
	PeriodicTasks []PeriodicTaskDiagnostics
	Plugins       []PluginDiagnostics
	Media         media.DiagnosticsSnapshot
	Process       processSvc.DiagnosticsSnapshot
	Jobs          jobs.Diagnostics
}

type EventBusDiagnostics struct {
	Published     int64
	Delivered     int64
	Dropped       int64
	Panics        int64
	Subscriptions int
	QueueDepth    int
	QueueCapacity int
}

type CommandDiagnostics struct {
	Capacity int
	Active   int
	Total    int64
}

type PeriodicTaskDiagnostics struct {
	Owner     string
	Name      string
	Runs      int64
	Failures  int64
	LastRunAt time.Time
	LastError string
}

type PluginDiagnostics struct {
	Name      string
	Resources []plugin.Resource
}

// Diagnostics returns a consistent snapshot of the app-owned runtime
// subsystems. It is safe to call while the application is running or stopping.
func (a *App) Diagnostics() DiagnosticsSnapshot {
	snapshot := DiagnosticsSnapshot{Lifecycle: a.LifecycleState()}
	if !a.startTime.IsZero() {
		snapshot.Uptime = time.Since(a.startTime)
	}
	if a.eventBus != nil {
		stats := a.eventBus.Stats()
		snapshot.EventBus = EventBusDiagnostics{
			Published:     stats.Published,
			Delivered:     stats.Delivered,
			Dropped:       stats.Dropped,
			Panics:        stats.Panics,
			Subscriptions: a.eventBus.SubscriptionCount(""),
			QueueDepth:    stats.QueueDepth,
			QueueCapacity: stats.QueueCapacity,
		}
	}
	if a.client != nil && a.client.Dispatcher() != nil {
		commands := a.client.Dispatcher().CommandConcurrency()
		snapshot.Commands = CommandDiagnostics{Capacity: commands.Capacity, Active: commands.Active, Total: commands.Total}
	}
	if a.sched != nil {
		for _, task := range a.sched.PeriodicTaskSnapshots() {
			snapshot.PeriodicTasks = append(snapshot.PeriodicTasks, PeriodicTaskDiagnostics{
				Owner: task.Owner, Name: task.Name, Runs: task.Runs,
				Failures: task.Failures, LastRunAt: task.LastRunAt, LastError: task.LastError,
			})
		}
		sort.Slice(snapshot.PeriodicTasks, func(i, j int) bool {
			return snapshot.PeriodicTasks[i].Name < snapshot.PeriodicTasks[j].Name
		})
	}
	if a.plugins != nil {
		for _, p := range a.plugins.Plugins() {
			entry := PluginDiagnostics{Name: p.Name()}
			if scope, ok := a.plugins.Scope(p.Name()); ok && scope != nil {
				entry.Resources = scope.Resources()
				sort.Slice(entry.Resources, func(i, j int) bool { return entry.Resources[i].ID < entry.Resources[j].ID })
			}
			snapshot.Plugins = append(snapshot.Plugins, entry)
		}
		sort.Slice(snapshot.Plugins, func(i, j int) bool { return snapshot.Plugins[i].Name < snapshot.Plugins[j].Name })
	}
	if a.media != nil {
		snapshot.Media = a.media.Diagnostics()
	}
	if a.processRunner != nil {
		snapshot.Process = a.processRunner.Diagnostics()
	}
	if a.jobs != nil {
		snapshot.Jobs = a.jobs.Diagnostics()
	}
	return snapshot
}
