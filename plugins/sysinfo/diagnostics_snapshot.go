package sysinfo

import (
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/taskengine"
)

// ProcessMemorySnapshot contains process counters used by the operator card.
type ProcessMemorySnapshot struct {
	NumGoroutine int
	HeapAlloc    uint64
	HeapInuse    uint64
	HeapIdle     uint64
	HeapReleased uint64
	HeapObjects  uint64
	StackInuse   uint64
	StackSys     uint64
	Sys          uint64
	NextGC       uint64
	NumGC        uint32
	RSSBytes     uint64
	RSSAvailable bool
}

// ResourceSnapshot is the bounded, numeric view supplied by application wiring.
type ResourceSnapshot struct {
	ProcessMemory       ProcessMemorySnapshot
	TaskEngine          taskengine.RuntimeStats
	TaskEngineAvailable bool
	EventBus            core.EventBusStats
	Persistence         jobs.PersistencePumpStats
	Jobs                jobs.Diagnostics
	Interaction         interaction.Stats
	Inline              inline.RuntimeStats
	ResourceActive      int
	ResourceLeaked      int
	DBOpen              int
	DBInUse             int
	DBIdle              int
	ResolverCacheCount  int
	PeerCacheEntries    int
	PeerCacheBytes      int64
	RPCTotalRequests    int64
	RPCFloodWaits       int64
}

// SetDiagnosticsProvider attaches the app-owned snapshot callback before runtime start.
func (p *Plugin) SetDiagnosticsProvider(provider func() ResourceSnapshot) {
	if p != nil {
		p.diagnosticsProvider = provider
	}
}

// ResourceSnapshot returns one app-owned sample when a provider is installed.
func (p *Plugin) ResourceSnapshot() (ResourceSnapshot, bool) {
	if p == nil || p.diagnosticsProvider == nil {
		return ResourceSnapshot{}, false
	}
	return p.diagnosticsProvider(), true
}
