package telegram

import (
	"context"
	"sync"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
)

// TaskSubmitter is the narrow finite-execution surface consumed by Telegram.
// TaskEngine compatibility adapters and the legacy WorkerManager both satisfy
// this contract, which keeps dispatch independent from physical worker ownership.
type TaskSubmitter interface {
	Submit(ctx context.Context, poolName string, task tasks.Task) error
}

var dispatcherSubmitters sync.Map // map[*Dispatcher]TaskSubmitter

// SetTaskSubmitter attaches the finite execution authority used by command
// dispatch. Production wiring points this at the one-way TaskEngine adapter.
func (d *Dispatcher) SetTaskSubmitter(submitter TaskSubmitter) {
	if d == nil {
		return
	}
	if submitter == nil {
		dispatcherSubmitters.Delete(d)
		return
	}
	dispatcherSubmitters.Store(d, submitter)
}

// SetWorkers is retained for embedders during migration. It delegates to the
// narrow submitter contract and does not make Dispatcher depend on worker state.
func (d *Dispatcher) SetWorkers(manager *workers.Manager) {
	d.SetTaskSubmitter(manager)
}

func (d *Dispatcher) taskSubmitter() TaskSubmitter {
	if d == nil {
		return nil
	}
	value, ok := dispatcherSubmitters.Load(d)
	if !ok {
		return nil
	}
	return value.(TaskSubmitter)
}

func (d *Dispatcher) submitInteractiveCommand(
	ctx context.Context,
	cancel context.CancelFunc,
	coreCtx *core.Context,
	cmd core.Command,
	taskID string,
	owner string,
	correlationID string,
) error {
	submitter := d.taskSubmitter()
	if submitter == nil {
		// Standalone dispatcher tests and embedders may not wire the runtime
		// execution authority. Keep an asynchronous fallback path tracked by cmdWG.
		d.runningCommands.Add(1)
		d.totalCommands.Add(1)
		d.cmdWG.Add(1)
		go func() {
			defer d.runningCommands.Add(-1)
			defer d.cmdWG.Done()
			defer cancel()
			_ = d.executor.Execute(coreCtx, cmd)
		}()
		return nil
	}

	task := tasks.Task{
		ID:            taskID,
		Owner:         owner,
		Name:          "command:" + cmd.Name,
		Priority:      100,
		CorrelationID: correlationID,
		Run: func(taskCtx context.Context) error {
			d.runningCommands.Add(1)
			defer d.runningCommands.Add(-1)
			defer cancel()

			execCtx := *coreCtx
			execCtx.Ctx = taskCtx
			return d.executor.Execute(&execCtx, cmd)
		},
	}
	if cmd.Timeout > 0 {
		task.Timeout = cmd.Timeout
	}
	if err := submitter.Submit(ctx, string(workers.PoolInteractive), task); err != nil {
		cancel()
		return err
	}
	d.totalCommands.Add(1)
	return nil
}
