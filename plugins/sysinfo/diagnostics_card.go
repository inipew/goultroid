package sysinfo

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/ui"
)

const diagnosticsListLimit = 5
const diagnosticsCardMaxBytes = 3500

func (p *Plugin) renderResourceSnapshot(s ResourceSnapshot) string {
	dlqCount := 0
	if p.eventBus != nil {
		dlqCount = len(p.eventBus.DLQ())
	}
	poolLimit, resourceLimit := diagnosticsListLimit, diagnosticsListLimit
	for {
		card := p.renderResourceSnapshotWithLimits(s, dlqCount, poolLimit, resourceLimit)
		if len(card) < diagnosticsCardMaxBytes || (poolLimit == 0 && resourceLimit == 0) {
			return card
		}
		if resourceLimit > 0 {
			resourceLimit--
		} else {
			poolLimit--
		}
	}
}

func (p *Plugin) renderResourceSnapshotWithLimits(s ResourceSnapshot, dlqCount, poolLimit, resourceLimit int) string {
	card := ui.NewCard("GoUltroid Runtime Diagnostics & Quotas").WithIcon("🔬")
	leaks := "🟢 None"
	if s.ResourceLeaked > 0 {
		leaks = fmt.Sprintf("🔴 <b>%d leaked!</b>", s.ResourceLeaked)
	}
	card.AddField("🛡️ Resource Leaks", leaks)
	card.AddField("📦 Tracked Resources", fmt.Sprintf("active %d | leaked %d", s.ResourceActive, s.ResourceLeaked))

	memory := s.ProcessMemory
	rss := "RSS unavailable"
	if memory.RSSAvailable {
		rss = "RSS: " + formatDiagnosticBytes(memory.RSSBytes)
	}
	card.AddField("📈 Process", fmt.Sprintf("%d goroutines | %s", memory.NumGoroutine, rss))
	card.AddField("🧠 Go Heap", fmt.Sprintf("alloc %s | inuse %s | idle %s | released %s | objects %d",
		formatDiagnosticBytes(memory.HeapAlloc), formatDiagnosticBytes(memory.HeapInuse),
		formatDiagnosticBytes(memory.HeapIdle), formatDiagnosticBytes(memory.HeapReleased), memory.HeapObjects))
	card.AddField("🧵 Go Stack/GC", fmt.Sprintf("stack %s/%s | sys %s | next GC %s | GC %d",
		formatDiagnosticBytes(memory.StackInuse), formatDiagnosticBytes(memory.StackSys),
		formatDiagnosticBytes(memory.Sys), formatDiagnosticBytes(memory.NextGC), memory.NumGC))

	if s.TaskEngineAvailable {
		stats := s.TaskEngine
		poolNames := make([]string, 0, len(stats.Pools))
		for name := range stats.Pools {
			poolNames = append(poolNames, string(name))
		}
		sort.Strings(poolNames)
		var pools strings.Builder
		for _, name := range poolNames[:min(len(poolNames), poolLimit)] {
			pools.WriteString(formatPoolRuntimeStats(boundedDiagnosticLabel(name), stats.Pools[tasks.PoolID(name)]))
			pools.WriteByte('\n')
		}
		if len(poolNames) > poolLimit {
			fmt.Fprintf(&pools, "• additional %d pools omitted\n", len(poolNames)-poolLimit)
		}
		card.AddField("⚙️ Task Pools", strings.TrimSpace(pools.String()))
		resourceNames := make([]string, 0, len(stats.Resources))
		for name := range stats.Resources {
			resourceNames = append(resourceNames, name)
		}
		sort.Strings(resourceNames)
		var resources strings.Builder
		for _, name := range resourceNames[:min(len(resourceNames), resourceLimit)] {
			value := stats.Resources[name]
			fmt.Fprintf(&resources, "• <b>%s</b>: %d/%d\n", ui.EscapeHTML(boundedDiagnosticLabel(name)), value.Used, value.Capacity)
		}
		if len(resourceNames) > resourceLimit {
			fmt.Fprintf(&resources, "• additional %d resources omitted\n", len(resourceNames)-resourceLimit)
		}
		card.AddField("🧮 Execution Resources", strings.TrimSpace(resources.String()))
		card.AddField("🧠 Task Memory", fmt.Sprintf("retained %s/%s | terminal %d | results %d/%d",
			ui.FormatBytes(stats.RetainedBytes), ui.FormatBytes(stats.RetainedCap), stats.TerminalCount,
			stats.ResultSlotsHeld, stats.ResultCapacity))
		card.AddField("📬 Completion", fmt.Sprintf("workers %d/%d | pending %d | active %d",
			stats.DeliveryLane.Workers, stats.DeliveryLane.WorkerLimit, stats.DeliveryLane.Pending, stats.DeliveryLane.Active))
		card.AddField("💾 Durability", fmt.Sprintf("workers %d/%d | pending %d | active %d",
			stats.DurabilityLane.Workers, stats.DurabilityLane.WorkerLimit, stats.DurabilityLane.Pending, stats.DurabilityLane.Active))
	} else {
		card.AddField("⚙️ Task Pools", "TaskEngine unavailable")
	}

	event := s.EventBus
	card.AddField("🛰️ EventBus Telemetry", fmt.Sprintf("Pub %d | Deliv %d | Drop %d | DLQ %d | general %d | ordered %d | queue %d/%d",
		event.Published, event.Delivered, event.Dropped, dlqCount,
		event.ActiveWorkers, event.OrderedWorkers, event.QueueDepth, event.QueueCapacity))
	card.AddField("💽 Persistence", fmt.Sprintf("workers %d/%d | queued %d | active %d | retained %s",
		s.Persistence.Workers, s.Persistence.WorkerLimit, s.Persistence.Queued, s.Persistence.Active,
		ui.FormatBytes(s.Persistence.RetainedBytes)))
	card.AddField("🔁 Jobs Retry", fmt.Sprintf("workers %d/%d | queued %d | active %d | tracked %d",
		s.Jobs.RetryWorkers, s.Jobs.RetryWorkerLimit, s.Jobs.RetryQueued, s.Jobs.RetryActive, s.Jobs.TrackedOccurrences))
	card.AddField("🧩 Interaction", fmt.Sprintf("sessions %d | inputs %d | state %s",
		s.Interaction.Sessions, s.Interaction.Inputs, ui.FormatBytes(int64(s.Interaction.StateBytes))))
	card.AddField("⚡ Inline Cache", fmt.Sprintf("entries %d | retained %s",
		s.Inline.CacheEntries, ui.FormatBytes(s.Inline.CacheBytes)))
	card.AddField("🗄️ DB", fmt.Sprintf("open %d | in-use %d | idle %d", s.DBOpen, s.DBInUse, s.DBIdle))
	card.AddField("📡 Telegram", fmt.Sprintf("resolver %d | peer entries %d | peer bytes %s | requests %d | flood waits %d",
		s.ResolverCacheCount, s.PeerCacheEntries, ui.FormatBytes(s.PeerCacheBytes), s.RPCTotalRequests, s.RPCFloodWaits))
	card.WithFooter("<i>Point-in-time runtime and resource snapshot.</i>")
	return card.Render()
}

func boundedDiagnosticLabel(name string) string {
	const maxRunes = 24
	if utf8.RuneCountInString(name) <= maxRunes {
		return name
	}
	runes := []rune(name)
	return string(runes[:maxRunes]) + "…"
}

func formatDiagnosticBytes(n uint64) string {
	if n > uint64(^uint64(0)>>1) {
		return fmt.Sprintf("%d B", n)
	}
	return ui.FormatBytes(int64(n))
}
