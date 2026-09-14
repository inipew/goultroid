package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
)

type executionHandlerKey struct {
	name    string
	version uint16
}

// executionHandlerRegistry is an instance-owned executable capability table.
// WorkSpec only carries stable HandlerRef values; executable closures never
// enter task state or persisted job values.
type executionHandlerRegistry struct {
	mu       sync.RWMutex
	handlers map[executionHandlerKey]tasks.HandlerFunc
}

func newExecutionHandlerRegistry() *executionHandlerRegistry {
	return &executionHandlerRegistry{handlers: make(map[executionHandlerKey]tasks.HandlerFunc)}
}

func (r *executionHandlerRegistry) ResolveHandler(ref tasks.HandlerRef) (tasks.HandlerFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, ok := r.handlers[executionHandlerKey{name: ref.Name(), version: ref.Version()}]
	return handler, ok
}

func (r *executionHandlerRegistry) register(ref tasks.HandlerRef, handler tasks.HandlerFunc) error {
	if ref.IsZero() || handler == nil {
		return errors.New("handler reference and implementation are required")
	}
	key := executionHandlerKey{name: ref.Name(), version: ref.Version()}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[key]; exists {
		return fmt.Errorf("execution handler already registered: %s@%d", ref.Name(), ref.Version())
	}
	r.handlers[key] = handler
	return nil
}

// executionRuntimeV2 is the production composition seam for the replacement
// execution core. Producers remain on the legacy path until their ownership
// partition is migrated; this component never mirrors legacy task lifecycle
// state and therefore cannot become a second execution authority accidentally.
type executionRuntimeV2 struct {
	catalog  *taskengine.Catalog
	handlers *executionHandlerRegistry
	executor *workers.PhysicalExecutor
	engine   *taskengine.Engine
}

func newExecutionRuntimeV2() (*executionRuntimeV2, error) {
	cfg := productionExecutionConfig()
	catalog, err := taskengine.NewCatalog(cfg)
	if err != nil {
		return nil, fmt.Errorf("create execution catalog: %w", err)
	}
	handlers := newExecutionHandlerRegistry()
	executor, err := workers.NewPhysicalExecutor(executionWorkerCounts(cfg), handlers)
	if err != nil {
		return nil, fmt.Errorf("create physical executor: %w", err)
	}
	engine, err := taskengine.New(cfg, catalog, executor)
	if err != nil {
		return nil, fmt.Errorf("create task engine: %w", err)
	}
	return &executionRuntimeV2{catalog: catalog, handlers: handlers, executor: executor, engine: engine}, nil
}

func productionExecutionConfig() taskengine.Config {
	return taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolLimits{
			workers.PoolGeneral:      {Workers: 8, MaxWaiting: 128, MaxWaitingBytes: 8 << 20},
			workers.PoolInteractive:  {Workers: 32, MaxWaiting: 128, MaxWaitingBytes: 8 << 20},
			workers.PoolDownload:     {Workers: 3, MaxWaiting: 32, MaxWaitingBytes: 16 << 20},
			workers.PoolMediaProcess: {Workers: 2, MaxWaiting: 16, MaxWaitingBytes: 16 << 20},
			workers.PoolScheduler:    {Workers: 4, MaxWaiting: 64, MaxWaitingBytes: 4 << 20},
		},
		ClassQuantum: map[tasks.PriorityClass]int{
			tasks.PriorityInteractive: 8,
			tasks.PriorityNormal:      4,
			tasks.PriorityBackground:  2,
			tasks.PriorityMaintenance: 1,
		},
		ResourceCapacity:       map[string]uint32{},
		ResultCredits:          512,
		ControlInboxCapacity:   256,
		PrepareQueueCapacity:   64,
		PersistenceCapacity:    64,
		DefaultOwner:           taskengine.OwnerLimits{MaxWaiting: 64, MaxActive: 4, MaxWaitingBytes: 8 << 20, Weight: 1},
		Owners:                 map[tasks.QuotaOwner]taskengine.OwnerLimits{},
		AdmissionDecisionLimit: 250 * time.Millisecond,
		QueueTimeout:           30 * time.Second,
		PrepareTimeout:         5 * time.Second,
		ExecutionTimeout:       2 * time.Minute,
		ResultRetention:        time.Minute,
	}
}

func executionWorkerCounts(cfg taskengine.Config) map[tasks.PoolID]int {
	counts := make(map[tasks.PoolID]int, len(cfg.Pools))
	for pool, limits := range cfg.Pools {
		counts[pool] = limits.Workers
	}
	return counts
}

func (e *executionRuntimeV2) registerHandler(descriptor taskengine.HandlerDescriptor, handler tasks.HandlerFunc) error {
	if err := e.catalog.RegisterHandler(descriptor); err != nil {
		return err
	}
	if err := e.handlers.register(descriptor.Ref, handler); err != nil {
		return fmt.Errorf("register executable handler: %w", err)
	}
	return nil
}

func (e *executionRuntimeV2) Name() string { return "execution-v2" }

func (e *executionRuntimeV2) Dependencies() []string { return []string{"database"} }

func (e *executionRuntimeV2) Start(ctx context.Context) error {
	if err := e.executor.Start(ctx); err != nil {
		return fmt.Errorf("start physical executor: %w", err)
	}
	if err := e.engine.Start(ctx); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return errors.Join(fmt.Errorf("start task engine: %w", err), e.executor.Stop(stopCtx))
	}
	return nil
}

func (e *executionRuntimeV2) Quiesce(ctx context.Context) error { return e.engine.Quiesce(ctx) }

func (e *executionRuntimeV2) Drain(ctx context.Context) error { return e.engine.Drain(ctx) }

func (e *executionRuntimeV2) Stop(ctx context.Context) error {
	return errors.Join(e.engine.Stop(ctx), e.executor.Stop(ctx))
}

func (e *executionRuntimeV2) Health(context.Context) runtime.ComponentHealth {
	stats := e.engine.Stats()
	if stats.ResultCapacity == 0 {
		return runtime.ComponentHealth{Status: runtime.HealthUnhealthy, Details: "task engine is not running"}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}
