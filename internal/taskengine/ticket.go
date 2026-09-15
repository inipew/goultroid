package taskengine

import (
	"context"

	"github.com/inipew/goultroid/internal/tasks"
)

type engineTicket struct {
	taskID tasks.TaskID
	engine *Engine
	done   chan struct{}
}

func (t *engineTicket) TaskID() tasks.TaskID {
	return t.taskID
}

func (t *engineTicket) State() tasks.TaskState {
	return t.engine.taskState(t.taskID)
}

func (t *engineTicket) Done() <-chan struct{} {
	return t.done
}

func (t *engineTicket) Result() (tasks.TaskResult, bool) {
	return t.engine.taskResult(t.taskID)
}

func (t *engineTicket) Wait(ctx context.Context) (tasks.TaskResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-t.done:
		res, _ := t.engine.taskResult(t.taskID)
		return res, nil
	case <-ctx.Done():
		return tasks.TaskResult{
			TaskID:  t.taskID,
			Outcome: tasks.OutcomeCancelled,
			Cause:   tasks.CauseTimeout,
		}, ctx.Err()
	}
}
