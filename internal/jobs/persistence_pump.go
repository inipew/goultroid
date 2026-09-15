package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
)

var (
	ErrPumpClosed    = errors.New("persistence pump is closed")
	ErrPumpQueueFull = errors.New("persistence pump queue is saturated")
)

type persistenceRequest struct {
	ctx      context.Context
	execute  func(ctx context.Context) error
	resultCh chan error
}

// PersistencePump is a dedicated service with bounded concurrency that commits durable results (ADR 0006 §3.3).
// It does NOT borrow feature execution slots, preventing cycles where tasks wait on persistence to finish.
type PersistencePump struct {
	mu          sync.Mutex
	concurrency int
	queueCap    int
	requests    chan persistenceRequest
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
	running     bool
	accepting   bool
	done        chan struct{}
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
		concurrency: concurrency,
		queueCap:    queueCap,
	}
}

func (p *PersistencePump) Name() string           { return "persistence-pump" }
func (p *PersistencePump) Dependencies() []string { return []string{"database"} }

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
	p.running = true
	p.accepting = true

	for i := 0; i < p.concurrency; i++ {
		p.wg.Add(1)
		go p.workerLoop()
	}
	go func() {
		p.wg.Wait()
		close(p.done)
	}()
	return nil
}

func (p *PersistencePump) workerLoop() {
	defer p.wg.Done()
	for req := range p.requests {
		err := p.processRequest(req)
		if req.resultCh != nil {
			req.resultCh <- err
			close(req.resultCh)
		}
	}
}

func (p *PersistencePump) processRequest(req persistenceRequest) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
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

// Enqueue submits an operation to the persistence pump.
func (p *PersistencePump) Enqueue(ctx context.Context, op func(ctx context.Context) error) (<-chan error, error) {
	p.mu.Lock()
	if !p.accepting || !p.running {
		p.mu.Unlock()
		return nil, ErrPumpClosed
	}
	resCh := make(chan error, 1)
	req := persistenceRequest{
		ctx:      ctx,
		execute:  op,
		resultCh: resCh,
	}
	select {
	case p.requests <- req:
		p.mu.Unlock()
		return resCh, nil
	default:
		p.mu.Unlock()
		return nil, ErrPumpQueueFull
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
	p.mu.Lock()
	if p.done == nil {
		p.mu.Unlock()
		return nil
	}
	if p.running {
		p.accepting = false
		close(p.requests)
		p.running = false
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
			Details: fmt.Sprintf("persistence queue saturated (%d/%d)", len(p.requests), p.queueCap),
		}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}
