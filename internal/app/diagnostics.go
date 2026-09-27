package app

import (
	"context"
	"sort"
	"time"

	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/media"
	processSvc "github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/telegram"
)

// DiagnosticsSnapshot is a read-only view of runtime coordination state.
// It intentionally contains metadata and counters, not mutable subsystem
// references.
type DiagnosticsSnapshot struct {
	Lifecycle            string
	Uptime               time.Duration
	ProcessMemory        ProcessMemoryDiagnostics
	EventBus             EventBusDiagnostics
	Interaction          interaction.Stats
	Inline               inline.RuntimeStats
	Resources            ResourceDiagnostics
	Commands             CommandDiagnostics
	PeriodicTasks        []PeriodicTaskDiagnostics
	Plugins              []PluginDiagnostics
	Media                media.DiagnosticsSnapshot
	Process              processSvc.DiagnosticsSnapshot
	Jobs                 jobs.Diagnostics
	PersistencePanics    uint64
	PersistencePump      jobs.PersistencePumpStats
	LifecycleCallbacks   runtime.CallbackExecutorStats
	TaskEngine           taskengine.RuntimeStats
	TaskEngineSnapshotOK bool
	RPC                  telegram.RPCMetricsSnapshot
	DB                   DBDiagnostics
	ResolverCacheCount   int
	PeerStorageCache     telegram.PeerStorageCacheStats
	Workers              []runtime.WorkerSnapshot
	LastShutdownReport   runtime.ShutdownReport
}

type ResourceDiagnostics struct {
	TotalActive int
	Leaked      int
}

type DBDiagnostics struct {
	MaxOpenConnections int
	OpenConnections    int
	InUse              int
	Idle               int
	WaitCount          int64
	WaitDuration       time.Duration
	MaxIdleClosed      int64
	MaxLifetimeClosed  int64
}

type EventBusDiagnostics struct {
	Published      int64
	Delivered      int64
	Dropped        int64
	Panics         int64
	Subscriptions  int
	QueueDepth     int
	QueueCapacity  int
	ActiveWorkers  int
	OrderedWorkers int
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
	snapshot := DiagnosticsSnapshot{Lifecycle: a.LifecycleState(), ProcessMemory: processMemoryDiagnostics()}
	if !a.startTime.IsZero() {
		snapshot.Uptime = time.Since(a.startTime)
	}
	if a.eventBus != nil {
		stats := a.eventBus.Stats()
		snapshot.EventBus = EventBusDiagnostics{
			Published:      stats.Published,
			Delivered:      stats.Delivered,
			Dropped:        stats.Dropped,
			Panics:         stats.Panics,
			Subscriptions:  a.eventBus.SubscriptionCount(""),
			QueueDepth:     stats.QueueDepth,
			QueueCapacity:  stats.QueueCapacity,
			ActiveWorkers:  stats.ActiveWorkers,
			OrderedWorkers: stats.OrderedWorkers,
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
		if interactions := a.plugins.InteractionRuntime(); interactions != nil {
			snapshot.Interaction = interactions.SnapshotStats()
		}
		snapshot.LifecycleCallbacks = a.plugins.CleanupStats()
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
	if a.inlineEngine != nil {
		snapshot.Inline = a.inlineEngine.RuntimeStats()
	}
	if a.resources != nil {
		for _, owner := range a.resources.AllSnapshots() {
			snapshot.Resources.TotalActive += owner.TotalActive
			snapshot.Resources.Leaked += owner.Leaked
		}
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
	if a.persistencePump != nil {
		snapshot.PersistencePanics = a.persistencePump.Panics()
		snapshot.PersistencePump = a.persistencePump.Stats()
	}
	if a.taskEngine != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		if stats, err := a.taskEngine.Stats(ctx); err == nil {
			snapshot.TaskEngine = stats
			snapshot.TaskEngineSnapshotOK = true
		}
		cancel()
	}
	if a.client != nil {
		snapshot.RPC = a.client.RPCMetrics()
		snapshot.ResolverCacheCount = a.client.ResolverCacheLen()
		snapshot.PeerStorageCache = a.client.PeerStorageCacheStats()
	}
	if a.db != nil && a.db.DB != nil {
		stats := a.db.DB.Stats()
		snapshot.DB = DBDiagnostics{
			MaxOpenConnections: stats.MaxOpenConnections,
			OpenConnections:    stats.OpenConnections,
			InUse:              stats.InUse,
			Idle:               stats.Idle,
			WaitCount:          stats.WaitCount,
			WaitDuration:       stats.WaitDuration,
			MaxIdleClosed:      stats.MaxIdleClosed,
			MaxLifetimeClosed:  stats.MaxLifetimeClosed,
		}
	}
	if a.supervisor != nil {
		snapshot.Workers = a.supervisor.Snapshot()
	}
	if a.runtime != nil {
		snapshot.LastShutdownReport = a.runtime.LastShutdownReport()
	}
	return snapshot
}
