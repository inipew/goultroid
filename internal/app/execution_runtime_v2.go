package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
)

// executionRuntimeV2 is the production composition seam for the replacement
// execution core. Compatibility producers translate one-way into TaskEngine;
// no legacy lifecycle state is mirrored back into the old managers.
type executionRuntimeV2 struct {
	catalog   *taskengine.Catalog
	registry  *execution.Registry
	executor  *workers.PhysicalExecutor
	engine    *taskengine.Engine
	submitter *execution.LegacySubmitter
}

func newExecutionRuntimeV2() (*executionRuntimeV2, error) {
	cfg := productionExecutionConfig()
	catalog, err := taskengine.NewCatalog(cfg)
	if err != nil {
		return nil, fmt.Errorf("create execution catalog: %w", err)
	}
	registry, err := execution.NewRegistry(catalog)
	if err != nil {
		return nil, fmt.Errorf("create execution registry: %w", err)
	}
	executor, err := workers.NewPhysicalExecutor(executionWorkerCounts(cfg), registry)
	if err != nil {
		return nil, fmt.Errorf("create physical executor: %w", err)
	}
	engine, err := taskengine.New(cfg, catalog, executor)
	if err != nil {
		return nil, fmt.Errorf("create task engine: %w", err)
	}
	pools := make([]tasks.PoolID, 0, len(cfg.Pools))
	for pool := range cfg.Pools {
		pools = append(pools, pool)
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i] < pools[j] })
	submitter, err := execution.NewLegacySubmitter(engine, registry, pools)
	if err != nil {
		return nil, fmt.Errorf("create compatibility submitter: %w", err)
	}
	return &executionRuntimeV2{
		catalog: catalog, registry: registry, executor: executor, engine: engine, submitter: submitter,
	}, nil
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
	return e.registry.Register(descriptor, handler)
}

func (e *executionRuntimeV2) Submitter() *execution.LegacySubmitter { return e.submitter }
func (e *executionRuntimeV2) Engine() *taskengine.Engine             { return e.engine }
func (e *executionRuntimeV2) Catalog() *taskengine.Catalog           { return e.catalog }
func (e *executionRuntimeV2) Registry() *execution.Registry          { return e.registry }

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
	if err := e.submitter.Start(ctx); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return errors.Join(
			fmt.Errorf("start execution submitter: %w", err),
			e.engine.Stop(stopCtx),
			e.executor.Stop(stopCtx),
		)
	}
	return nil
}

func (e *executionRuntimeV2) Quiesce(ctx context.Context) error { return e.engine.Quiesce(ctx) }

func (e *executionRuntimeV2) Drain(ctx context.Context) error {
	if err := e.engine.Drain(ctx); err != nil {
		return err
	}
	return e.submitter.Drain(ctx)
}

func (e *executionRuntimeV2) Stop(ctx context.Context) error {
	// Runtime calls Quiesce/Drain first, but keep Stop self-contained for startup
	// rollback and direct component tests.
	var errs []error
	if err := e.engine.Quiesce(ctx); err != nil {
		errs = append(errs, err)
	} else if err := e.engine.Drain(ctx); err != nil {
		errs = append(errs, err)
	} else if err := e.submitter.Drain(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := e.submitter.Stop(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := e.engine.Stop(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := e.executor.Stop(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (e *executionRuntimeV2) Health(context.Context) runtime.ComponentHealth {
	stats := e.engine.Stats()
	if stats.ResultCapacity == 0 {
		return runtime.ComponentHealth{Status: runtime.HealthUnhealthy, Details: "task engine is not running"}
	}
	if stats.ResultCreditsUsed == stats.ResultCapacity {
		return runtime.ComponentHealth{Status: runtime.HealthDegraded, Details: "task result credits exhausted"}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}
