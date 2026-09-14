package taskengine

import "time"

// Clock is the TaskEngine time source. It is intentionally narrow so queue
// deadlines and retention can be model-tested without wall-clock sleeps.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

// Timer is the resettable single deadline timer used by the coordinator.
type Timer interface {
	C() <-chan time.Time
	Reset(time.Duration) bool
	Stop() bool
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }
func (systemClock) NewTimer(delay time.Duration) Timer {
	return &systemTimer{timer: time.NewTimer(delay)}
}

type systemTimer struct{ timer *time.Timer }

func (t *systemTimer) C() <-chan time.Time { return t.timer.C }
func (t *systemTimer) Reset(delay time.Duration) bool {
	return t.timer.Reset(delay)
}
func (t *systemTimer) Stop() bool { return t.timer.Stop() }
