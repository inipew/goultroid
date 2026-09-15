package telegram

import (
	"context"
	"errors"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"
)

var ErrWorkersNotConfigured = errors.New("dispatcher: worker manager is required for command execution")

// SetWorkers attaches the shared physical execution authority used by command
// dispatch. It is wired by the application composition root.
func (d *Dispatcher) SetWorkers(manager *workers.Manager) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ownsWorkers && d.workers != nil && d.workers != manager {
		_ = d.workers.Stop(context.Background())
		d.ownsWorkers = false
	}
	d.workers = manager
}

func (d *Dispatcher) workerManager() *workers.Manager {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.workers
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
		cancel()
		return ErrWorkersNotConfigured
	}

	d.cmdWG.Add(1)
	task := tasks.Task{
		ID:            taskID,
		Owner:         owner,
		Name:          "command:" + cmd.Name,
		Priority:      100,
		CorrelationID: correlationID,
		Run: func(taskCtx context.Context) error {
			d.runningCommands.Add(1)
			defer d.runningCommands.Add(-1)
			defer d.cmdWG.Done()
			defer cancel()

			runCtx, runCancel := context.WithCancel(taskCtx)
			defer runCancel()

			stopWatching := context.AfterFunc(coreCtx.Ctx, func() {
				runCancel()
			})
			defer stopWatching()

			execCtx := *coreCtx
			execCtx.Ctx = runCtx
			return d.executor.Execute(&execCtx, cmd)
		},
	}
	if cmd.Timeout > 0 {
		task.Timeout = cmd.Timeout
	}
	if err := manager.Submit(ctx, workers.PoolInteractive, task); err != nil {
		d.cmdWG.Done()
		cancel()
		return err
	}
	d.totalCommands.Add(1)
	return nil
}
