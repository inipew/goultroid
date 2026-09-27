package sysinfo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestFormatPoolRuntimeStats(t *testing.T) {
	stats := taskengine.PoolRuntimeStats{
		Workers:      8,
		MinWorkers:   2,
		MaxWorkers:   16,
		Idle:         5,
		IdleWorkers:  5,
		Running:      2,
		Dispatching:  1,
		Waiting:      4,
		WaitingBytes: 2048,
	}

	formatted := formatPoolRuntimeStats("interactive", stats)
	expectedTokens := []string{
		"• <b>interactive</b>:",
		"running 2",
		"dispatching 1",
		"idle 5",
		"waiting 4 / 2.0 KB",
		"workers 8 (2-16)",
	}

	for _, token := range expectedTokens {
		if !strings.Contains(formatted, token) {
			t.Errorf("expected formatted stats to contain %q, got %q", token, formatted)
		}
	}
}

func TestDiagnosticsCard_ProviderSnapshot(t *testing.T) {
	p := New()
	calls := 0
	p.SetDiagnosticsProvider(func() ResourceSnapshot {
		calls++
		return ResourceSnapshot{
			ProcessMemory:       ProcessMemorySnapshot{NumGoroutine: 121, HeapAlloc: 1048576, HeapInuse: 2097152, RSSBytes: 8388608, RSSAvailable: true},
			TaskEngineAvailable: true,
			TaskEngine: taskengine.RuntimeStats{
				RetainedBytes: 4096, TerminalCount: 7,
				DeliveryLane:   taskengine.LaneRuntimeStats{WorkerLimit: 4, Workers: 2, Pending: 1, Active: 1},
				DurabilityLane: taskengine.LaneRuntimeStats{WorkerLimit: 3, Workers: 1, Pending: 0, Active: 1},
				Pools:          map[tasks.PoolID]taskengine.PoolRuntimeStats{"interactive": {Workers: 2, MaxWorkers: 32, Running: 1, Idle: 1}},
				Resources:      map[string]taskengine.ResourceRuntimeStats{"download": {Used: 1, Capacity: 3}},
			},
			EventBus:       core.EventBusStats{ActiveWorkers: 3, OrderedWorkers: 2, QueueDepth: 4, QueueCapacity: 100},
			Persistence:    jobs.PersistencePumpStats{WorkerLimit: 2, Workers: 1, Queued: 2, Active: 1, RetainedBytes: 8192},
			Jobs:           jobs.Diagnostics{RetryWorkerLimit: 4, RetryWorkers: 2, RetryQueued: 1, RetryActive: 1, TrackedOccurrences: 9},
			Interaction:    interaction.Stats{Sessions: 5, Inputs: 2, StateBytes: 1024},
			Inline:         inline.RuntimeStats{CacheEntries: 6, CacheBytes: 2048},
			ResourceActive: 8, ResourceLeaked: 0,
			DBOpen: 7, DBInUse: 2, DBIdle: 5,
			ResolverCacheCount: 11, PeerCacheEntries: 12, PeerCacheBytes: 3072,
			RPCTotalRequests: 17, RPCFloodWaits: 1,
		}
	})
	card := p.renderDiagnostics(context.Background())
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	for _, token := range []string{"121 goroutines", "RSS: 8.0 MB", "interactive", "workers 2 (0-32)", "download</b>: 1/3", "Completion", "workers 2/4", "Durability", "workers 1/3", "general 3", "ordered 2", "queued 2", "tracked 9", "sessions 5", "entries 6", "active 8", "open 7", "resolver 11", "requests 17"} {
		if !strings.Contains(card, token) {
			t.Errorf("card missing %q: %s", token, card)
		}
	}
}

func TestDiagnosticsCard_Unavailable(t *testing.T) {
	p := New()
	p.SetDiagnosticsProvider(func() ResourceSnapshot { return ResourceSnapshot{} })
	card := p.renderDiagnostics(context.Background())
	if !strings.Contains(card, "RSS unavailable") || !strings.Contains(card, "TaskEngine unavailable") {
		t.Fatalf("unavailable status missing: %s", card)
	}
	if card := New().renderDiagnostics(context.Background()); !strings.Contains(card, "Runtime Diagnostics") {
		t.Fatalf("fallback card missing: %s", card)
	}
}

func TestDiagnosticsCard_Bounded(t *testing.T) {
	p := New()
	p.SetDiagnosticsProvider(func() ResourceSnapshot {
		max := int(^uint(0) >> 1)
		pools := make(map[tasks.PoolID]taskengine.PoolRuntimeStats)
		for _, name := range []tasks.PoolID{"interactive", "general", "download", "media-process", "scheduler"} {
			pools[name] = taskengine.PoolRuntimeStats{Workers: max, Running: max, Waiting: max, WaitingBytes: int64(max), MaxWorkers: max}
		}
		return ResourceSnapshot{
			ProcessMemory:       ProcessMemorySnapshot{NumGoroutine: max, RSSBytes: ^uint64(0), RSSAvailable: true},
			TaskEngineAvailable: true,
			TaskEngine:          taskengine.RuntimeStats{Pools: pools},
		}
	})
	if got := len(p.renderDiagnostics(context.Background())); got >= 3500 {
		t.Fatalf("diagnostics card length = %d, want <3500", got)
	}
}

func TestDiagnosticsCard_BoundedLargeMaps(t *testing.T) {
	p := New()
	p.SetDiagnosticsProvider(func() ResourceSnapshot {
		max := int(^uint(0) >> 1)
		max64 := int64(max)
		pools := make(map[tasks.PoolID]taskengine.PoolRuntimeStats)
		resources := make(map[string]taskengine.ResourceRuntimeStats)
		for i := 0; i < 100; i++ {
			name := fmt.Sprintf("pool-%03d-%s", i, strings.Repeat("&", 100))
			pools[tasks.PoolID(name)] = taskengine.PoolRuntimeStats{Workers: max, Running: max, Dispatching: max, Idle: max, Waiting: max, MaxWorkers: max, WaitingBytes: max64}
			resources[name] = taskengine.ResourceRuntimeStats{Used: max64, Capacity: max64}
		}
		return ResourceSnapshot{
			ProcessMemory:       ProcessMemorySnapshot{NumGoroutine: max, HeapAlloc: ^uint64(0), HeapInuse: ^uint64(0), HeapIdle: ^uint64(0), HeapReleased: ^uint64(0), HeapObjects: ^uint64(0), StackInuse: ^uint64(0), StackSys: ^uint64(0), Sys: ^uint64(0), NextGC: ^uint64(0), NumGC: ^uint32(0), RSSBytes: ^uint64(0), RSSAvailable: true},
			TaskEngineAvailable: true,
			TaskEngine:          taskengine.RuntimeStats{Pools: pools, Resources: resources, RetainedBytes: max64, RetainedCap: max64, TerminalCount: max, ResultSlotsHeld: max, ResultCapacity: max, DeliveryLane: taskengine.LaneRuntimeStats{WorkerLimit: max, Workers: max, Pending: max, Active: max}, DurabilityLane: taskengine.LaneRuntimeStats{WorkerLimit: max, Workers: max, Pending: max, Active: max}},
			EventBus:            core.EventBusStats{Published: max64, Delivered: max64, Dropped: max64, ActiveWorkers: max, OrderedWorkers: max, QueueDepth: max, QueueCapacity: max},
			Persistence:         jobs.PersistencePumpStats{WorkerLimit: max, Workers: max, Queued: max, Active: max, RetainedBytes: max64},
			Jobs:                jobs.Diagnostics{RetryWorkerLimit: max, RetryWorkers: max, RetryQueued: max, RetryActive: max, TrackedOccurrences: max},
			Interaction:         interaction.Stats{Sessions: max, Inputs: max, StateBytes: max},
			Inline:              inline.RuntimeStats{CacheEntries: max, CacheBytes: max64},
			ResourceActive:      max, ResourceLeaked: max,
			DBOpen: max, DBInUse: max, DBIdle: max,
			ResolverCacheCount: max, PeerCacheEntries: max, PeerCacheBytes: max64, RPCTotalRequests: max64, RPCFloodWaits: max64,
		}
	})
	card := p.renderDiagnostics(context.Background())
	if got := len(card); got >= 3500 {
		t.Fatalf("diagnostics card length = %d, want <3500", got)
	}
	if !strings.Contains(card, "pools omitted") || !strings.Contains(card, "resources omitted") {
		t.Fatalf("omission counts missing: %s", card)
	}
}
