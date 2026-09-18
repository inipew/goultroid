package sysinfo

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/ui"
)

// Plugin provides rich system, hardware, network, and bot runtime metrics.
type Plugin struct {
	startTime  time.Time
	collector  *Collector
	resources  *resource.Manager
	eventBus   *core.EventBus
	taskEngine *taskengine.Engine
}

// New creates a new Sysinfo plugin instance.
func New(startTime ...time.Time) *Plugin {
	start := time.Now()
	if len(startTime) > 0 && !startTime[0].IsZero() {
		start = startTime[0]
	}
	return &Plugin{
		startTime: start,
		collector: NewCollector(start),
	}
}

// SetResources attaches the resource manager.
func (p *Plugin) SetResources(rm *resource.Manager) { p.resources = rm }

// SetEventBus attaches the event bus.
func (p *Plugin) SetEventBus(eb *core.EventBus)           { p.eventBus = eb }
func (p *Plugin) SetTaskEngine(engine *taskengine.Engine) { p.taskEngine = engine }

// Name returns the unique plugin identifier.
func (p *Plugin) Name() string {
	return "sysinfo"
}

// Metadata returns structured metadata about the plugin.
func (p *Plugin) Metadata() plugin.Metadata {
	return plugin.Metadata{
		Name:        "sysinfo",
		Version:     "1.0.0",
		Author:      "GoUltroid Team",
		Description: "Detailed host system hardware, CPU, memory, disk, network, and bot runtime telemetry",
	}
}

// Description returns a human-readable description of the plugin.
func (p *Plugin) Description() string {
	return "Detailed host system hardware, CPU, memory, disk, network, and bot runtime telemetry"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Shutdown cleans up any plugin resources.
func (p *Plugin) Shutdown() error {
	return nil
}

// Capabilities declares permissions and execution surfaces.
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "sysinfo",
			Name:        "System Telemetry",
			Description: "Inspect host hardware, compute, memory, storage, and network statistics",
			Category:    "Diagnostics",
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		},
	}
}

// Commands returns the list of sysinfo commands.
func (p *Plugin) Commands() []core.Command {
	surfaces := execution.SurfaceUserbot | execution.SurfaceAssistant
	return []core.Command{
		{
			Name:        "sysinfo",
			Aliases:     []string{"specs", "spc", "systeminfo", "neofetch"},
			Description: "Comprehensive overview of host system, CPU, memory, storage, and bot runtime",
			Usage:       ".sysinfo",
			Category:    "System",
			Permission:  core.PermissionOwner,
			Surfaces:    surfaces,
			Handler:     p.handleSysinfo,
		},
		{
			Name:        "cpuinfo",
			Aliases:     []string{"cpu", "processors"},
			Description: "Detailed CPU model, core counts, frequency, and load average telemetry",
			Usage:       ".cpuinfo",
			Category:    "System",
			Permission:  core.PermissionOwner,
			Surfaces:    surfaces,
			Handler:     p.handleCPUInfo,
		},
		{
			Name:        "meminfo",
			Aliases:     []string{"ram", "memory"},
			Description: "Detailed RAM, Swap, and Go process heap memory breakdown",
			Usage:       ".meminfo",
			Category:    "System",
			Permission:  core.PermissionOwner,
			Surfaces:    surfaces,
			Handler:     p.handleMemInfo,
		},
		{
			Name:        "diskinfo",
			Aliases:     []string{"disk", "storage", "df"},
			Description: "Detailed storage partitions, mount points, available space, and data dir usage",
			Usage:       ".diskinfo",
			Category:    "System",
			Permission:  core.PermissionOwner,
			Surfaces:    surfaces,
			Handler:     p.handleDiskInfo,
		},
		{
			Name:        "netinfo",
			Aliases:     []string{"network", "net", "interfaces"},
			Description: "Host network interfaces, IP addresses, MACs, and transmitted/received traffic",
			Usage:       ".netinfo",
			Category:    "System",
			Permission:  core.PermissionOwner,
			Surfaces:    surfaces,
			Handler:     p.handleNetInfo,
		},
		{
			Name:        "botinfo",
			Aliases:     []string{"bot", "procinfo", "goultroid"},
			Description: "Internal GoUltroid process runtime, goroutines, threads, and GC telemetry",
			Usage:       ".botinfo",
			Category:    "System",
			Permission:  core.PermissionOwner,
			Surfaces:    surfaces,
			Handler:     p.handleBotInfo,
		},
		{
			Name:        "diagnostics",
			Aliases:     []string{"diag", "quotas", "quotainfo", "resources"},
			Description: "Runtime worker pools, per-plugin task quotas, resource leaks, and event telemetry",
			Usage:       ".diagnostics",
			Category:    "System",
			Permission:  core.PermissionOwner,
			Surfaces:    surfaces,
			Handler:     p.handleDiagnostics,
		},
	}
}

func (p *Plugin) handleSysinfo(ctx *core.Context) error {
	host := p.collector.CollectHostInfo()
	cpu := p.collector.CollectCPUInfo()
	mem := p.collector.CollectMemInfo()
	disks := p.collector.CollectDiskMounts("/", "data")
	bot := p.collector.CollectBotInfo()

	card := ui.NewCard("GoUltroid System Specifications").
		WithIcon("🖥️").
		AddField("Operating System", fmt.Sprintf("%s (<code>%s</code>)", ui.EscapeHTML(host.OSName), host.Arch)).
		AddField("Kernel Release", ui.Code(host.KernelRelease)).
		AddField("Hostname", ui.Code(host.Hostname)).
		AddField("Host Uptime", FormatDuration(host.Uptime)).
		AddField("Processor", fmt.Sprintf("%s (%d Cores / %d Threads)", ui.EscapeHTML(cpu.ModelName), cpu.Cores, cpu.Threads))

	if cpu.Load1 > 0 || cpu.Load5 > 0 || cpu.Load15 > 0 {
		card.AddField("Load Average", fmt.Sprintf("<code>%.2f, %.2f, %.2f</code> (%.1f%%)", cpu.Load1, cpu.Load5, cpu.Load15, cpu.UsagePct))
	}

	card.AddField("Memory (RAM)", ui.FormatProgress(mem.UsedRAM, mem.TotalRAM, 10))

	if mem.TotalSwap > 0 {
		card.AddField("Swap Space", ui.FormatProgress(mem.UsedSwap, mem.TotalSwap, 10))
	}

	for _, d := range disks {
		label := fmt.Sprintf("Storage (%s)", d.Path)
		card.AddField(label, ui.FormatProgress(d.Used, d.Total, 10))
	}

	card.AddField("Bot Engine", fmt.Sprintf("%s | %d goroutines | Uptime: %s",
		ui.Code(bot.GoVersion),
		bot.NumGoroutine,
		FormatDuration(bot.Uptime),
	))

	if bot.RSS > 0 {
		card.AddField("Bot Memory (RSS)", ui.Code(ui.FormatBytes(bot.RSS)))
	}

	card.WithFooter("<i>Use <code>.cpuinfo</code>, <code>.meminfo</code>, <code>.diskinfo</code>, <code>.netinfo</code>, or <code>.botinfo</code> for deep dive.</i>")

	return ctx.EditOrReply(card.Render())
}

func (p *Plugin) handleCPUInfo(ctx *core.Context) error {
	cpu := p.collector.CollectCPUInfo()

	card := ui.NewCard("Processor & Compute Telemetry").
		WithIcon("⚡").
		AddField("Model Name", ui.Code(cpu.ModelName))

	if cpu.Vendor != "" {
		card.AddField("Vendor", ui.Code(cpu.Vendor))
	}

	card.AddField("Core Topology", fmt.Sprintf("%d physical core(s), %d logical thread(s)", cpu.Cores, cpu.Threads))

	if cpu.MHz > 0 {
		card.AddField("Clock Speed", fmt.Sprintf("%.2f MHz", cpu.MHz))
	}
	if cpu.CacheSize != "" {
		card.AddField("Cache Size", ui.Code(cpu.CacheSize))
	}

	if cpu.Load1 > 0 || cpu.Load5 > 0 || cpu.Load15 > 0 {
		card.AddField("Load Average", fmt.Sprintf("1m: <code>%.2f</code> | 5m: <code>%.2f</code> | 15m: <code>%.2f</code>", cpu.Load1, cpu.Load5, cpu.Load15))
	}

	if cpu.UsagePct > 0 {
		card.AddField("CPU Utilization", ui.ProgressBar(int64(cpu.UsagePct), 100, 12))
	}

	card.WithFooter("<i>Real-time compute statistics sampled from host system.</i>")

	return ctx.EditOrReply(card.Render())
}

func (p *Plugin) handleMemInfo(ctx *core.Context) error {
	mem := p.collector.CollectMemInfo()
	bot := p.collector.CollectBotInfo()

	card := ui.NewCard("Memory & Swap Telemetry").
		WithIcon("🧠").
		AddField("Total RAM", ui.Code(ui.FormatBytes(mem.TotalRAM))).
		AddField("Used RAM", fmt.Sprintf("%s (<code>%.1f%%</code>)", ui.FormatBytes(mem.UsedRAM), mem.RAMUsagePct)).
		AddField("Available RAM", ui.Code(ui.FormatBytes(mem.AvailRAM))).
		AddField("Free RAM", ui.Code(ui.FormatBytes(mem.FreeRAM)))

	if mem.Buffers > 0 || mem.Cached > 0 {
		card.AddField("Buffers / Cached", fmt.Sprintf("%s / %s", ui.FormatBytes(mem.Buffers), ui.FormatBytes(mem.Cached)))
	}

	card.AddField("RAM Allocation", ui.FormatProgress(mem.UsedRAM, mem.TotalRAM, 10))

	if mem.TotalSwap > 0 {
		card.AddField("Total Swap", ui.Code(ui.FormatBytes(mem.TotalSwap))).
			AddField("Used Swap", fmt.Sprintf("%s (<code>%.1f%%</code>)", ui.FormatBytes(mem.UsedSwap), mem.SwapUsagePct)).
			AddField("Free Swap", ui.Code(ui.FormatBytes(mem.FreeSwap))).
			AddField("Swap Allocation", ui.FormatProgress(mem.UsedSwap, mem.TotalSwap, 10))
	}

	card.AddField("Bot Heap Alloc", ui.Code(ui.FormatBytes(bot.HeapAlloc))).
		AddField("Bot Heap Sys", ui.Code(ui.FormatBytes(bot.HeapSys))).
		AddField("Bot Stack Inuse", ui.Code(ui.FormatBytes(bot.StackInuse)))

	card.WithFooter("<i>Memory metrics aggregated from /proc/meminfo and Go runtime.</i>")

	return ctx.EditOrReply(card.Render())
}

func (p *Plugin) handleDiskInfo(ctx *core.Context) error {
	disks := p.collector.CollectDiskMounts("/", ".", "data")
	bot := p.collector.CollectBotInfo()

	card := ui.NewCard("Storage & Filesystem Telemetry").
		WithIcon("💾")

	for i, d := range disks {
		header := fmt.Sprintf("%d. Mount: <code>%s</code>", i+1, ui.EscapeHTML(d.Path))
		card.AddField(header, ui.FormatProgress(d.Used, d.Total, 10))

		details := fmt.Sprintf("Used: %s | Free: %s | Total: %s",
			ui.FormatBytes(d.Used),
			ui.FormatBytes(d.Free),
			ui.FormatBytes(d.Total),
		)
		if d.InodesTotal > 0 {
			details += fmt.Sprintf(" | Inodes: %d free", d.InodesFree)
		}
		card.AddField("  └ Details", details)
	}

	if bot.DataDirSize > 0 {
		card.AddField("Bot Data Folder", fmt.Sprintf("<code>data/</code> consumes %s", ui.FormatBytes(bot.DataDirSize)))
	}

	card.WithFooter("<i>Storage capacity calculated via host filesystem statfs.</i>")

	return ctx.EditOrReply(card.Render())
}

func (p *Plugin) handleNetInfo(ctx *core.Context) error {
	ifaces := p.collector.CollectNetInfo()

	card := ui.NewCard(fmt.Sprintf("Network Interfaces (%d)", len(ifaces))).
		WithIcon("🌐")

	activeCount := 0
	for _, ifi := range ifaces {
		// Filter out purely down or empty interfaces if there are many
		if len(ifi.IPs) == 0 && ifi.RxBytes == 0 && ifi.TxBytes == 0 && !strings.Contains(ifi.Flags, "up") {
			continue
		}
		activeCount++

		ipStr := "None"
		if len(ifi.IPs) > 0 {
			ipStr = strings.Join(ifi.IPs, ", ")
		}

		statusEmoji := "🟢"
		if !strings.Contains(ifi.Flags, "up") {
			statusEmoji = "🔴"
		}

		card.AddField(
			fmt.Sprintf("%s <code>%s</code> (MTU: %d)", statusEmoji, ui.EscapeHTML(ifi.Name), ifi.MTU),
			fmt.Sprintf("IP: <code>%s</code>", ui.EscapeHTML(ipStr)),
		)

		if ifi.MAC != "" {
			card.AddField("  ├ MAC", ui.Code(ifi.MAC))
		}

		if ifi.RxBytes > 0 || ifi.TxBytes > 0 {
			traffic := fmt.Sprintf("⬇️ %s (%d pkts) | ⬆️ %s (%d pkts)",
				ui.FormatBytes(ifi.RxBytes), ifi.RxPackets,
				ui.FormatBytes(ifi.TxBytes), ifi.TxPackets,
			)
			card.AddField("  └ Traffic", traffic)
		}
	}

	if activeCount == 0 {
		card.WithHeader("No active network interfaces detected.")
	}

	card.WithFooter("<i>Network telemetry sampled from kernel net subsystem.</i>")

	return ctx.EditOrReply(card.Render())
}

func (p *Plugin) handleBotInfo(ctx *core.Context) error {
	bot := p.collector.CollectBotInfo()

	card := ui.NewCard("GoUltroid Bot Engine Telemetry").
		WithIcon("🤖").
		AddField("Process ID (PID)", fmt.Sprintf("PID: <code>%d</code> (PPID: <code>%d</code>)", bot.PID, bot.PPID)).
		AddField("Bot Uptime", FormatDuration(bot.Uptime)).
		AddField("Go Version", ui.Code(bot.GoVersion)).
		AddField("Compiler / Arch", fmt.Sprintf("<code>%s</code> on <code>%s/%s</code>", bot.Compiler, runtime.GOOS, runtime.GOARCH)).
		AddField("Concurrency", fmt.Sprintf("<code>%d</code> goroutines | <code>%d</code> logical CPUs", bot.NumGoroutine, bot.NumCPU))

	if bot.Threads > 0 {
		card.AddField("Kernel Threads", ui.Code(fmt.Sprintf("%d threads", bot.Threads)))
	}

	if bot.RSS > 0 {
		card.AddField("Process Resident (RSS)", ui.Code(ui.FormatBytes(bot.RSS)))
	}
	if bot.VMS > 0 {
		card.AddField("Virtual Memory (VMS)", ui.Code(ui.FormatBytes(bot.VMS)))
	}

	card.AddField("Heap Memory Alloc", ui.Code(ui.FormatBytes(bot.HeapAlloc))).
		AddField("Heap Inuse / Sys", fmt.Sprintf("%s / %s", ui.FormatBytes(bot.HeapInuse), ui.FormatBytes(bot.HeapSys))).
		AddField("Stack Memory Inuse", ui.Code(ui.FormatBytes(bot.StackInuse))).
		AddField("Garbage Collector", fmt.Sprintf("%d cycles completed", bot.NumGC))

	if !bot.LastGCTime.IsZero() {
		card.AddField("Last GC Run", bot.LastGCTime.Format(time.RFC822))
	}

	if bot.DataDirSize > 0 {
		card.AddField("Local Data Footprint", ui.FormatBytes(bot.DataDirSize))
	}

	card.WithFooter("<i>Internal runtime profiling from Go runtime and OS process descriptor.</i>")

	return ctx.EditOrReply(card.Render())
}

func (p *Plugin) handleDiagnostics(ctx *core.Context) error {
	card := ui.NewCard("GoUltroid Runtime Diagnostics & Quotas").WithIcon("🔬")

	// 1. Tracked Resources & Leaks
	if p.resources != nil {
		snaps := p.resources.AllSnapshots()
		leakedTotal := 0
		for _, s := range snaps {
			leakedTotal += s.Leaked
		}
		leakStr := "🟢 None"
		if leakedTotal > 0 {
			leakStr = fmt.Sprintf("🔴 <b>%d leaked!</b>", leakedTotal)
		}
		card.AddField("🛡️ Resource Leaks", leakStr)

		if len(snaps) > 0 {
			var sb strings.Builder
			for _, s := range snaps {
				var typeParts []string
				for tName, count := range s.CountsByType {
					typeParts = append(typeParts, fmt.Sprintf("%s: %d", tName, count))
				}
				sb.WriteString(fmt.Sprintf("• <b>%s</b>: %d active (%s)\n",
					ui.EscapeHTML(s.Owner), s.TotalActive, strings.Join(typeParts, ", ")))
			}
			card.AddField("📦 Active Tracked Resources", strings.TrimRight(sb.String(), "\n"))
		}
	}

	// 2. EventBus
	if p.taskEngine != nil {
		statsCtx, cancel := context.WithTimeout(ctx.Ctx, 200*time.Millisecond)
		stats, err := p.taskEngine.Stats(statsCtx)
		cancel()
		if err == nil {
			poolNames := make([]string, 0, len(stats.Pools))
			for name := range stats.Pools {
				poolNames = append(poolNames, string(name))
			}
			sort.Strings(poolNames)
			var lines strings.Builder
			for _, name := range poolNames {
				pool := stats.Pools[tasks.PoolID(name)]
				lines.WriteString(formatPoolRuntimeStats(name, pool))
				lines.WriteByte('\n')
			}
			card.AddField("⚙️ Task Pools", strings.TrimSpace(lines.String()))
			resourceNames := make([]string, 0, len(stats.Resources))
			for name := range stats.Resources {
				resourceNames = append(resourceNames, name)
			}
			sort.Strings(resourceNames)
			var resourceLines strings.Builder
			for _, name := range resourceNames {
				value := stats.Resources[name]
				fmt.Fprintf(&resourceLines, "• <b>%s</b>: %d/%d\n", ui.EscapeHTML(name), value.Used, value.Capacity)
			}
			card.AddField("🧮 Execution Resources", strings.TrimSpace(resourceLines.String()))
			card.AddField("🧠 Task Memory", fmt.Sprintf("Retained: %s / %s | Results: %d/%d", ui.FormatBytes(stats.RetainedBytes), ui.FormatBytes(stats.RetainedCap), stats.ResultSlotsHeld, stats.ResultCapacity))
		}
	}

	// 3. EventBus
	if p.eventBus != nil {
		ebStats := p.eventBus.Stats()
		dlqLen := len(p.eventBus.DLQ())
		card.AddField("🛰️ EventBus Telemetry", fmt.Sprintf("Pub: <code>%d</code> | Deliv: <code>%d</code> | Drop: <code>%d</code> | DLQ: <code>%d</code>",
			ebStats.Published, ebStats.Delivered, ebStats.Dropped, dlqLen))
	}

	card.WithFooter("<i>Telemetry aggregated across task execution, resources, and eventbus.</i>")
	return ctx.EditOrReply(card.Render())
}

func formatPoolRuntimeStats(name string, pool taskengine.PoolRuntimeStats) string {
	return fmt.Sprintf("• <b>%s</b>: running %d, dispatching %d, idle %d, waiting %d / %s | workers %d (%d-%d)",
		ui.EscapeHTML(name), pool.Running, pool.Dispatching, pool.Idle, pool.Waiting, ui.FormatBytes(pool.WaitingBytes),
		pool.Workers, pool.MinWorkers, pool.MaxWorkers)
}
