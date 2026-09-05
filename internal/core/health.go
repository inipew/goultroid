package core

import (
	"runtime"
	"time"
)

// HealthStats collects a snapshot of the current process health metrics.
type HealthStats struct {
	// Uptime is how long the bot has been running since startTime was recorded.
	Uptime time.Duration
	// Goroutines is the current goroutine count.
	Goroutines int
	// HeapAllocMB is the current heap allocation in megabytes.
	HeapAllocMB float64
	// SysMB is the total memory obtained from the OS in megabytes.
	SysMB float64
	// NumGC is the total number of garbage collections completed.
	NumGC uint32
	// PauseTotalMs is the cumulative STW GC pause time in milliseconds.
	PauseTotalMs float64
}

// GatherHealth collects current runtime metrics relative to the provided start time.
func GatherHealth(startTime time.Time) HealthStats {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	return HealthStats{
		Uptime:       time.Since(startTime).Truncate(time.Second),
		Goroutines:   runtime.NumGoroutine(),
		HeapAllocMB:  float64(ms.HeapAlloc) / (1024 * 1024),
		SysMB:        float64(ms.Sys) / (1024 * 1024),
		NumGC:        ms.NumGC,
		PauseTotalMs: float64(ms.PauseTotalNs) / 1e6,
	}
}
