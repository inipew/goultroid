package telegram

import (
	"context"
	"sync"
	"testing"
	"time"
)

// FakeClock is a controllable Clock for deterministic tests.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func NewFakeClock(initial time.Time) *FakeClock {
	return &FakeClock{now: initial}
}

func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// FakeSleeper records sleep durations and allows immediate non-blocking progress in tests.
type FakeSleeper struct {
	mu     sync.Mutex
	sleeps []time.Duration
	err    error
}

func (f *FakeSleeper) Sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sleeps = append(f.sleeps, d)
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return f.err
}

func (f *FakeSleeper) TotalSleep() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	var total time.Duration
	for _, s := range f.sleeps {
		total += s
	}
	return total
}

func (f *FakeSleeper) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sleeps)
}

func TestRealClock(t *testing.T) {
	c := RealClock{}
	t1 := c.Now()
	time.Sleep(2 * time.Millisecond)
	t2 := c.Now()
	if !t2.After(t1) {
		t.Fatalf("expected t2 (%v) to be after t1 (%v)", t2, t1)
	}
}

func TestRealSleeper_ZeroDuration(t *testing.T) {
	s := RealSleeper{}
	ctx := context.Background()
	if err := s.Sleep(ctx, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Sleep(ctx, -time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRealSleeper_Cancellation(t *testing.T) {
	s := RealSleeper{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Sleep(ctx, 5*time.Second)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestFakeClock_Advance(t *testing.T) {
	initial := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	fc := NewFakeClock(initial)
	if !fc.Now().Equal(initial) {
		t.Fatalf("expected initial time %v, got %v", initial, fc.Now())
	}
	fc.Advance(5 * time.Minute)
	expected := initial.Add(5 * time.Minute)
	if !fc.Now().Equal(expected) {
		t.Fatalf("expected %v, got %v", expected, fc.Now())
	}
}

func TestFakeSleeper_Records(t *testing.T) {
	fs := &FakeSleeper{}
	ctx := context.Background()
	_ = fs.Sleep(ctx, 100*time.Millisecond)
	_ = fs.Sleep(ctx, 200*time.Millisecond)
	if fs.Calls() != 2 {
		t.Fatalf("expected 2 calls, got %d", fs.Calls())
	}
	if fs.TotalSleep() != 300*time.Millisecond {
		t.Fatalf("expected 300ms, got %v", fs.TotalSleep())
	}
}
