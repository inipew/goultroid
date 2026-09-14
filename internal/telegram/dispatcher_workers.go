package telegram

import (
	"context"
	"sync"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
)

var dispatcherWorkers sync.Map // map[*Dispatcher]*workers.Manager

// SetWorkers attaches the shared physical execution authority used by command
// dispatch. It is wired by the application composition root.
func (d *Dispatcher) SetWorkers(manager *workers.Manager) {
	if d == nil {
		return
	}
	if manager == nil {
		dispatcherWorkers.Delete(d)
		return
	}
	dispatcherWorkers.Store(d, manager)
}

func (d *Dispatcher) workerManager() *workers.Manager {
	if d == nil {
		return nil
	}
	value, ok := dispatcherWorkers.Load(d)
	if !ok {
		return nil
	}
	return value.(*workers.Manager)
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
	manager := d.workerManager()
	if manager == nil {
		// Standalone dispatcher tests and embedders may not wire the runtime
		// worker manager. Keep a synchronous compatibility path rather than
		// reintroducing a second semaphore/goroutine execution authority.
		d.runningCommands.Add(1)
		d.totalCommands.Add(1)
		defer d.runningCommands.Add(-1)
		defer cancel()
		return d.executor.Execute(coreCtx, cmd)
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
	if err := manager.Submit(ctx, workers.PoolInteractive, task); err != nil {
		cancel()
		return err
	}
	d.totalCommands.Add(1)
	return nil
}
