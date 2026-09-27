package app

import "runtime"

// ProcessMemoryDiagnostics is a point-in-time view of Go memory and process RSS.
// RSS and Go heap fields are sampled separately and must not be added together.
type ProcessMemoryDiagnostics struct {
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

func processMemoryDiagnostics() ProcessMemoryDiagnostics {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	rss, available := readRSSBytes()
	return ProcessMemoryDiagnostics{
		NumGoroutine: runtime.NumGoroutine(),
		HeapAlloc:    mem.HeapAlloc,
		HeapInuse:    mem.HeapInuse,
		HeapIdle:     mem.HeapIdle,
		HeapReleased: mem.HeapReleased,
		HeapObjects:  mem.HeapObjects,
		StackInuse:   mem.StackInuse,
		StackSys:     mem.StackSys,
		Sys:          mem.Sys,
		NextGC:       mem.NextGC,
		NumGC:        mem.NumGC,
		RSSBytes:     rss,
		RSSAvailable: available,
	}
}
