package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type retryWorkerTicket struct {
	id      tasks.TaskID
	started chan<- struct{}
	release <-chan struct{}
}

func (t retryWorkerTicket) TaskID() tasks.TaskID { return t.id }
func (retryWorkerTicket) State() tasks.TaskState { return tasks.StateRunning }
func (retryWorkerTicket) Done() <-chan struct{}  { return nil }
func (retryWorkerTicket) Result() (tasks.TaskResult, bool) {
	return tasks.TaskResult{}, false
}
func (t retryWorkerTicket) Wait(ctx context.Context) (tasks.TaskResult, error) {
	if t.started != nil {
		t.started <- struct{}{}
	}
	if t.release != nil {
		select {
		case <-t.release:
		case <-ctx.Done():
			return tasks.TaskResult{}, ctx.Err()
		}
	}
	return tasks.TaskResult{TaskID: t.id, Outcome: tasks.OutcomeCompleted}, nil
}

func newRetryWorkerTestManager(idle time.Duration) (*Manager, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		retryQueue:       make(chan retryItem, 8),
		recoveryWake:     make(chan struct{}, 1),
		stopCh:           make(chan struct{}),
		baseCtx:          ctx,
		baseCancel:       cancel,
		done:             make(chan struct{}),
		tracked:          make(map[string]*trackedOccurrence),
		retryIdleTimeout: idle,
	}
	return m, cancel
}

func TestRetryWorkersAreLazyAndRetire(t *testing.T) {
	m, cancel := newRetryWorkerTestManager(10 * time.Millisecond)
	defer cancel()
	m.tracked["occ"] = &trackedOccurrence{}
	m.enqueueRetry(retryItem{
		occurrenceID: "occ",
		ticket:       retryWorkerTicket{id: "retry-lazy"},
	})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if m.retryRemaining.Load() == 0 {
			m.mu.RLock()
			_, tracked := m.tracked["occ"]
			m.mu.RUnlock()
			if !tracked {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("retry workers did not retire: running=%d", m.retryRemaining.Load())
}

func TestRetryWorkersScaleToDemand(t *testing.T) {
	m, cancel := newRetryWorkerTestManager(time.Second)
	defer cancel()

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	for i := 0; i < 2; i++ {
		occurrenceID := "occ-" + string(rune('a'+i))
		m.tracked[occurrenceID] = &trackedOccurrence{}
		m.enqueueRetry(retryItem{
			occurrenceID: occurrenceID,
			ticket: retryWorkerTicket{
				id: tasks.TaskID(occurrenceID), started: started, release: release,
			},
		})
	}

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("retry monitor failed to scale to concurrent demand")
		}
	}
	if got := m.retryRemaining.Load(); got < 2 {
		close(release)
		t.Fatalf("retry workers=%d, want at least 2", got)
	}
	close(release)
}
