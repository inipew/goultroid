package broadcast

import "time"

const (
	defaultProgressMinInterval = 2 * time.Second
	maxProgressSnapshots       = 20
)

type progressCoalescer struct {
	callback      ProgressCallback
	startedAt     time.Time
	lastEmitAt    time.Time
	lastCompleted int
	minStep       int
	last          BroadcastReport
	emitted       bool
}

func newProgressCoalescer(callback ProgressCallback, total int, startedAt time.Time) *progressCoalescer {
	minStep := 1
	if total > maxProgressSnapshots {
		minStep = (total + maxProgressSnapshots - 1) / maxProgressSnapshots
	}
	return &progressCoalescer{
		callback:   callback,
		startedAt:  startedAt,
		lastEmitAt: startedAt,
		minStep:    minStep,
	}
}

func (p *progressCoalescer) Emit(report BroadcastReport, now time.Time, force bool) {
	if p == nil || p.callback == nil {
		return
	}
	completed := report.Sent + report.Failed
	if completed < p.lastCompleted {
		return
	}

	if force {
		if p.emitted && sameProgressState(p.last, report) {
			return
		}
	} else {
		if completed-p.lastCompleted < p.minStep {
			return
		}
		if now.Sub(p.lastEmitAt) < defaultProgressMinInterval {
			return
		}
	}

	if now.Before(p.startedAt) {
		now = p.startedAt
	}
	report.Duration = now.Sub(p.startedAt)
	p.callback(report)
	p.last = report
	p.lastCompleted = completed
	p.lastEmitAt = now
	p.emitted = true
}

func sameProgressState(a, b BroadcastReport) bool {
	return a.Total == b.Total &&
		a.Sent == b.Sent &&
		a.Failed == b.Failed &&
		a.RateLimited == b.RateLimited &&
		a.Canceled == b.Canceled
}
