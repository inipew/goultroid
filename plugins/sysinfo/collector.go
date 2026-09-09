package sysinfo

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// HostInfo holds host system identification and uptime.
type HostInfo struct {
	Hostname      string
	OSName        string
	KernelRelease string
	Arch          string
	Uptime        time.Duration
	BootTime      time.Time
}

// CPUInfo holds processor details, core counts, and load averages.
type CPUInfo struct {
	ModelName string
	Vendor    string
	Cores     int
	Threads   int
	MHz       float64
	CacheSize string
	Load1     float64
	Load5     float64
	Load15    float64
	UsagePct  float64
}

// MemInfo holds RAM and Swap statistics.
type MemInfo struct {
	TotalRAM     int64
	FreeRAM      int64
	AvailRAM     int64
	UsedRAM      int64
	Buffers      int64
	Cached       int64
	TotalSwap    int64
	FreeSwap     int64
	UsedSwap     int64
	RAMUsagePct  float64
	SwapUsagePct float64
}

// DiskMount holds storage stats for a specific mount point.
type DiskMount struct {
	Path        string
	Total       int64
	Free        int64
	Used        int64
	UsagePct    float64
	InodesTotal uint64
	InodesFree  uint64
}

// NetInterface holds details and traffic counters for a network interface.
type NetInterface struct {
	Name      string
	IPs       []string
	MAC       string
	Flags     string
	MTU       int
	RxBytes   int64
	TxBytes   int64
	RxPackets int64
	TxPackets int64
}

// BotInfo holds runtime process statistics for GoUltroid.
type BotInfo struct {
	PID          int
	PPID         int
	Uptime       time.Duration
	GoVersion    string
	Compiler     string
	NumGoroutine int
	NumCPU       int
	HeapAlloc    int64
	HeapSys      int64
	HeapInuse    int64
	StackInuse   int64
	TotalAlloc   int64
	SysMem       int64
	NumGC        uint32
	LastGCTime   time.Time
	RSS          int64
	VMS          int64
	Threads      int
	DataDirSize  int64
}

// Collector provides system telemetry gathering routines.
type Collector struct {
	startTime time.Time
}

// NewCollector initializes a new Collector.
func NewCollector(startTime time.Time) *Collector {
	if startTime.IsZero() {
		startTime = time.Now()
	}
	return &Collector{startTime: startTime}
}

// CollectHostInfo gathers operating system, kernel, and hostname data.
func (c *Collector) CollectHostInfo() HostInfo {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}

	osName := runtime.GOOS
	if runtime.GOOS == "linux" {
		if pretty := readOSReleasePretty(); pretty != "" {
			osName = pretty
		}
	}

	kernel := runtime.GOOS
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		kernel = strings.TrimSpace(string(data))
	}

	uptimeDuration, bootTime := c.getSystemUptime()

	return HostInfo{
		Hostname:      hostname,
		OSName:        osName,
		KernelRelease: kernel,
		Arch:          runtime.GOARCH,
		Uptime:        uptimeDuration,
		BootTime:      bootTime,
	}
}

func readOSReleasePretty() string {
	files := []string{"/etc/os-release", "/usr/lib/os-release"}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				val := strings.TrimPrefix(line, "PRETTY_NAME=")
				return strings.Trim(val, `"`)
			}
		}
	}
	return ""
}

func (c *Collector) getSystemUptime() (time.Duration, time.Time) {
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if sec, err := strconv.ParseFloat(fields[0], 64); err == nil {
				d := time.Duration(sec * float64(time.Second))
				boot := time.Now().Add(-d)
				return d, boot
			}
		}
	}

	var si syscall.Sysinfo_t
	if err := syscall.Sysinfo(&si); err == nil {
		d := time.Duration(si.Uptime) * time.Second
		return d, time.Now().Add(-d)
	}

	return 0, time.Time{}
}

// CollectCPUInfo gathers processor hardware details, core counts, and load.
func (c *Collector) CollectCPUInfo() CPUInfo {
	info := CPUInfo{
		ModelName: runtime.GOARCH,
		Cores:     runtime.NumCPU(),
		Threads:   runtime.NumCPU(),
	}

	if runtime.GOOS == "linux" {
		c.parseProcCPUInfo(&info)
		c.parseProcLoadAvg(&info)
		info.UsagePct = c.calculateCPUUsage()
	}

	return info
}

func (c *Collector) parseProcCPUInfo(info *CPUInfo) {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	threadsCount := 0
	coreIDs := make(map[string]bool)

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "model name", "Hardware", "Processor":
			if info.ModelName == runtime.GOARCH || info.ModelName == "" {
				info.ModelName = val
			}
		case "vendor_id":
			if info.Vendor == "" {
				info.Vendor = val
			}
		case "cpu MHz":
			if info.MHz == 0 {
				info.MHz, _ = strconv.ParseFloat(val, 64)
			}
		case "cache size":
			if info.CacheSize == "" {
				info.CacheSize = val
			}
		case "processor":
			threadsCount++
		case "core id":
			coreIDs[val] = true
		}
	}

	if threadsCount > 0 {
		info.Threads = threadsCount
	}
	if len(coreIDs) > 0 {
		info.Cores = len(coreIDs)
	}
}

func (c *Collector) parseProcLoadAvg(info *CPUInfo) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		var si syscall.Sysinfo_t
		if syscall.Sysinfo(&si) == nil {
			info.Load1 = float64(si.Loads[0]) / 65536.0
			info.Load5 = float64(si.Loads[1]) / 65536.0
			info.Load15 = float64(si.Loads[2]) / 65536.0
		}
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		info.Load1, _ = strconv.ParseFloat(fields[0], 64)
		info.Load5, _ = strconv.ParseFloat(fields[1], 64)
		info.Load15, _ = strconv.ParseFloat(fields[2], 64)
	}
}

func (c *Collector) calculateCPUUsage() float64 {
	readStat := func() (idle, total uint64, ok bool) {
		data, err := os.ReadFile("/proc/stat")
		if err != nil {
			return 0, 0, false
		}
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		if !scanner.Scan() {
			return 0, 0, false
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			return 0, 0, false
		}
		var sum uint64
		for i := 1; i < len(fields); i++ {
			val, _ := strconv.ParseUint(fields[i], 10, 64)
			sum += val
			if i == 4 { // idle
				idle = val
			}
		}
		return idle, sum, true
	}

	idle1, total1, ok1 := readStat()
	if !ok1 {
		return 0.0
	}
	time.Sleep(100 * time.Millisecond)
	idle2, total2, ok2 := readStat()
	if !ok2 || total2 <= total1 {
		return 0.0
	}

	totalDelta := float64(total2 - total1)
	idleDelta := float64(idle2 - idle1)
	if totalDelta <= 0 {
		return 0.0
	}
	usage := (1.0 - (idleDelta / totalDelta)) * 100.0
	if usage < 0 {
		usage = 0
	}
	if usage > 100 {
		usage = 100
	}
	return usage
}

// CollectMemInfo gathers system RAM and Swap usage.
func (c *Collector) CollectMemInfo() MemInfo {
	var m MemInfo

	if runtime.GOOS == "linux" {
		if c.parseProcMemInfo(&m) {
			if m.TotalRAM > 0 {
				m.RAMUsagePct = float64(m.UsedRAM) / float64(m.TotalRAM) * 100.0
			}
			if m.TotalSwap > 0 {
				m.SwapUsagePct = float64(m.UsedSwap) / float64(m.TotalSwap) * 100.0
			}
			return m
		}
	}

	var si syscall.Sysinfo_t
	if err := syscall.Sysinfo(&si); err == nil {
		unit := int64(si.Unit)
		if unit <= 0 {
			unit = 1
		}
		m.TotalRAM = int64(si.Totalram) * unit
		m.FreeRAM = int64(si.Freeram) * unit
		m.Buffers = int64(si.Bufferram) * unit
		m.AvailRAM = m.FreeRAM + m.Buffers
		m.UsedRAM = m.TotalRAM - m.AvailRAM
		m.TotalSwap = int64(si.Totalswap) * unit
		m.FreeSwap = int64(si.Freeswap) * unit
		m.UsedSwap = m.TotalSwap - m.FreeSwap

		if m.TotalRAM > 0 {
			m.RAMUsagePct = float64(m.UsedRAM) / float64(m.TotalRAM) * 100.0
		}
		if m.TotalSwap > 0 {
			m.SwapUsagePct = float64(m.UsedSwap) / float64(m.TotalSwap) * 100.0
		}
		return m
	}

	var rStats runtime.MemStats
	runtime.ReadMemStats(&rStats)
	m.TotalRAM = int64(rStats.Sys)
	m.UsedRAM = int64(rStats.Alloc)
	m.FreeRAM = m.TotalRAM - m.UsedRAM
	m.AvailRAM = m.FreeRAM
	if m.TotalRAM > 0 {
		m.RAMUsagePct = float64(m.UsedRAM) / float64(m.TotalRAM) * 100.0
	}

	return m
}

func (c *Collector) parseProcMemInfo(m *MemInfo) bool {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return false
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valFields := strings.Fields(parts[1])
		if len(valFields) == 0 {
			continue
		}
		kb, err := strconv.ParseInt(valFields[0], 10, 64)
		if err != nil {
			continue
		}
		bytes := kb * 1024

		switch key {
		case "MemTotal":
			m.TotalRAM = bytes
		case "MemFree":
			m.FreeRAM = bytes
		case "MemAvailable":
			m.AvailRAM = bytes
		case "Buffers":
			m.Buffers = bytes
		case "Cached":
			m.Cached = bytes
		case "SwapTotal":
			m.TotalSwap = bytes
		case "SwapFree":
			m.FreeSwap = bytes
		}
	}

	if m.AvailRAM == 0 {
		m.AvailRAM = m.FreeRAM + m.Buffers + m.Cached
	}
	m.UsedRAM = m.TotalRAM - m.AvailRAM
	if m.UsedRAM < 0 {
		m.UsedRAM = 0
	}
	m.UsedSwap = m.TotalSwap - m.FreeSwap
	if m.UsedSwap < 0 {
		m.UsedSwap = 0
	}
	return m.TotalRAM > 0
}

// CollectDiskMounts gathers disk space and inode statistics for specified mount paths.
func (c *Collector) CollectDiskMounts(paths ...string) []DiskMount {
	if len(paths) == 0 {
		paths = []string{"/", "."}
	}

	seen := make(map[string]bool)
	var mounts []DiskMount

	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true

		var stat syscall.Statfs_t
		if err := syscall.Statfs(abs, &stat); err != nil {
			continue
		}

		bsize := uint64(stat.Bsize)
		if bsize == 0 {
			bsize = 4096
		}

		total := int64(stat.Blocks * bsize)
		free := int64(stat.Bavail * bsize)
		used := total - free
		if used < 0 {
			used = 0
		}

		var pct float64
		if total > 0 {
			pct = float64(used) / float64(total) * 100.0
		}

		mounts = append(mounts, DiskMount{
			Path:        p,
			Total:       total,
			Free:        free,
			Used:        used,
			UsagePct:    pct,
			InodesTotal: stat.Files,
			InodesFree:  stat.Ffree,
		})
	}

	return mounts
}

// CollectNetInfo gathers network interfaces and traffic counters.
func (c *Collector) CollectNetInfo() []NetInterface {
	trafficMap := c.parseProcNetDev()

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var results []NetInterface
	for _, ifi := range ifaces {
		var ips []string
		if addrs, err := ifi.Addrs(); err == nil {
			for _, a := range addrs {
				ips = append(ips, a.String())
			}
		}

		item := NetInterface{
			Name:  ifi.Name,
			IPs:   ips,
			MAC:   ifi.HardwareAddr.String(),
			Flags: ifi.Flags.String(),
			MTU:   ifi.MTU,
		}

		if stats, ok := trafficMap[ifi.Name]; ok {
			item.RxBytes = stats.RxBytes
			item.RxPackets = stats.RxPackets
			item.TxBytes = stats.TxBytes
			item.TxPackets = stats.TxPackets
		}

		results = append(results, item)
	}

	return results
}

type netStats struct {
	RxBytes   int64
	RxPackets int64
	TxBytes   int64
	TxPackets int64
}

func (c *Collector) parseProcNetDev() map[string]netStats {
	result := make(map[string]netStats)
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return result
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		ifaceName := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}

		rxBytes, _ := strconv.ParseInt(fields[0], 10, 64)
		rxPackets, _ := strconv.ParseInt(fields[1], 10, 64)
		txBytes, _ := strconv.ParseInt(fields[8], 10, 64)
		txPackets, _ := strconv.ParseInt(fields[9], 10, 64)

		result[ifaceName] = netStats{
			RxBytes:   rxBytes,
			RxPackets: rxPackets,
			TxBytes:   txBytes,
			TxPackets: txPackets,
		}
	}

	return result
}

// CollectBotInfo gathers internal Go runtime, memory, and process statistics.
func (c *Collector) CollectBotInfo() BotInfo {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	pid := os.Getpid()
	ppid := os.Getppid()
	uptime := time.Since(c.startTime)

	var lastGC time.Time
	if m.LastGC > 0 {
		lastGC = time.Unix(0, int64(m.LastGC))
	}

	info := BotInfo{
		PID:          pid,
		PPID:         ppid,
		Uptime:       uptime,
		GoVersion:    runtime.Version(),
		Compiler:     runtime.Compiler,
		NumGoroutine: runtime.NumGoroutine(),
		NumCPU:       runtime.NumCPU(),
		HeapAlloc:    int64(m.HeapAlloc),
		HeapSys:      int64(m.HeapSys),
		HeapInuse:    int64(m.HeapInuse),
		StackInuse:   int64(m.StackInuse),
		TotalAlloc:   int64(m.TotalAlloc),
		SysMem:       int64(m.Sys),
		NumGC:        m.NumGC,
		LastGCTime:   lastGC,
		DataDirSize:  calculateDirSize("data"),
	}

	if runtime.GOOS == "linux" {
		c.parseProcSelfStat(&info)
	}

	return info
}

func (c *Collector) parseProcSelfStat(info *BotInfo) {
	// Parse /proc/self/statm for resident memory
	if data, err := os.ReadFile("/proc/self/statm"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 2 {
			pageSize := int64(os.Getpagesize())
			vmsPages, _ := strconv.ParseInt(fields[0], 10, 64)
			rssPages, _ := strconv.ParseInt(fields[1], 10, 64)
			info.VMS = vmsPages * pageSize
			info.RSS = rssPages * pageSize
		}
	}

	// Parse /proc/self/status for thread count if available
	if data, err := os.ReadFile("/proc/self/status"); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "Threads:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					info.Threads, _ = strconv.Atoi(fields[1])
				}
				break
			}
		}
	}
}

func calculateDirSize(dir string) int64 {
	var total int64
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil {
			return nil
		}
		if !fi.IsDir() {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// FormatDuration formats a duration into days, hours, minutes, seconds.
func FormatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60

	parts := []string{}
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 || days > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 || hours > 0 || days > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	parts = append(parts, fmt.Sprintf("%ds", secs))

	return strings.Join(parts, " ")
}
