package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestPeriodicCoordinatorIsZeroIdleWithoutRegistrations(t *testing.T) {
	c := newPeriodicCoordinator(nil)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	c.mu.Lock()
	loopRunning := c.loopRunning
	done := c.done
	c.mu.Unlock()
	if loopRunning || done != nil {
		t.Fatalf("empty periodic coordinator retained timing loop: running=%v done=%v", loopRunning, done != nil)
	}

	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPeriodicCoordinatorRetiresAfterLastRegistrationRemoved(t *testing.T) {
	// This test exercises only loop lifecycle. Register normally persists a Job
	// definition, so seed one internal registration directly under the lock.
	c := newPeriodicCoordinator(nil)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	now := time.Now()
	entry := &periodicRegistration{
		Owner: "runtime", Name: "idle-retire", Interval: time.Minute,
		ScheduledFor: now.Add(time.Minute), NextRun: now.Add(time.Minute), Generation: 1,
	}
	entry.heapEntry = &TimerEntry{
		Kind: TimerScheduleOccurrence, Owner: entry.Owner, ID: periodicKey(entry.Owner, entry.Name),
		Generation: entry.Generation, Deadline: entry.NextRun, Data: entry,
	}
	c.entries[periodicKey(entry.Owner, entry.Name)] = entry
	c.heap.Push(entry.heapEntry)
	c.startLoopLocked()
	done := c.done
	c.mu.Unlock()
	c.notify()

	c.mu.Lock()
	delete(c.entries, periodicKey(entry.Owner, entry.Name))
	c.heap = NewIndexedHeap()
	c.mu.Unlock()
	c.notify()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic timing loop did not retire after registry became empty")
	}
	c.mu.Lock()
	running := c.loopRunning
	c.mu.Unlock()
	if running {
		t.Fatal("periodic timing loop remained active after retirement")
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
