package app

import (
	"context"
	"sort"
	"time"

	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/scheduler"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/media"
	processSvc "github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
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
	Storage              storage.Diagnostics
	Jobs                 jobs.Diagnostics
	Scheduler            scheduler.Diagnostics
	PersistencePump      jobs.PersistencePumpStats
	LifecycleCallbacks   runtime.CallbackExecutorStats
	TaskEngine           taskengine.RuntimeStats
	TaskEngineSnapshotOK bool
	RPC                  telegram.RPCMetricsSnapshot
	DB                   DBDiagnostics
	ResolverCacheCount   int
	PeerStorageCache     telegram.PeerStorageCacheStats
	Workers              []runtime.WorkerSnapshot
	RuntimeComponents    []runtime.ComponentSnapshot
	StartupFailures      []runtime.StartupFailureSnapshot
	LastShutdownReport   runtime.ShutdownReport
}

// ResourceOwnerDiagnostics describes active resource consumption for a single owner.
type ResourceOwnerDiagnostics struct {
	Owner        string         `json:"owner"`
	TotalActive  int            `json:"total_active"`
	CountsByType map[string]int `json:"counts_by_type,omitempty"`
	Leaked       int            `json:"leaked,omitempty"`
}

type ResourceDiagnostics struct {
	TotalActive int                        `json:"total_active"`
	Leaked      int                        `json:"leaked"`
	ByOwner     []ResourceOwnerDiagnostics `json:"by_owner,omitempty"`
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
	if a.runtime != nil {
		snapshot.Uptime = a.runtime.Uptime()
	} else if a.state() == lifecycleRunning && !a.startTime.IsZero() {
		d := time.Since(a.startTime)
		if d > 0 {
			snapshot.Uptime = d
		}
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
		snapshot.Scheduler = a.sched.Diagnostics()
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
				resList := scope.Resources()
				copiedRes := make([]plugin.Resource, len(resList))
				for i, res := range resList {
					var metaCopy map[string]string
					if len(res.Metadata) > 0 {
						metaCopy = make(map[string]string, len(res.Metadata))
						for mk, mv := range res.Metadata {
							metaCopy[mk] = mv
						}
					}
					res.Metadata = metaCopy
					copiedRes[i] = res
				}
				sort.Slice(copiedRes, func(i, j int) bool { return copiedRes[i].ID < copiedRes[j].ID })
				entry.Resources = copiedRes
			}
			snapshot.Plugins = append(snapshot.Plugins, entry)
		}
		sort.Slice(snapshot.Plugins, func(i, j int) bool { return snapshot.Plugins[i].Name < snapshot.Plugins[j].Name })
	}
	if a.inlineEngine != nil {
		snapshot.Inline = a.inlineEngine.RuntimeStats()
	}
	if a.resources != nil {
		owners := a.resources.AllSnapshots()
		sort.Slice(owners, func(i, j int) bool { return owners[i].Owner < owners[j].Owner })
		snapshot.Resources.ByOwner = make([]ResourceOwnerDiagnostics, 0, len(owners))
		for _, owner := range owners {
			snapshot.Resources.TotalActive += owner.TotalActive
			snapshot.Resources.Leaked += owner.Leaked

			var countsCopy map[string]int
			if len(owner.CountsByType) > 0 {
				countsCopy = make(map[string]int, len(owner.CountsByType))
				for k, v := range owner.CountsByType {
					countsCopy[k] = v
				}
			}
			snapshot.Resources.ByOwner = append(snapshot.Resources.ByOwner, ResourceOwnerDiagnostics{
				Owner:        owner.Owner,
				TotalActive:  owner.TotalActive,
				CountsByType: countsCopy,
				Leaked:       owner.Leaked,
			})
		}
	}
	if a.media != nil {
		snapshot.Media = a.media.Diagnostics()
	}
	if a.processRunner != nil {
		snapshot.Process = a.processRunner.Diagnostics()
	}
	var store storage.Storage = a.storage
	if store == nil && a.media != nil {
		store = a.media.Storage()
	}
	if dp, ok := store.(storage.DiagnosticsProvider); ok {
		snapshot.Storage = dp.Diagnostics()
	}
	if a.jobs != nil {
		snapshot.Jobs = a.jobs.Diagnostics()
	}
	if a.persistencePump != nil {
		snapshot.PersistencePump = a.persistencePump.Stats()
	}
	if a.taskEngine != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		if stats, err := a.taskEngine.Stats(ctx); err == nil {
			var poolsCopy map[tasks.PoolID]taskengine.PoolRuntimeStats
			if len(stats.Pools) > 0 {
				poolsCopy = make(map[tasks.PoolID]taskengine.PoolRuntimeStats, len(stats.Pools))
				for k, v := range stats.Pools {
					poolsCopy[k] = v
				}
			}
			stats.Pools = poolsCopy

			var resCopy map[string]taskengine.ResourceRuntimeStats
			if len(stats.Resources) > 0 {
				resCopy = make(map[string]taskengine.ResourceRuntimeStats, len(stats.Resources))
				for k, v := range stats.Resources {
					resCopy[k] = v
				}
			}
			stats.Resources = resCopy

			snapshot.TaskEngine = stats
			snapshot.TaskEngineSnapshotOK = true
			if interactivePool, ok := stats.Pools["interactive"]; ok && interactivePool.MaxWorkers > 0 {
				snapshot.Commands.Capacity = interactivePool.MaxWorkers
			}
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
		rtDiag := a.runtime.Diagnostics()
		snapshot.RuntimeComponents = rtDiag.Components
		snapshot.StartupFailures = rtDiag.StartupFailures
	}
	return snapshot
}
