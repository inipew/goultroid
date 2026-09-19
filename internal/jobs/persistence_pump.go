package jobs

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/runtime"
)

const (
	DefaultPersistenceRetainedBytes int64 = 64 << 20 // 64 MiB
	defaultPersistenceRequestBytes  int64 = 512
)

var (
	ErrPumpClosed      = errors.New("persistence pump is closed")
	ErrPumpQueueFull   = errors.New("persistence pump queue is saturated")
	ErrPumpByteBudget  = errors.New("persistence pump retained-byte budget is saturated")
)

type persistenceRequest struct {
	ctx           context.Context
	execute       func(ctx context.Context) error
	resultCh      chan error
	ack           func(error)
	retainedBytes int64
}

// PersistencePumpStats is a bounded-cardinality view of persistence admission.
type PersistencePumpStats struct {
	QueueDepth     int
	QueueCapacity  int
	RetainedBytes  int64
	RetainedCap    int64
	ByteRejections uint64
	Panics         uint64
}

// PersistencePump is a dedicated service with bounded concurrency that commits durable results (ADR 0006 §3.3).
// It does NOT borrow feature execution slots, preventing cycles where tasks wait on persistence to finish.
type PersistencePump struct {
	mu            sync.Mutex
	concurrency   int
	queueCap      int
	idleTimeout   time.Duration
	requests      chan persistenceRequest
	wg            sync.WaitGroup
	remaining     atomic.Int64
	queued        atomic.Int64
	active        atomic.Int64
	doneOnce      sync.Once
	ctx           context.Context
	cancel        context.CancelFunc
	running       bool
	accepting     bool
	done          chan struct{}
	panicReporter   core.PanicReporter
	panicCount      atomic.Uint64
	maxRetainedBytes int64
	retainedBytes    int64
	byteRejections   atomic.Uint64
}

var _ runtime.Component = (*PersistencePump)(nil)

// NewPersistencePump creates a persistence pump with fixed concurrency and bounded queue.
func NewPersistencePump(concurrency int, queueCap int) *PersistencePump {
	if concurrency <= 0 {
		concurrency = 2
	}
	if queueCap <= 0 {
		queueCap = 256
	}
	return &PersistencePump{
		concurrency:      concurrency,
		queueCap:         queueCap,
		idleTimeout:      30 * time.Second,
		maxRetainedBytes: DefaultPersistenceRetainedBytes,
	}
}

func (p *PersistencePump) Name() string           { return "persistence-pump" }
func (p *PersistencePump) Dependencies() []string { return []string{"database"} }

// SetMaxRetainedBytes sets the total byte admission budget for queued and
// in-flight persistence callbacks. It is safe before Start and while running.
func (p *PersistencePump) SetMaxRetainedBytes(max int64) {
	if p == nil || max <= 0 {
		return
	}
	p.mu.Lock()
	p.maxRetainedBytes = max
	p.mu.Unlock()
}

// Stats returns persistence queue/retained-memory diagnostics.
func (p *PersistencePump) Stats() PersistencePumpStats {
	if p == nil {
		return PersistencePumpStats{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	depth := 0
	if p.requests != nil {
		depth = len(p.requests)
	}
	return PersistencePumpStats{
		QueueDepth:     depth,
		QueueCapacity:  p.queueCap,
		RetainedBytes:  p.retainedBytes,
		RetainedCap:    p.maxRetainedBytes,
		ByteRejections: p.byteRejections.Load(),
		Panics:         p.panicCount.Load(),
	}
}

// SetPanicReporter attaches the framework panic reporter used by worker
// boundaries. Recovered persistence panics remain operation failures, while the
// stack and component identity are reported for diagnostics.
func (p *PersistencePump) SetPanicReporter(reporter core.PanicReporter) {
	p.mu.Lock()
	p.panicReporter = reporter
	p.mu.Unlock()
}

// Panics returns the number of recovered persistence operation panics.
func (p *PersistencePump) Panics() uint64 {
	if p == nil {
		return 0
	}
	return p.panicCount.Load()
}

// Start initializes the pump goroutines.
func (p *PersistencePump) Start(parent context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx != nil {
		return errors.New("persistence pump already running")
	}
	if parent == nil {
		parent = context.Background()
	}
	p.ctx, p.cancel = context.WithCancel(parent)
	p.requests = make(chan persistenceRequest, p.queueCap)
	p.done = make(chan struct{})
	p.doneOnce = sync.Once{}
	p.remaining.Store(0)
	p.queued.Store(0)
	p.active.Store(0)
	p.running = true
	p.accepting = true
	if p.idleTimeout <= 0 {
		p.idleTimeout = 30 * time.Second
	}
	return nil
}

func (p *PersistencePump) ensureWorkersLocked(done chan struct{}) {
	if !p.running {
		return
	}
	target := int(p.active.Load() + p.queued.Load())
	if target < 1 {
		target = 1
	}
	if target > p.concurrency {
		target = p.concurrency
	}
	running := int(p.remaining.Load())
	for running < target {
		p.remaining.Add(1)
		running++
		p.wg.Add(1)
		go p.workerLoop(done)
	}
}

func (p *PersistencePump) workerDone(done chan struct{}) {
	p.wg.Done()
	p.mu.Lock()
	remaining := p.remaining.Add(-1)
	if !p.running {
		if remaining == 0 {
			p.doneOnce.Do(func() { close(done) })
		}
		p.mu.Unlock()
		return
	}
	if p.queued.Load() > 0 {
		p.ensureWorkersLocked(done)
	}
	p.mu.Unlock()
}

func (p *PersistencePump) workerLoop(done chan struct{}) {
	defer p.workerDone(done)
	timer := time.NewTimer(p.idleTimeout)
	defer timer.Stop()

	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(p.idleTimeout)
	}

	for {
		select {
		case req, ok := <-p.requests:
			if !ok {
				return
			}
			p.active.Add(1)
			p.queued.Add(-1)
			err := p.processRequest(req)
			p.active.Add(-1)
			p.mu.Lock()
			p.retainedBytes -= req.retainedBytes
			if p.retainedBytes < 0 {
				p.retainedBytes = 0
			}
			p.mu.Unlock()
			if req.ack != nil {
				req.ack(err)
			} else if req.resultCh != nil {
				req.resultCh <- err
				close(req.resultCh)
			}
			resetTimer()
		case <-timer.C:
			p.mu.Lock()
			retire := p.running && p.queued.Load() == 0
			p.mu.Unlock()
			if retire {
				return
			}
			timer.Reset(p.idleTimeout)
		}
	}
}

func (p *PersistencePump) processRequest(req persistenceRequest) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			p.panicCount.Add(1)
			stack := debug.Stack()
			p.mu.Lock()
			reporter := p.panicReporter
			p.mu.Unlock()
			if reporter != nil {
				reporter.ReportPanic(core.PanicReport{
					Owner:     "jobs",
					Component: "persistence-pump",
					Value:     recovered,
					Stack:     stack,
					At:        time.Now().UTC(),
				})
			}
			err = fmt.Errorf("persistence operation panic: %v", recovered)
		}
	}()
	parent := req.ctx
	if parent == nil {
		parent = p.ctx
	}
	opCtx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	if p.ctx.Err() != nil {
		cancel()
	}
	if err := opCtx.Err(); err != nil {
		return err
	}
	if req.execute != nil {
		return req.execute(opCtx)
	}
	return nil
}

// Enqueue submits a legacy-weight operation to the persistence pump.
func (p *PersistencePump) Enqueue(ctx context.Context, op func(ctx context.Context) error) (<-chan error, error) {
	return p.EnqueueSized(ctx, defaultPersistenceRequestBytes, op)
}

// EnqueueSized submits an operation with an explicit retained-memory charge.
// The charge remains held until the callback physically completes, not merely
// until a worker dequeues it.
func (p *PersistencePump) EnqueueSized(ctx context.Context, retainedBytes int64, op func(ctx context.Context) error) (<-chan error, error) {
	resCh := make(chan error, 1)
	if err := p.enqueueRequest(ctx, retainedBytes, op, resCh, nil); err != nil {
		return nil, err
	}
	return resCh, nil
}

// EnqueueSizedAck is the allocation-light acknowledgement path used by
// TaskEngine in production. It avoids a per-commit result channel consumer:
// the persistence worker invokes ack exactly once after the operation returns.
func (p *PersistencePump) EnqueueSizedAck(ctx context.Context, retainedBytes int64, op func(ctx context.Context) error, ack func(error)) error {
	if ack == nil {
		return errors.New("persistence acknowledgement callback is nil")
	}
	return p.enqueueRequest(ctx, retainedBytes, op, nil, ack)
}

func (p *PersistencePump) enqueueRequest(ctx context.Context, retainedBytes int64, op func(ctx context.Context) error, resultCh chan error, ack func(error)) error {
	if retainedBytes <= 0 {
		retainedBytes = defaultPersistenceRequestBytes
	}
	p.mu.Lock()
	if !p.accepting || !p.running {
		p.mu.Unlock()
		return ErrPumpClosed
	}
	if p.maxRetainedBytes > 0 && retainedBytes > p.maxRetainedBytes-p.retainedBytes {
		p.byteRejections.Add(1)
		p.mu.Unlock()
		return ErrPumpByteBudget
	}
	req := persistenceRequest{
		ctx:           ctx,
		execute:       op,
		resultCh:      resultCh,
		ack:           ack,
		retainedBytes: retainedBytes,
	}
	p.queued.Add(1)
	select {
	case p.requests <- req:
		p.retainedBytes += retainedBytes
		p.ensureWorkersLocked(p.done)
		p.mu.Unlock()
		return nil
	default:
		p.queued.Add(-1)
		p.mu.Unlock()
		return ErrPumpQueueFull
	}
}

// Quiesce stops accepting new requests.
func (p *PersistencePump) Quiesce(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accepting = false
	return nil
}

// Drain waits for in-flight persistence requests to be committed.
func (p *PersistencePump) Drain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	if p.done == nil {
		p.mu.Unlock()
		return nil
	}
	if p.running {
		p.accepting = false
		close(p.requests)
		p.running = false
		if p.remaining.Load() == 0 {
			p.doneOnce.Do(func() { close(p.done) })
		}
	}
	done := p.done
	p.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop terminates the pump.
func (p *PersistencePump) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	err := p.Drain(ctx)
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	return err
}

// Health evaluates component health.
func (p *PersistencePump) Health(ctx context.Context) runtime.ComponentHealth {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return runtime.ComponentHealth{
			Status:  runtime.HealthDegraded,
			Details: "persistence pump is stopped",
		}
	}
	if len(p.requests) >= p.queueCap {
		return runtime.ComponentHealth{
			Status:  runtime.HealthDegraded,
			Details: fmt.Sprintf("persistence queue saturated (%d/%d)", len(p.requests), p.queueCap)}
	}
	if p.maxRetainedBytes > 0 && p.retainedBytes >= p.maxRetainedBytes {
		return runtime.ComponentHealth{
			Status:  runtime.HealthDegraded,
			Details: fmt.Sprintf("persistence retained-byte budget saturated (%d/%d)", p.retainedBytes, p.maxRetainedBytes)}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy, Details: fmt.Sprintf("queue=%d/%d retained_bytes=%d/%d", len(p.requests), p.queueCap, p.retainedBytes, p.maxRetainedBytes)}
}
