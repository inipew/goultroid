package telegram

import (
	"context"
	"errors"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
)

var ErrTasksNotConfigured = errors.New("dispatcher: task client is required for feature execution")

// SetTasks attaches the only execution authority used by all Telegram feature
// work. Dispatcher constructors deliberately do not create a fallback runtime.
func (d *Dispatcher) SetTasks(client tasks.Client) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.tasks = client
	d.mu.Unlock()
}

func (d *Dispatcher) taskClient() tasks.Client {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.tasks
}

func (d *Dispatcher) submitInteractiveCommand(ctx context.Context, cancel context.CancelFunc, coreCtx *core.Context, cmd core.Command, taskID string, owner string, correlationID string) error {
	return d.submitInteractiveCommandWithAdmission(ctx, cancel, coreCtx, cmd, taskID, owner, correlationID, nil)
}

// submitInteractiveCommandWithAdmission gates handler execution until the
// caller has durably committed any post-Submit admission state. TaskEngine may
// dispatch a worker before Submit returns, so this gate closes the otherwise
// observable race between TaskEngine admission and durable idempotency accept.
func (d *Dispatcher) submitInteractiveCommandWithAdmission(
	ctx context.Context,
	cancel context.CancelFunc,
	coreCtx *core.Context,
	cmd core.Command,
	taskID string,
	owner string,
	correlationID string,
	onAdmitted func() error,
) error {
	client := d.taskClient()
	if client == nil {
		cancel()
		return ErrTasksNotConfigured
	}

	var admissionReady chan struct{}
	var admissionErr error
	if onAdmitted != nil {
		admissionReady = make(chan struct{})
	}

	d.cmdWG.Add(1)
	_, err := client.Submit(ctx, tasks.WorkSpec{
		ID:               tasks.TaskID(taskID),
		Scope:            cmd.Scope,
		QuotaOwner:       tasks.OwnerID(owner),
		Pool:             "interactive",
		Class:            tasks.PriorityInteractive,
		OrderingKey:      correlationID,
		ExecutionTimeout: cmd.Timeout,
		Resources:        append([]tasks.ResourceRequirement(nil), cmd.Resources...),
		Handler: func(taskCtx context.Context) error {
			if admissionReady != nil {
				select {
				case <-admissionReady:
				case <-taskCtx.Done():
					return taskCtx.Err()
				}
				if admissionErr != nil {
					return admissionErr
				}
			}

			d.runningCommands.Add(1)
			defer d.runningCommands.Add(-1)
			runCtx, runCancel := context.WithCancel(taskCtx)
			defer runCancel()
			stopWatching := context.AfterFunc(coreCtx.Ctx, runCancel)
			defer stopWatching()
			execCtx := *coreCtx
			execCtx.Ctx = runCtx
			return d.executor.Execute(&execCtx, cmd)
		},
		OnComplete: func(tasks.TaskResult) {
			d.cmdWG.Done()
			cancel()
		},
	})
	if err != nil {
		d.cmdWG.Done()
		cancel()
		return err
	}

	if onAdmitted != nil {
		admissionErr = onAdmitted()
		close(admissionReady)
		if admissionErr != nil {
			// The admitted task is gated and therefore cannot have entered the
			// command handler. Best-effort cancellation reclaims its queue/runtime
			// accounting early; the gate itself guarantees fail-closed execution.
			_, _ = client.Cancel(tasks.TaskID(taskID), tasks.CausePersistenceFailure)
			return admissionErr
		}
	}

	d.totalCommands.Add(1)
	return nil
}
