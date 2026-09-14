from pathlib import Path


def read(path: str) -> str:
    return Path(path).read_text()


def write(path: str, content: str) -> None:
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(content)


def replace(path: str, old: str, new: str, count: int = 1) -> None:
    text = read(path)
    actual = text.count(old)
    if actual != count:
        raise RuntimeError(f"{path}: expected {count} occurrence(s), found {actual}: {old[:120]!r}")
    write(path, text.replace(old, new, count))


def replace_between(path: str, start: str, end: str, new: str) -> None:
    text = read(path)
    i = text.find(start)
    if i < 0:
        raise RuntimeError(f"{path}: start marker not found")
    j = text.find(end, i)
    if j < 0:
        raise RuntimeError(f"{path}: end marker not found")
    write(path, text[:i] + new + text[j:])


# 1) WorkerManager owns physical execution capacity, including interactive commands.
replace(
    "internal/workers/manager.go",
    'm.pools[PoolGeneral] = NewPool(PoolGeneral, 8, 200, queue.PolicyBlock)\n',
    'm.pools[PoolGeneral] = NewPool(PoolGeneral, 8, 200, queue.PolicyBlock)\n\tm.pools[PoolInteractive] = NewPool(PoolInteractive, 32, 128, queue.PolicyReject)\n',
)

write(
    "internal/workers/reservation.go",
    r'''package workers

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

const PoolInteractive = "interactive"

var ErrNoExecutionCapacity = errors.New("no physical execution capacity available")

// ExecutionReservation reserves one physical execution slot before a durable
// producer claims work. Releasing is idempotent so callers can safely cover
// cancellation, submission failure, and the queued->running handoff.
type ExecutionReservation struct {
	counter *atomic.Int32
	once    sync.Once
}

func (r *ExecutionReservation) Release() {
	if r == nil || r.counter == nil {
		return
	}
	r.once.Do(func() { r.counter.Add(-1) })
}

var poolReservations sync.Map // map[*Pool]*atomic.Int32

func reservationCounter(pool *Pool) *atomic.Int32 {
	counter, _ := poolReservations.LoadOrStore(pool, &atomic.Int32{})
	return counter.(*atomic.Int32)
}

// TryReserveExecution atomically reserves a physical worker slot for poolName.
// It is intentionally non-blocking: durable producers must not claim leases
// unless execution capacity is available now.
func (m *Manager) TryReserveExecution(poolName string) (*ExecutionReservation, error) {
	m.mu.RLock()
	pool, ok := m.pools[poolName]
	accepting := m.accepting
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("worker pool %q not found", poolName)
	}
	if !accepting || !pool.running.Load() {
		return nil, ErrNoExecutionCapacity
	}

	counter := reservationCounter(pool)
	for {
		reserved := counter.Load()
		busy := pool.busy.Load()
		if int(busy+reserved) >= pool.concurrency {
			return nil, ErrNoExecutionCapacity
		}
		if counter.CompareAndSwap(reserved, reserved+1) {
			return &ExecutionReservation{counter: counter}, nil
		}
	}
}
''',
)

# 2) Route Telegram interactive commands through TaskManager/WorkerManager.
write(
    "internal/telegram/dispatcher_workers.go",
    r'''package telegram

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
''',
)

replace(
    "internal/app/wiring_telegram.go",
    '\tdispatcher.Executor().SetRateLimiter(commandRateLimiterAdapter{limiter: core.cmdLimiter})\n',
    '\tdispatcher.Executor().SetRateLimiter(commandRateLimiterAdapter{limiter: core.cmdLimiter})\n\tdispatcher.SetWorkers(core.workerManager)\n',
)

replace(
    "internal/telegram/dispatcher_dispatch.go",
    '''\tselect {\n\tcase d.cmdSem <- struct{}{}:\n\tcase <-execCtx.Done():\n\t\tcancel()\n\t\treturn nil\n\t}\n\td.cmdWG.Add(1)\n\td.runningCommands.Add(1)\n\td.totalCommands.Add(1)\n\tgo func() {\n\t\tdefer func() {\n\t\t\td.runningCommands.Add(-1)\n\t\t\t<-d.cmdSem\n\t\t\td.cmdWG.Done()\n\t\t\tcancel()\n\t\t}()\n\t\t_ = d.executor.Execute(coreCtx, cmd)\n\t}()\n''',
    '''\ttaskOwner := fmt.Sprintf("telegram:user:%d", sender.ID)\n\tif sender.ID == 0 {\n\t\ttaskOwner = "telegram:unknown"\n\t}\n\ttaskID := fmt.Sprintf("cmd:%d:%d", chat.ID, msg.ID)\n\tcorrelationID := fmt.Sprintf("msg:%d:%d", chat.ID, msg.ID)\n\tif err := d.submitInteractiveCommand(execCtx, cancel, coreCtx, cmd, taskID, taskOwner, correlationID); err != nil {\n\t\td.logger.Warn("interactive command admission rejected",\n\t\t\tzap.String("command", cmdName),\n\t\t\tzap.Int64("chat_id", chat.ID),\n\t\t\tzap.Error(err),\n\t\t)\n\t}\n''',
)

# 3) Managed-job trigger can wait for the actual task terminal result.
replace(
    "internal/jobs/manager.go",
    '''\tcleanupCancel    context.CancelFunc\n\tcleanupWG        sync.WaitGroup\n}''',
    '''\tcleanupCancel     context.CancelFunc\n\tcleanupWG         sync.WaitGroup\n\tcompletionSeq     uint64\n\tcompletionWaiters map[string]map[uint64]chan error\n}''',
)
replace(
    "internal/jobs/manager.go",
    '''\t\tterminalAt:      make(map[string]time.Time),\n\t\tretention:        7 * 24 * time.Hour,''',
    '''\t\tterminalAt:        make(map[string]time.Time),\n\t\tcompletionWaiters: make(map[string]map[uint64]chan error),\n\t\tretention:         7 * 24 * time.Hour,''',
)
replace(
    "internal/jobs/manager.go",
    '''\t\t\t\tif repo != nil {\n\t\t\t\t\t_ = repo.UpdateState(context.Background(), jobID, StateCompleted, job.LastError, job.LastRun, job.NextRun)\n\t\t\t\t}\n\t\t\t\treturn nil''',
    '''\t\t\t\tif repo != nil {\n\t\t\t\t\t_ = repo.UpdateState(context.Background(), jobID, StateCompleted, job.LastError, job.LastRun, job.NextRun)\n\t\t\t\t}\n\t\t\t\tm.notifyCompletion(jobID, nil)\n\t\t\t\treturn nil''',
)
replace(
    "internal/jobs/manager.go",
    '''\t\t\tif repo != nil {\n\t\t\t\t_ = repo.UpdateState(context.Background(), jobID, jobState, jobErr, jLastRun, jNextRun)\n\t\t\t}\n\n\t\t\treturn err''',
    '''\t\t\tif repo != nil {\n\t\t\t\t_ = repo.UpdateState(context.Background(), jobID, jobState, jobErr, jLastRun, jNextRun)\n\t\t\t}\n\n\t\t\tm.notifyCompletion(jobID, err)\n\t\t\treturn err''',
)
replace(
    "internal/jobs/manager.go",
    'func (m *Manager) rollbackTrigger(ctx context.Context, job *Job, state JobState, lastRun time.Time, reason string) {',
    r'''func (m *Manager) registerCompletionWaiter(jobID string) (uint64, <-chan error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completionSeq++
	id := m.completionSeq
	waiters := m.completionWaiters[jobID]
	if waiters == nil {
		waiters = make(map[uint64]chan error)
		m.completionWaiters[jobID] = waiters
	}
	ch := make(chan error, 1)
	waiters[id] = ch
	return id, ch
}

func (m *Manager) unregisterCompletionWaiter(jobID string, waiterID uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	waiters := m.completionWaiters[jobID]
	if waiters == nil {
		return
	}
	delete(waiters, waiterID)
	if len(waiters) == 0 {
		delete(m.completionWaiters, jobID)
	}
}

func (m *Manager) notifyCompletion(jobID string, runErr error) {
	m.mu.Lock()
	waiters := m.completionWaiters[jobID]
	delete(m.completionWaiters, jobID)
	m.mu.Unlock()
	for _, ch := range waiters {
		ch <- runErr
		close(ch)
	}
}

// TriggerAndWait triggers one managed job and waits for the concrete Task to
// reach a terminal state. Scheduler uses this so durable schedule completion
// reflects execution, not merely successful admission to a worker queue.
func (m *Manager) TriggerAndWait(ctx context.Context, jobID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	waiterID, result := m.registerCompletionWaiter(jobID)
	if err := m.Trigger(ctx, jobID); err != nil {
		m.unregisterCompletionWaiter(jobID, waiterID)
		return err
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		m.unregisterCompletionWaiter(jobID, waiterID)
		return ctx.Err()
	}
}

func (m *Manager) rollbackTrigger(ctx context.Context, job *Job, state JobState, lastRun time.Time, reason string) {''',
)

# 4+5) Scheduler: remove its semaphore, reserve worker capacity before claim,
# and quiesce claim handoffs before workers close admission.
replace(
    "internal/scheduler/engine.go",
    '''\tmaxConcurrency int\n\tsem            chan struct{}\n\tmisfirePolicy  MisfirePolicy''',
    '''\tmaxConcurrency int\n\tmisfirePolicy  MisfirePolicy\n\n\tclaimMu   sync.Mutex\n\tclaimWG   sync.WaitGroup\n\tquiescing bool''',
)
replace(
    "internal/scheduler/engine.go",
    '''\t\tmaxConcurrency: defaultConcurrency,\n\t\tsem:            make(chan struct{}, defaultConcurrency),\n\t\tmisfirePolicy:  MisfireRunOnce,''',
    '''\t\tmaxConcurrency: defaultConcurrency,\n\t\tmisfirePolicy:  MisfireRunOnce,''',
)
replace(
    "internal/scheduler/engine.go",
    '''\te.maxConcurrency = n\n\te.sem = make(chan struct{}, n)''',
    '''\te.maxConcurrency = n''',
)
replace(
    "internal/scheduler/engine.go",
    '''\te.ctx, e.cancel = context.WithCancel(parentCtx)\n\te.running = true''',
    '''\te.ctx, e.cancel = context.WithCancel(parentCtx)\n\te.claimMu.Lock()\n\te.quiescing = false\n\te.claimMu.Unlock()\n\te.running = true''',
)
replace(
    "internal/scheduler/engine.go",
    'func (e *Engine) StopContext(ctx context.Context) error {\n\tif ctx == nil {\n\t\tctx = context.Background()\n\t}\n',
    '''func (e *Engine) StopContext(ctx context.Context) error {\n\tif ctx == nil {\n\t\tctx = context.Background()\n\t}\n\tif err := e.Quiesce(ctx); err != nil {\n\t\treturn err\n\t}\n''',
)
replace(
    "internal/scheduler/engine.go",
    '// Stop gracefully stops the scheduler using the provided context.\nfunc (e *Engine) Stop(ctx context.Context) error {\n\treturn e.StopContext(ctx)\n}\n',
    r'''// Stop gracefully stops the scheduler using the provided context.
func (e *Engine) Stop(ctx context.Context) error {
	return e.StopContext(ctx)
}

func (e *Engine) beginClaimBatch() bool {
	e.claimMu.Lock()
	defer e.claimMu.Unlock()
	if e.quiescing {
		return false
	}
	e.claimWG.Add(1)
	return true
}

func (e *Engine) endClaimBatch() {
	e.claimWG.Done()
}

// Quiesce stops new durable claims and waits for any claim->submit handoff that
// already started. This runs before WorkerManager.Quiesce via the runtime DAG,
// preventing fresh leases from being claimed after worker admission closes.
func (e *Engine) Quiesce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.claimMu.Lock()
	e.quiescing = true
	e.claimMu.Unlock()

	done := make(chan struct{})
	go func() {
		e.claimWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
''',
)

new_process_due = r'''func (e *Engine) processDueJobs(ctx context.Context, now time.Time) {
	if !e.beginClaimBatch() {
		return
	}
	defer e.endClaimBatch()

	// Do not claim jobs before Telegram service is ready.
	if e.svcFunc != nil && e.svcFunc() == nil {
		return
	}

	claimBatch := e.maxConcurrency
	if claimBatch <= 0 {
		claimBatch = 1
	}
	if claimBatch > 10 {
		claimBatch = 10
	}

	var reservations []*workers.ExecutionReservation
	if e.workers != nil {
		claimBatch = 0
		for len(reservations) < 10 {
			reservation, err := e.workers.TryReserveExecution(workers.PoolScheduler)
			if errors.Is(err, workers.ErrNoExecutionCapacity) {
				break
			}
			if err != nil {
				e.logger.Debug("scheduler execution capacity unavailable", zap.Error(err))
				break
			}
			reservations = append(reservations, reservation)
		}
		claimBatch = len(reservations)
	}
	if claimBatch <= 0 {
		return
	}

	claimedJobs, err := e.db.ClaimDueScheduledJobs(ctx, now, claimBatch, 90*time.Second)
	if err != nil {
		for _, reservation := range reservations {
			reservation.Release()
		}
		e.logger.Error("failed to claim due scheduled jobs", zap.Error(err))
		return
	}

	for i := len(claimedJobs); i < len(reservations); i++ {
		reservations[i].Release()
	}
	if len(reservations) > len(claimedJobs) {
		reservations = reservations[:len(claimedJobs)]
	}

	for i, job := range claimedJobs {
		j := job
		if e.workers != nil {
			reservation := reservations[i]
			taskID := fmt.Sprintf("sched-%d-%s", j.ID, j.ClaimToken)
			jobCtx, cancel := context.WithCancel(e.ctx)
			stopReservationWatch := context.AfterFunc(jobCtx, reservation.Release)
			e.registerActiveJob(j.ID, j.ClaimToken, cancel)
			taskName := fmt.Sprintf("%s-%d", j.ActionType, j.ID)

			task := tasks.Task{
				ID:        taskID,
				Owner:     "scheduler",
				Name:      taskName,
				Timeout:   90 * time.Second,
				CreatedAt: time.Now().UTC(),
				Run: func(taskCtx context.Context) error {
					// The physical worker has incremented Busy before Run, so the
					// reservation can now transition to the real busy accounting.
					reservation.Release()
					stopReservationWatch()
					defer func() {
						e.unregisterActiveJob(j.ID, j.ClaimToken)
						cancel()
					}()
					e.executeJob(taskCtx, j, cancel)
					return nil
				},
			}

			if err := e.workers.Submit(jobCtx, workers.PoolScheduler, task); err != nil {
				stopReservationWatch()
				reservation.Release()
				e.logger.Error("failed to submit scheduled job to worker pool", zap.Int64("job_id", j.ID), zap.Error(err))
				e.unregisterActiveJob(j.ID, j.ClaimToken)
				cancel()
			}
			continue
		}

		// Compatibility path for tests/embedders without WorkerManager. It is
		// intentionally synchronous: Scheduler no longer owns physical concurrency.
		jobCtx, cancel := context.WithCancel(e.ctx)
		e.registerActiveJob(j.ID, j.ClaimToken, cancel)
		e.executeJob(jobCtx, j, cancel)
		e.unregisterActiveJob(j.ID, j.ClaimToken)
		cancel()
	}
}

'''
replace_between(
    "internal/scheduler/engine.go",
    'func (e *Engine) processDueJobs(ctx context.Context, now time.Time) {',
    '// executeJob deliberately uses an at-least-once external execution model.',
    new_process_due,
)
replace(
    "internal/scheduler/engine.go",
    '\treturn e.jobsMgr.Trigger(ctx, jobID)\n',
    '\treturn e.jobsMgr.TriggerAndWait(ctx, jobID)\n',
)

# Focused regression tests for reservation and actual managed-job completion.
write(
    "internal/workers/reservation_test.go",
    r'''package workers

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTryReserveExecutionHonorsPhysicalConcurrency(t *testing.T) {
	m := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := m.Stop(stopCtx); err != nil {
			t.Fatal(err)
		}
	}()

	var reservations []*ExecutionReservation
	for i := 0; i < 4; i++ {
		r, err := m.TryReserveExecution(PoolScheduler)
		if err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
		reservations = append(reservations, r)
	}
	if _, err := m.TryReserveExecution(PoolScheduler); !errors.Is(err, ErrNoExecutionCapacity) {
		t.Fatalf("expected ErrNoExecutionCapacity, got %v", err)
	}

	reservations[0].Release()
	r, err := m.TryReserveExecution(PoolScheduler)
	if err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
	r.Release()
	for _, reservation := range reservations[1:] {
		reservation.Release()
	}
}
''',
)

write(
    "internal/jobs/wait_test.go",
    r'''package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/tasks"
)

type asyncTestSubmitter struct{}

func (asyncTestSubmitter) Submit(ctx context.Context, _ string, task tasks.Task) error {
	go func() { _ = task.Execute(ctx) }()
	return nil
}

func TestTriggerAndWaitReturnsConcreteExecutionResult(t *testing.T) {
	want := errors.New("execution failed")
	m := NewManager(asyncTestSubmitter{})
	if err := m.Register(Job{
		ID:    "managed-1",
		Owner: "test",
		Run: func(context.Context) error {
			return want
		},
	}); err != nil {
		t.Fatal(err)
	}

	err := m.TriggerAndWait(context.Background(), "managed-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected concrete run error %v, got %v", want, err)
	}
}
''',
)

# Remove this staging script from the actual code commit.
Path(__file__).unlink()
