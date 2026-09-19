package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

func TestPersistencePump_EnqueueAndProcess(t *testing.T) {
	pump := NewPersistencePump(2, 10)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatalf("start pump failed: %v", err)
	}
	defer pump.Stop(context.Background())

	var executed atomic.Bool
	resCh, err := pump.Enqueue(context.Background(), func(ctx context.Context) error {
		executed.Store(true)
		return nil
	})
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	select {
	case err := <-resCh:
		if err != nil {
			t.Fatalf("op failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for op completion")
	}

	if !executed.Load() {
		t.Errorf("expected op to be executed")
	}
}

func TestPersistencePump_QueueSaturation(t *testing.T) {
	pump := NewPersistencePump(1, 1) // 1 worker, 1 queue buffer
	_ = pump.Start(context.Background())
	defer pump.Stop(context.Background())
	blocker := make(chan struct{})
	defer close(blocker)

	started := make(chan struct{})
	// Enqueue op that occupies the single worker
	_, err := pump.Enqueue(context.Background(), func(ctx context.Context) error {
		close(started)
		<-blocker
		return nil
	})
	if err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}

	// Wait until worker has dequeued and started the task
	<-started

	// Enqueue op that fills the buffer (1 slot)
	_, err = pump.Enqueue(context.Background(), func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("second enqueue failed: %v", err)
	}

	// Third enqueue must be rejected with ErrPumpQueueFull
	_, err = pump.Enqueue(context.Background(), func(ctx context.Context) error {
		return nil
	})
	if !errors.Is(err, ErrPumpQueueFull) {
		t.Errorf("expected ErrPumpQueueFull, got: %v", err)
	}
}

func TestPersistencePumpConcurrentDrainAndCancellation(t *testing.T) {
	p := NewPersistencePump(1, 10)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	result, err := p.Enqueue(context.Background(), func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Drain(ctx); err == nil {
		t.Fatal("drain completed while operation active")
	}
	if err := p.Drain(ctx); err == nil {
		t.Fatal("repeated drain falsely reported completion")
	}
	_ = p.Stop(ctx)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("operation not cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("pump lifetime not propagated")
	}
	if err := p.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPersistencePumpPanicDoesNotKillWorker(t *testing.T) {
	p := NewPersistencePump(1, 10)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	result, err := p.Enqueue(context.Background(), func(context.Context) error { panic("failed operation") })
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("panic not reported")
	}
	next, err := p.Enqueue(context.Background(), func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("missing operation deadline")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-next; err != nil {
		t.Fatal(err)
	}
}

type recordingPumpPanicReporter struct {
	mu      sync.Mutex
	reports []core.PanicReport
}

func (r *recordingPumpPanicReporter) ReportPanic(report core.PanicReport) {
	r.mu.Lock()
	r.reports = append(r.reports, report)
	r.mu.Unlock()
}

func (r *recordingPumpPanicReporter) snapshot() []core.PanicReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.PanicReport(nil), r.reports...)
}

func TestPersistencePumpPanicReportsStackAndKeepsWorkerAlive(t *testing.T) {
	p := NewPersistencePump(1, 4)
	reporter := &recordingPumpPanicReporter{}
	p.SetPanicReporter(reporter)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())

	result, err := p.Enqueue(context.Background(), func(context.Context) error {
		panic("durability exploded")
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("panic was not converted to operation error")
	}
	if p.Panics() != 1 {
		t.Fatalf("panic count=%d, want 1", p.Panics())
	}
	reports := reporter.snapshot()
	if len(reports) != 1 {
		t.Fatalf("panic reports=%d, want 1", len(reports))
	}
	if reports[0].Component != "persistence-pump" || len(reports[0].Stack) == 0 {
		t.Fatalf("incomplete panic report: %+v", reports[0])
	}

	next, err := p.Enqueue(context.Background(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := <-next; err != nil {
		t.Fatalf("worker did not survive recovered panic: %v", err)
	}
}

func TestPersistencePumpRetainedByteBudgetIncludesInFlight(t *testing.T) {
	p := NewPersistencePump(1, 4)
	p.SetMaxRetainedBytes(1024)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())

	started := make(chan struct{})
	release := make(chan struct{})
	first, err := p.EnqueueSized(context.Background(), 800, func(context.Context) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	if _, err := p.EnqueueSized(context.Background(), 300, func(context.Context) error { return nil }); !errors.Is(err, ErrPumpByteBudget) {
		close(release)
		t.Fatalf("expected byte-budget rejection while first callback is in flight, got %v", err)
	}
	stats := p.Stats()
	if stats.RetainedBytes != 800 || stats.ByteRejections != 1 {
		close(release)
		t.Fatalf("unexpected retained stats: %+v", stats)
	}

	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if p.Stats().RetainedBytes == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := p.Stats().RetainedBytes; got != 0 {
		t.Fatalf("retained bytes were not released after callback completion: %d", got)
	}
	second, err := p.EnqueueSized(context.Background(), 300, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("enqueue after release: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func TestPersistencePumpWorkersAreLazyAndRetire(t *testing.T) {
	p := NewPersistencePump(2, 4)
	p.idleTimeout = 10 * time.Millisecond
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.remaining.Load(); got != 0 {
		t.Fatalf("persistence pump started %d idle workers, want 0", got)
	}

	result, err := p.Enqueue(context.Background(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && p.remaining.Load() != 0 {
		time.Sleep(time.Millisecond)
	}
	if got := p.remaining.Load(); got != 0 {
		t.Fatalf("persistence pump retained %d workers after idle timeout", got)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPersistencePumpLazyWorkersScaleToDemand(t *testing.T) {
	p := NewPersistencePump(2, 4)
	p.idleTimeout = time.Second
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make([]<-chan error, 0, 2)
	for i := 0; i < 2; i++ {
		result, err := p.Enqueue(context.Background(), func(context.Context) error {
			started <- struct{}{}
			<-release
			return nil
		})
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		results = append(results, result)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("persistence pump failed to scale to configured concurrency")
		}
	}
	close(release)
	for i, result := range results {
		if err := <-result; err != nil {
			t.Fatalf("result %d: %v", i, err)
		}
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
