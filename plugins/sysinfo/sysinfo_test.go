package sysinfo_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
	"github.com/inipew/goultroid/plugins/sysinfo"
)

type mockTelegram struct {
	core.MockTelegramServicer
	mu        sync.Mutex
	sentText  string
	editCount int
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentText = text
	return &tg.Message{ID: 10, Message: text}, nil
}

func (m *mockTelegram) EditMessage(ctx context.Context, peer tg.InputPeerClass, id int, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentText = text
	m.editCount++
	return nil
}

func (m *mockTelegram) getText() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sentText
}

func TestSysinfo_MetadataAndCommands(t *testing.T) {
	p := sysinfo.New()

	if p.Name() != "sysinfo" {
		t.Errorf("expected plugin name sysinfo, got %s", p.Name())
	}

	meta := p.Metadata()
	if meta.Name != "sysinfo" || meta.Version == "" {
		t.Errorf("invalid metadata: %+v", meta)
	}

	if err := p.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if err := p.Shutdown(); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	caps := p.Capabilities()
	if len(caps) == 0 || caps[0].ID != "sysinfo" {
		t.Errorf("expected capability sysinfo, got %+v", caps)
	}

	cmds := p.Commands()
	if len(cmds) != 7 {
		t.Fatalf("expected 7 commands, got %d", len(cmds))
	}

	expectedCmds := map[string]bool{
		"sysinfo":     true,
		"cpuinfo":     true,
		"meminfo":     true,
		"diskinfo":    true,
		"netinfo":     true,
		"botinfo":     true,
		"diagnostics": true,
	}

	for _, c := range cmds {
		if !expectedCmds[c.Name] {
			t.Errorf("unexpected command: %s", c.Name)
		}
		if c.Handler == nil {
			t.Errorf("command %s has nil handler", c.Name)
		}
	}
}

func TestSysinfo_CommandHandlers(t *testing.T) {
	startTime := time.Now().Add(-2 * time.Hour)
	p := sysinfo.New(startTime)
	mockTG := &mockTelegram{}

	cmdMap := make(map[string]core.Command)
	for _, c := range p.Commands() {
		cmdMap[c.Name] = c
	}

	callCmd := func(name string) string {
		cmd, ok := cmdMap[name]
		if !ok {
			t.Fatalf("command %s not registered", name)
		}
		ctx := &core.Context{
			Ctx:     context.Background(),
			Svc:     mockTG,
			PeerID:  &tg.InputPeerChat{ChatID: 100},
			Message: &core.Message{ID: 1, IsOutgoing: true, Text: "." + name},
		}
		err := cmd.Handler(ctx)
		if err != nil {
			t.Fatalf("command %s returned error: %v", name, err)
		}
		return mockTG.getText()
	}

	// 1. .sysinfo
	sysinfoOutput := callCmd("sysinfo")
	if !strings.Contains(sysinfoOutput, "System Specifications") {
		t.Errorf("expected card title in sysinfo output, got:\n%s", sysinfoOutput)
	}
	if !strings.Contains(sysinfoOutput, "Operating System") {
		t.Errorf("expected Operating System in sysinfo output, got:\n%s", sysinfoOutput)
	}
	if !strings.Contains(sysinfoOutput, "Memory (RAM)") {
		t.Errorf("expected Memory (RAM) in sysinfo output, got:\n%s", sysinfoOutput)
	}

	// 2. .cpuinfo
	cpuOutput := callCmd("cpuinfo")
	if !strings.Contains(cpuOutput, "Processor") || !strings.Contains(cpuOutput, "Telemetry") {
		t.Errorf("expected card title in cpuinfo output, got:\n%s", cpuOutput)
	}
	if !strings.Contains(cpuOutput, "Core Topology") {
		t.Errorf("expected Core Topology in cpuinfo output, got:\n%s", cpuOutput)
	}

	// 3. .meminfo
	memOutput := callCmd("meminfo")
	if !strings.Contains(memOutput, "Memory") || !strings.Contains(memOutput, "Telemetry") {
		t.Errorf("expected card title in meminfo output, got:\n%s", memOutput)
	}
	if !strings.Contains(memOutput, "Total RAM") {
		t.Errorf("expected Total RAM in meminfo output, got:\n%s", memOutput)
	}

	// 4. .diskinfo
	diskOutput := callCmd("diskinfo")
	if !strings.Contains(diskOutput, "Storage") || !strings.Contains(diskOutput, "Telemetry") {
		t.Errorf("expected card title in diskinfo output, got:\n%s", diskOutput)
	}
	if !strings.Contains(diskOutput, "Mount:") {
		t.Errorf("expected Mount in diskinfo output, got:\n%s", diskOutput)
	}

	// 5. .netinfo
	netOutput := callCmd("netinfo")
	if !strings.Contains(netOutput, "Network Interfaces") {
		t.Errorf("expected Network Interfaces in netinfo output, got:\n%s", netOutput)
	}

	// 6. .botinfo
	botOutput := callCmd("botinfo")
	if !strings.Contains(botOutput, "GoUltroid Bot Engine Telemetry") {
		t.Errorf("expected card title in botinfo output, got:\n%s", botOutput)
	}
	if !strings.Contains(botOutput, "Process ID (PID)") {
		t.Errorf("expected Process ID in botinfo output, got:\n%s", botOutput)
	}
	if !strings.Contains(botOutput, "goroutines") {
		t.Errorf("expected goroutines in botinfo output, got:\n%s", botOutput)
	}
}

func TestCollector_GranularFunctions(t *testing.T) {
	c := sysinfo.NewCollector(time.Now().Add(-10 * time.Minute))

	// Host
	host := c.CollectHostInfo()
	if host.Hostname == "" {
		t.Errorf("expected non-empty hostname")
	}
	if host.OSName == "" {
		t.Errorf("expected non-empty OS name")
	}

	// CPU
	cpu := c.CollectCPUInfo()
	if cpu.Cores <= 0 {
		t.Errorf("expected positive core count, got %d", cpu.Cores)
	}

	// Memory
	mem := c.CollectMemInfo()
	if mem.TotalRAM <= 0 {
		t.Errorf("expected positive total RAM, got %d", mem.TotalRAM)
	}

	// Disk
	disks := c.CollectDiskMounts("/", ".")
	if len(disks) == 0 {
		t.Errorf("expected at least 1 disk mount")
	}
	if disks[0].Total <= 0 {
		t.Errorf("expected positive disk total, got %d", disks[0].Total)
	}

	// Net
	_ = c.CollectNetInfo()

	// Bot
	bot := c.CollectBotInfo()
	if bot.PID <= 0 {
		t.Errorf("expected positive PID, got %d", bot.PID)
	}
	if bot.NumGoroutine <= 0 {
		t.Errorf("expected positive goroutines count, got %d", bot.NumGoroutine)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		duration time.Duration
		expected string
	}{
		{45 * time.Second, "45s"},
		{125 * time.Second, "2m 5s"},
		{3665 * time.Second, "1h 1m 5s"},
		{90065 * time.Second, "1d 1h 1m 5s"},
	}

	for _, tc := range cases {
		res := sysinfo.FormatDuration(tc.duration)
		if res != tc.expected {
			t.Errorf("for %v expected %s, got %s", tc.duration, tc.expected, res)
		}
	}
}

func TestModule_ManifestAndRegistration(t *testing.T) {
	m := sysinfo.Module
	manifest := m.Manifest()
	if manifest.ID != "sysinfo" {
		t.Errorf("expected module ID sysinfo, got %s", manifest.ID)
	}

	router := core.NewRouter(".")
	mgr := plugin.NewManager(router)

	rt := &module.Runtime{
		CoreRuntime: module.CoreRuntime{
			Plugins: mgr,
			Router:  router,
		},
		StartTime: time.Now(),
	}

	if err := m.Register(context.Background(), rt); err != nil {
		t.Fatalf("module registration failed: %v", err)
	}

	registered := mgr.Plugins()
	if len(registered) != 1 || registered[0].Name() != "sysinfo" {
		t.Errorf("expected sysinfo registered in plugin manager, got %v", registered)
	}
}

func TestSysinfo_DiagnosticsCommand(t *testing.T) {
	p := sysinfo.New()

	workersMgr := workers.NewManager()
	tasksMgr := tasks.NewManager()
	workersMgr.SetTasksManager(tasksMgr)

	resMgr := resource.NewManager()
	_ = resMgr.Register(resource.Resource{
		ID:    "res-1",
		Owner: "plugin:downloader",
		Type:  resource.TypeTempFile,
	})

	eventBus := core.NewEventBus()
	_ = eventBus.Start(context.Background())
	defer eventBus.Close()

	p.SetWorkers(workersMgr)
	p.SetTasks(tasksMgr)
	p.SetResources(resMgr)
	p.SetEventBus(eventBus)

	// Set a custom quota and submit a task
	workersMgr.SetOwnerQuota("plugin:downloader", tasks.Quota{
		MaxConcurrent: 3,
		MaxQueued:     10,
	})

	ctx := context.Background()
	_ = workersMgr.Start(ctx)
	defer workersMgr.Stop(ctx)

	_ = workersMgr.Submit(ctx, workers.PoolDownload, tasks.Task{
		ID:    "down-1",
		Owner: "plugin:downloader",
		Run: func(c context.Context) error {
			return nil
		},
	})
	time.Sleep(30 * time.Millisecond)

	mockTG := &mockTelegram{}
	cmdCtx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerChat{ChatID: 123},
		Message: &core.Message{ID: 1, IsOutgoing: true},
	}

	cmdMap := make(map[string]core.Command)
	for _, c := range p.Commands() {
		cmdMap[c.Name] = c
	}

	diagCmd, ok := cmdMap["diagnostics"]
	if !ok {
		t.Fatalf("diagnostics command not found")
	}

	if err := diagCmd.Handler(cmdCtx); err != nil {
		t.Fatalf("diagnostics handler returned error: %v", err)
	}

	output := mockTG.getText()
	if !strings.Contains(output, "GoUltroid Runtime Diagnostics") {
		t.Errorf("expected header in output, got: %s", output)
	}
	if !strings.Contains(output, "Worker Pools") {
		t.Errorf("expected Worker Pools in output, got: %s", output)
	}
	if !strings.Contains(output, "Task Summary") {
		t.Errorf("expected Task Summary in output, got: %s", output)
	}
	if !strings.Contains(output, "Per-Plugin Task Quotas") {
		t.Errorf("expected Per-Plugin Task Quotas in output, got: %s", output)
	}
	if !strings.Contains(output, "Active Tracked Resources") {
		t.Errorf("expected Active Tracked Resources in output, got: %s", output)
	}
	if !strings.Contains(output, "EventBus Telemetry") {
		t.Errorf("expected EventBus Telemetry in output, got: %s", output)
	}
}
