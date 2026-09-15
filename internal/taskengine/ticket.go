package taskengine

import (
	"context"

	"github.com/inipew/goultroid/internal/tasks"
)

type engineTicket struct {
	taskID tasks.TaskID
	engine *Engine
	done   chan struct{}
	rec    *taskRecord
}

func (t *engineTicket) TaskID() tasks.TaskID {
	return t.taskID
}

func (t *engineTicket) State() tasks.TaskState {
	// Always linearize through the control loop; direct rec.state reads would
	// race the single writer.
	return t.engine.taskState(t.taskID)
}

func (t *engineTicket) Done() <-chan struct{} {
	return t.done
}

func (t *engineTicket) Result() (tasks.TaskResult, bool) {
	select {
	case <-t.done:
		// done is closed by runLoop after the terminal result write, so the
		// read below observes the happens-before edge of channel close.
		// Re-validate via control loop in case the record was evicted.
		if t.rec != nil {
			select {
			case <-t.done:
				// Copy under happens-before; rec is never mutated after done.
				res := t.rec.result
				state := t.engine.taskState(t.taskID)
				if state == tasks.StateCompleted || state == tasks.StateFailed ||
					state == tasks.StateCancelled || state == tasks.StateTimedOut {
					return res, true
				}
				// Evicted: fall through to control-loop fetch (will miss).
			default:
			}
		}
		return t.engine.taskResult(t.taskID)
	default:
		return t.engine.taskResult(t.taskID)
	}
}

func (t *engineTicket) Wait(ctx context.Context) (tasks.TaskResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-t.done:
		if t.rec != nil {
			res, ok := t.engine.taskResult(t.taskID)
			if ok {
				return res, nil
			}
			return t.rec.result, nil
		}
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
