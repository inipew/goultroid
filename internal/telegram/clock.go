package telegram

import (
	"context"
	"time"
)

// Clock provides the current time.
type Clock interface {
	Now() time.Time
}

// Sleeper provides cancellable sleep operations.
type Sleeper interface {
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock implements Clock using standard time.Now.
type RealClock struct{}

func (RealClock) Now() time.Time {
	return time.Now()
}

// RealSleeper implements Sleeper using a timer with context cancellation.
type RealSleeper struct{}

func (RealSleeper) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// DefaultClock is the standard system clock.
var DefaultClock Clock = RealClock{}

// DefaultSleeper is the standard system sleeper.
var DefaultSleeper Sleeper = RealSleeper{}
