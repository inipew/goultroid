package app

import (
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/plugins/sysinfo"
)

// sysinfoResourceSnapshot detaches the numeric operator view from app diagnostics.
func (a *App) sysinfoResourceSnapshot() sysinfo.ResourceSnapshot {
	if a == nil {
		return sysinfo.ResourceSnapshot{}
	}
	d := a.Diagnostics()
	process := d.ProcessMemory
	view := sysinfo.ResourceSnapshot{
		ProcessMemory: sysinfo.ProcessMemorySnapshot{
			NumGoroutine: process.NumGoroutine,
			HeapAlloc:    process.HeapAlloc,
			HeapInuse:    process.HeapInuse,
			HeapIdle:     process.HeapIdle,
			HeapReleased: process.HeapReleased,
			HeapObjects:  process.HeapObjects,
			StackInuse:   process.StackInuse,
			StackSys:     process.StackSys,
			Sys:          process.Sys,
			NextGC:       process.NextGC,
			NumGC:        process.NumGC,
			RSSBytes:     process.RSSBytes,
			RSSAvailable: process.RSSAvailable,
		},
		TaskEngine:          d.TaskEngine,
		TaskEngineAvailable: d.TaskEngineSnapshotOK,
		EventBus: core.EventBusStats{
			Published:      d.EventBus.Published,
			Delivered:      d.EventBus.Delivered,
			Dropped:        d.EventBus.Dropped,
			Panics:         d.EventBus.Panics,
			QueueDepth:     d.EventBus.QueueDepth,
			QueueCapacity:  d.EventBus.QueueCapacity,
			ActiveWorkers:  d.EventBus.ActiveWorkers,
			OrderedWorkers: d.EventBus.OrderedWorkers,
		},
		Persistence:        d.PersistencePump,
		Jobs:               d.Jobs,
		Interaction:        d.Interaction,
		Inline:             d.Inline,
		ResourceActive:     d.Resources.TotalActive,
		ResourceLeaked:     d.Resources.Leaked,
		DBOpen:             d.DB.OpenConnections,
		DBInUse:            d.DB.InUse,
		DBIdle:             d.DB.Idle,
		ResolverCacheCount: d.ResolverCacheCount,
		PeerCacheEntries:   d.PeerStorageCache.PeerEntries,
		PeerCacheBytes:     d.PeerStorageCache.EntityBytes,
		RPCTotalRequests:   d.RPC.TotalRequests,
		RPCFloodWaits:      d.RPC.FloodWaitCount,
	}
	view.TaskEngine.Pools = make(map[tasks.PoolID]taskengine.PoolRuntimeStats, len(d.TaskEngine.Pools))
	for id, stats := range d.TaskEngine.Pools {
		view.TaskEngine.Pools[id] = stats
	}
	view.TaskEngine.Resources = make(map[string]taskengine.ResourceRuntimeStats, len(d.TaskEngine.Resources))
	for name, stats := range d.TaskEngine.Resources {
		view.TaskEngine.Resources[name] = stats
	}
	return view
}

func (a *App) wireSysinfoDiagnostics() {
	if a == nil || a.plugins == nil {
		return
	}
	registered, ok := a.plugins.Find("sysinfo")
	if !ok {
		return
	}
	if p, ok := registered.(*sysinfo.Plugin); ok {
		p.SetDiagnosticsProvider(a.sysinfoResourceSnapshot)
	}
}
