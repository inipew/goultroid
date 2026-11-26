package taskengine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type submitReplyC struct {
	ticket tasks.Ticket
	err    error
}

type submitCellB struct {
	state  atomic.Uint32
	done   chan struct{}
	ticket tasks.Ticket
	err    error
}

func (c *submitCellB) decide(next submitDecisionState) bool {
	if c == nil {
		return false
	}
	return c.state.CompareAndSwap(uint32(decisionPending), uint32(next))
}

type c3BarrierResolver struct {
	entered chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (r *c3BarrierResolver) ResolveHandler(ref string, _ any) (tasks.HandlerFunc, error) {
	if ref == "barrier" {
		r.once.Do(func() { close(r.entered) })
		<-r.proceed
	}
	return func(context.Context) error { return nil }, nil
}

func reportC3Percentiles(b *testing.B, samples []time.Duration) {
	b.Helper()
	if len(samples) == 0 {
		return
	}
	slices.Sort(samples)
	pct := func(q float64) float64 {
		idx := int(float64(len(samples)-1) * q)
		return float64(samples[idx].Nanoseconds())
	}
	b.ReportMetric(pct(0.50), "p50_ns/op")
	b.ReportMetric(pct(0.95), "p95_ns/op")
	b.ReportMetric(pct(0.99), "p99_ns/op")
}

// TestC3_ProductionSubmitLinearizationAndLifecycle verifies real Engine.Submit
// (status quo A) using deterministic HandlerResolver barriers for mid-flight
// cancellation, late reply after caller timeout, inbox saturation, and shutdown.
func TestC3_ProductionSubmitLinearizationAndLifecycle(t *testing.T) {
	t.Run("normal_and_precancel", func(t *testing.T) {
		e := NewEngine(Config{
			Pools: map[tasks.PoolID]PoolEngineConfig{
				"general": {Concurrency: 4, BacklogLimit: 64},
			},
			ResultCapacity: 128,
		})
		if err := e.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer e.Stop(context.Background())

		for i := 0; i < 20; i++ {
			id := tasks.TaskID(fmt.Sprintf("c3-prod-ok-%d", i))
			tkt, err := e.Submit(context.Background(), tasks.WorkSpec{
				ID:         id,
				QuotaOwner: "user:c3",
				Pool:       "general",
				Class:      tasks.PriorityNormal,
				Handler:    func(context.Context) error { return nil },
			})
			if err != nil {
				t.Fatalf("Submit(%s) error = %v", id, err)
			}
			if tkt == nil || tkt.TaskID() != id {
				t.Fatalf("ticket mismatch: got %v, want %s", tkt, id)
			}
			if _, werr := tkt.Wait(context.Background()); werr != nil {
				t.Fatalf("Wait(%s) = %v", id, werr)
			}
		}

		cctx, ccancel := context.WithCancel(context.Background())
		ccancel()
		if _, err := e.Submit(cctx, tasks.WorkSpec{
			ID:         "c3-prod-precancel",
			QuotaOwner: "user:c3",
			Pool:       "general",
			Handler:    func(context.Context) error { return nil },
		}); !errors.Is(err, context.Canceled) {
			t.Fatalf("precancel err = %v, want context.Canceled", err)
		}
	})

	t.Run("midflight_cancel_at_admission_barrier", func(t *testing.T) {
		resolver := &c3BarrierResolver{
			entered: make(chan struct{}),
			proceed: make(chan struct{}),
		}
		e := NewEngine(Config{
			Pools: map[tasks.PoolID]PoolEngineConfig{
				"general": {Concurrency: 4, BacklogLimit: 64},
			},
			ResultCapacity: 64,
		})
		e.SetHandlerResolver(resolver)
		if err := e.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer e.Stop(context.Background())

		ctx, cancel := context.WithCancel(context.Background())
		type res struct {
			tkt tasks.Ticket
			err error
		}
		out := make(chan res, 1)
		go func() {
			tkt, err := e.Submit(ctx, tasks.WorkSpec{
				ID:         "c3-barrier-cancel",
				QuotaOwner: "user:c3",
				Pool:       "general",
				HandlerRef: "barrier",
			})
			out <- res{tkt: tkt, err: err}
		}()

		// Deterministically wait until coordinator is inside admitSubmit() BEFORE decisionAccepted CAS.
		<-resolver.entered
		cancel()
		// Caller must observe ErrLinearizationCancel while coordinator is still blocked.
		got := <-out
		if !errors.Is(got.err, tasks.ErrLinearizationCancel) || !errors.Is(got.err, context.Canceled) {
			close(resolver.proceed)
			t.Fatalf("midflight cancel err = %v, want ErrLinearizationCancel+Canceled", got.err)
		}

		// Release coordinator; admitSubmit must see callerCtx.Err() and NOT admit the task.
		close(resolver.proceed)
		if snap, ok := e.Snapshot("c3-barrier-cancel"); ok {
			t.Fatalf("cancelled task was admitted into registry: %+v", snap)
		}
	})

	t.Run("late_reply_after_caller_exit_and_inbox_full", func(t *testing.T) {
		resolver := &c3BarrierResolver{
			entered: make(chan struct{}),
			proceed: make(chan struct{}),
		}
		e := NewEngine(Config{
			InboxCapacity: 1,
			Pools: map[tasks.PoolID]PoolEngineConfig{
				"general": {Concurrency: 2, BacklogLimit: 32},
			},
			ResultCapacity:  32,
			DecisionTimeout: 20 * time.Millisecond,
		})
		e.SetHandlerResolver(resolver)
		if err := e.Start(context.Background()); err != nil {
			t.Fatal(err)
		}

		// 1. Hold coordinator inside admitSubmit() for "c3-blocker".
		blockerDone := make(chan error, 1)
		go func() {
			tkt, err := e.Submit(context.Background(), tasks.WorkSpec{
				ID:         "c3-blocker",
				QuotaOwner: "user:c3",
				Pool:       "general",
				HandlerRef: "barrier",
			})
			if err == nil && tkt != nil {
				_, err = tkt.Wait(context.Background())
			}
			blockerDone <- err
		}()
		<-resolver.entered

		// 2. Enqueue one request to fill InboxCapacity=1, wait until it is observed
		// in e.inbox, then cancel its caller and wait for Submit to return BEFORE
		// releasing the coordinator so coordinator must handle a true late reply.
		lateCtx, lateCancel := context.WithCancel(context.Background())
		lateErr := make(chan error, 1)
		go func() {
			_, err := e.Submit(lateCtx, tasks.WorkSpec{
				ID:         "c3-late-caller",
				QuotaOwner: "user:c3",
				Pool:       "general",
				Handler:    func(context.Context) error { return nil },
			})
			lateErr <- err
		}()

		enqueueDeadline := time.Now().Add(time.Second)
		for len(e.inbox) < 1 {
			if time.Now().After(enqueueDeadline) {
				lateCancel()
				close(resolver.proceed)
				t.Fatal("timed out waiting for late-caller request to enter e.inbox")
			}
			time.Sleep(time.Millisecond)
		}

		lateCancel()
		if err := <-lateErr; !errors.Is(err, tasks.ErrLinearizationCancel) || !errors.Is(err, context.Canceled) {
			close(resolver.proceed)
			t.Fatalf("late caller err = %v, want ErrLinearizationCancel+Canceled", err)
		}

		// 3. Release coordinator; it processes "c3-late-caller" and sends late reply
		// to the buffered channel without blocking runLoop.
		close(resolver.proceed)
		if err := <-blockerDone; err != nil {
			t.Fatalf("blocker task err = %v", err)
		}

		// 4. Verify coordinator remains responsive and Stop() drains engine workers cleanly.
		okTkt, okErr := e.Submit(context.Background(), tasks.WorkSpec{
			ID:         "c3-post-late-ok",
			QuotaOwner: "user:c3",
			Pool:       "general",
			Handler:    func(context.Context) error { return nil },
		})
		if okErr != nil {
			t.Fatalf("coordinator unresponsive after late reply: %v", okErr)
		}
		_, _ = okTkt.Wait(context.Background())
		if err := e.Stop(context.Background()); err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
		select {
		case <-e.runtimeDone:
		case <-time.After(time.Second):
			t.Fatal("engine runtimeDone did not close after Stop()")
		}
	})

	t.Run("shutdown_while_submit_inflight", func(t *testing.T) {
		resolver := &c3BarrierResolver{
			entered: make(chan struct{}),
			proceed: make(chan struct{}),
		}
		e := NewEngine(Config{
			InboxCapacity: 4,
			Pools: map[tasks.PoolID]PoolEngineConfig{
				"general": {Concurrency: 1, BacklogLimit: 16},
			},
			ResultCapacity: 16,
		})
		e.SetHandlerResolver(resolver)
		if err := e.Start(context.Background()); err != nil {
			t.Fatal(err)
		}

		submitDone := make(chan error, 1)
		go func() {
			_, err := e.Submit(context.Background(), tasks.WorkSpec{
				ID:         "c3-inflight-stop",
				QuotaOwner: "user:c3",
				Pool:       "general",
				HandlerRef: "barrier",
			})
			submitDone <- err
		}()

		// Wait until Submit is deterministically inside coordinator admitSubmit().
		<-resolver.entered
		stopDone := make(chan error, 1)
		go func() {
			stopDone <- e.Stop(context.Background())
		}()

		// Wait until Stop() has enqueued its quiesce control request into e.inbox
		// while coordinator is still held at the admission barrier.
		quiesceDeadline := time.Now().Add(time.Second)
		for len(e.inbox) == 0 {
			if time.Now().After(quiesceDeadline) {
				close(resolver.proceed)
				t.Fatal("timed out waiting for Stop() quiesce request in e.inbox")
			}
			time.Sleep(time.Millisecond)
		}
		close(resolver.proceed)

		if err := <-submitDone; err != nil && !errors.Is(err, tasks.ErrEngineQuiescing) {
			t.Fatalf("inflight submit err = %v", err)
		}
		if err := <-stopDone; err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
		select {
		case <-e.runtimeDone:
		case <-time.After(time.Second):
			t.Fatal("engine runtimeDone did not close after Stop()")
		}
	})
}

// BenchmarkC3_EndToEnd_ProductionSubmitAndWait measures the real production
// Engine.Submit + Ticket.Wait cycle (sendSubmitControl -> controlGate ->
// requestPool -> inbox -> runLoop -> admitSubmit -> worker execution -> Wait)
// for normal/contention scenarios, and Submit-only for error scenarios
// (Status Quo A).
func BenchmarkC3_EndToEnd_ProductionSubmitAndWait(b *testing.B) {
	b.Run("normal_no_deadline", func(b *testing.B) {
		e := NewEngine(Config{
			Pools:          map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 8, BacklogLimit: 65536}},
			ResultCapacity: 200000,
		})
		if err := e.Start(context.Background()); err != nil {
			b.Fatal(err)
		}
		defer e.Stop(context.Background())

		ctx := context.Background()
		samples := make([]time.Duration, 0, min(b.N, 50000))
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			start := time.Now()
			tkt, err := e.Submit(ctx, tasks.WorkSpec{
				ID:         tasks.TaskID(fmt.Sprintf("c3-e2e-nd-%d", i)),
				QuotaOwner: "user:c3",
				Pool:       "general",
				Class:      tasks.PriorityNormal,
				Handler:    func(context.Context) error { return nil },
			})
			if err != nil {
				b.Fatal(err)
			}
			if _, err := tkt.Wait(ctx); err != nil {
				b.Fatal(err)
			}
			dur := time.Since(start)
			if len(samples) < cap(samples) {
				samples = append(samples, dur)
			}
		}
		b.StopTimer()
		reportC3Percentiles(b, samples)
	})

	b.Run("normal_with_deadline", func(b *testing.B) {
		e := NewEngine(Config{
			Pools:          map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 8, BacklogLimit: 65536}},
			ResultCapacity: 200000,
		})
		if err := e.Start(context.Background()); err != nil {
			b.Fatal(err)
		}
		defer e.Stop(context.Background())

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		samples := make([]time.Duration, 0, min(b.N, 50000))
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			start := time.Now()
			tkt, err := e.Submit(ctx, tasks.WorkSpec{
				ID:         tasks.TaskID(fmt.Sprintf("c3-e2e-wd-%d", i)),
				QuotaOwner: "user:c3",
				Pool:       "general",
				Class:      tasks.PriorityNormal,
				Handler:    func(context.Context) error { return nil },
			})
			if err != nil {
				b.Fatal(err)
			}
			if _, err := tkt.Wait(ctx); err != nil {
				b.Fatal(err)
			}
			dur := time.Since(start)
			if len(samples) < cap(samples) {
				samples = append(samples, dur)
			}
		}
		b.StopTimer()
		reportC3Percentiles(b, samples)
	})

	b.Run("contention", func(b *testing.B) {
		e := NewEngine(Config{
			Pools:          map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 8, BacklogLimit: 65536}},
			ResultCapacity: 200000,
		})
		if err := e.Start(context.Background()); err != nil {
			b.Fatal(err)
		}
		defer e.Stop(context.Background())

		ctx := context.Background()
		var seq atomic.Uint64
		var mu sync.Mutex
		var workerErr error
		// Bounded indicative sample collection (up to 50,000 samples across workers).
		samples := make([]time.Duration, 0, 50000)
		b.ResetTimer()
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			local := make([]time.Duration, 0, 1024)
			for pb.Next() {
				start := time.Now()
				tkt, err := e.Submit(ctx, tasks.WorkSpec{
					ID:         tasks.TaskID(fmt.Sprintf("c3-e2e-par-%d", seq.Add(1))),
					QuotaOwner: "user:c3",
					Pool:       "general",
					Class:      tasks.PriorityNormal,
					Handler:    func(context.Context) error { return nil },
				})
				if err != nil {
					mu.Lock()
					if workerErr == nil {
						workerErr = err
					}
					mu.Unlock()
					return
				}
				if _, err := tkt.Wait(ctx); err != nil {
					mu.Lock()
					if workerErr == nil {
						workerErr = err
					}
					mu.Unlock()
					return
				}
				dur := time.Since(start)
				if len(local) < cap(local) {
					local = append(local, dur)
				}
			}
			mu.Lock()
			if len(samples) < cap(samples) {
				remain := cap(samples) - len(samples)
				if len(local) > remain {
					local = local[:remain]
				}
				samples = append(samples, local...)
			}
			mu.Unlock()
		})
		b.StopTimer()
		if workerErr != nil {
			b.Fatal(workerErr)
		}
		reportC3Percentiles(b, samples)
	})

	b.Run("cancellation", func(b *testing.B) {
		e := NewEngine(Config{
			Pools:          map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 8, BacklogLimit: 65536}},
			ResultCapacity: 200000,
		})
		if err := e.Start(context.Background()); err != nil {
			b.Fatal(err)
		}
		defer e.Stop(context.Background())

		cctx, ccancel := context.WithCancel(context.Background())
		ccancel()
		samples := make([]time.Duration, 0, min(b.N, 50000))
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			start := time.Now()
			_, err := e.Submit(cctx, tasks.WorkSpec{
				ID:         tasks.TaskID(fmt.Sprintf("c3-e2e-cancel-%d", i)),
				QuotaOwner: "user:c3",
				Pool:       "general",
				Handler:    func(context.Context) error { return nil },
			})
			dur := time.Since(start)
			if !errors.Is(err, tasks.ErrLinearizationCancel) && !errors.Is(err, context.Canceled) {
				b.Fatalf("unexpected err: %v", err)
			}
			if len(samples) < cap(samples) {
				samples = append(samples, dur)
			}
		}
		b.StopTimer()
		reportC3Percentiles(b, samples)
	})

	b.Run("inbox_full_timeout", func(b *testing.B) {
		e := NewEngine(Config{
			InboxCapacity: 1,
			Pools:         map[tasks.PoolID]PoolEngineConfig{"general": {Concurrency: 1, BacklogLimit: 1}},
		})
		// Do not call Start; leave inbox un-drained with capacity 1 and fill it
		// so enqueueRequest exercises the full-inbox + deadline timeout path.
		e.mu.Lock()
		rootCtx, rootCancel := context.WithCancel(context.Background())
		e.rootCtx = rootCtx
		e.rootCancel = rootCancel
		e.inbox = make(chan *engineRequest, 1)
		e.mu.Unlock()
		defer rootCancel()

		e.inbox <- &engineRequest{op: opSweep}
		samples := make([]time.Duration, 0, min(b.N, 1000))
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			dctx, dcancel := context.WithTimeout(context.Background(), 50*time.Microsecond)
			start := time.Now()
			_, err := e.Submit(dctx, tasks.WorkSpec{
				ID:         tasks.TaskID(fmt.Sprintf("c3-e2e-full-%d", i)),
				QuotaOwner: "user:c3",
				Pool:       "general",
				Handler:    func(context.Context) error { return nil },
			})
			dur := time.Since(start)
			dcancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				b.Fatalf("unexpected err: %v", err)
			}
			if len(samples) < cap(samples) {
				samples = append(samples, dur)
			}
		}
		b.StopTimer()
		reportC3Percentiles(b, samples)
	})
}

// BenchmarkC3_PrimitiveCost provides exploratory primitive-level cost estimates
// for reply channels and timeout primitives. These are NOT semantically
// equivalent end-to-end Engine.Submit benchmarks.
func BenchmarkC3_PrimitiveCost(b *testing.B) {
	sampleErr := errors.New("sample")
	var sinkTicket tasks.Ticket
	var sinkErr error

	b.Run("reply_A_engineReply_chan", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			cell := &submitCell{}
			reply := make(chan engineReply, 1)
			cell.decide(decisionAccepted)
			reply <- engineReply{err: sampleErr}
			rep := <-reply
			sinkTicket, sinkErr = rep.ticket, rep.err
		}
	})

	b.Run("reply_B_submitCell_done_chan", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			cell := &submitCellB{done: make(chan struct{})}
			cell.decide(decisionAccepted)
			cell.ticket = nil
			cell.err = sampleErr
			close(cell.done)
			<-cell.done
			sinkTicket, sinkErr = cell.ticket, cell.err
		}
	})

	b.Run("reply_C_slim_submitReply_chan", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			cell := &submitCell{}
			reply := make(chan submitReplyC, 1)
			cell.decide(decisionAccepted)
			reply <- submitReplyC{err: sampleErr}
			rep := <-reply
			sinkTicket, sinkErr = rep.ticket, rep.err
		}
	})

	b.Run("timeout_context_WithTimeout", func(b *testing.B) {
		bg := context.Background()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			ctx, cancel := context.WithTimeout(bg, time.Second)
			_ = ctx.Done()
			cancel()
		}
	})

	b.Run("timeout_time_NewTimer", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			timer := time.NewTimer(time.Second)
			_ = timer.C
			timer.Stop()
		}
	})

	_, _ = sinkTicket, sinkErr
}
