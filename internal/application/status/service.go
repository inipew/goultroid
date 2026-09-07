package status

import (
	"fmt"
	"runtime"
	"time"

	"github.com/inipew/goultroid/internal/ui"
)

// Snapshot captures operational runtime metrics at a point in time.
type Snapshot struct {
	Uptime      time.Duration
	AllocMB     float64
	SysMB       float64
	Goroutines  int
	GoVersion   string
	OwnerID     int64
	ProgressBar string
}

// CollectSnapshot samples system resources and calculates uptime and memory usage.
func CollectSnapshot(startTime time.Time, ownerID int64) Snapshot {
	uptime := time.Since(startTime)

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	allocMB := float64(mem.Alloc) / 1024 / 1024
	sysMB := float64(mem.Sys) / 1024 / 1024

	return Snapshot{
		Uptime:      uptime,
		AllocMB:     allocMB,
		SysMB:       sysMB,
		Goroutines:  runtime.NumGoroutine(),
		GoVersion:   runtime.Version(),
		OwnerID:     ownerID,
		ProgressBar: ui.ProgressBar(int64(mem.Alloc), int64(mem.Sys), 8),
	}
}

// RenderAliveCard formats the status snapshot into a standard UI card.
func RenderAliveCard(s Snapshot, botUsername string) string {
	var ownerStr string
	if s.OwnerID != 0 {
		ownerStr = ui.Code(fmt.Sprintf("%d", s.OwnerID))
	} else {
		ownerStr = "<i>Not configured</i>"
	}

	card := ui.NewCard("GoUltroid is Alive & Running!").
		WithIcon("✨")

	if botUsername != "" {
		card.AddField("Bot", "@"+botUsername)
	}
	card.AddField("Uptime", FormatDuration(s.Uptime)).
		AddField("Go Version", ui.Code(s.GoVersion)).
		AddField("RAM Usage", fmt.Sprintf("%s (%.1f / %.1f MB)", s.ProgressBar, s.AllocMB, s.SysMB)).
		AddField("Goroutines", ui.Code(fmt.Sprintf("%d", s.Goroutines))).
		AddField("Owner", ownerStr).
		AddField("Status", "🟢 Active & Running").
		WithFooter("<i>Powered by Go & gotd</i>")

	return card.Render()
}

// FormatDuration pretty-prints a time.Duration into days, hours, minutes, and seconds.
func FormatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	mins := d / time.Minute
	d -= mins * time.Minute
	secs := d / time.Second

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm %ds", days, hours, mins, secs)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, mins, secs)
	}
	if mins > 0 {
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	return fmt.Sprintf("%ds", secs)
}
