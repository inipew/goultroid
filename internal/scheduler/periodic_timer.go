package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type periodicTaskSubmitter interface {
	Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error)
}

type periodicRegistration struct {
	Owner      string
	Name       string
	Interval   time.Duration
	Options    PeriodicTaskOptions
	Task       TaskFunc
	NextRun    time.Time
	Generation uint64
	Running    bool
	Attempt    int
	RunCancel  context.CancelFunc
	Runs       int64
	Failures   int64
	LastRunAt  time.Time
	LastError  string
	heapEntry  *TimerEntry
}

func periodicKey(owner, name string) string {
	if owner == "" || owner == "runtime" {
		return name
	}
	return owner + "/" + name
}

// periodicCoordinator multiplexes all runtime periodic tasks onto an indexed min-heap timer.
// It decides WHEN work is due; physical execution is delegated to TaskEngine through submitter
// and never owns an execution goroutine (ADR 0006 §8).
type periodicCoordinator struct {
	mu        sync.Mutex
	entries   map[string]*periodicRegistration
	heap      *IndexedHeap
	wake      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	running   bool
	seq       uint64
	logger    *zap.Logger
	submitter periodicTaskSubmitter
}

func newPeriodicCoordinator(logger *zap.Logger) *periodicCoordinator {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &periodicCoordinator{
		entries: make(map[string]*periodicRegistration),
		heap:    NewIndexedHeap(),
		wake:    make(chan struct{}, 1),
		logger:  logger,
	}
}

func (c *periodicCoordinator) SetSubmitter(submitter periodicTaskSubmitter) {
	c.mu.Lock()
	c.submitter = submitter
	c.mu.Unlock()
	c.notify()
}

func (c *periodicCoordinator) notify() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *periodicCoordinator) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return errors.New("periodic coordinator already running")
	}
	c.ctx, c.cancel = context.WithCancel(parent)
	c.running = true
	c.wg.Add(1)
	c.mu.Unlock()
	go c.loop()
	return nil
}

func (c *periodicCoordinator) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	c.running = false
	cancel := c.cancel
	var runCancels []context.CancelFunc
	for _, entry := range c.entries {
		if entry.RunCancel != nil {
			runCancels = append(runCancels, entry.RunCancel)
		}
	}
	c.entries = make(map[string]*periodicRegistration)
	c.heap = NewIndexedHeap()
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, runCancel := range runCancels {
		runCancel()
	}
	c.notify()

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *periodicCoordinator) Register(name string, interval time.Duration, options PeriodicTaskOptions, task TaskFunc) error {
	owner := strings.TrimSpace(options.Owner)
	if owner == "" {
		owner = "runtime"
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("task name cannot be empty")
	}
	if interval <= 0 {
		return errors.New("interval must be positive")
	}
	if task == nil {
		return errors.New("task function cannot be nil")
	}
	if options.MaxAttempts < 0 {
		return errors.New("max attempts cannot be negative")
	}
	if options.MaxAttempts == 0 {
		options.MaxAttempts = 1
	}
	if options.RetryDelay < 0 {
		return errors.New("retry delay cannot be negative")
	}
	options.Owner = owner

	c.mu.Lock()
	if !c.running || c.ctx == nil {
		c.mu.Unlock()
		return errors.New("scheduler engine is not running: call Start() first")
	}
	c.seq++
	generation := c.seq
	key := periodicKey(owner, name)

	var oldCancel context.CancelFunc
	if old := c.entries[key]; old != nil {
		oldCancel = old.RunCancel
		if old.heapEntry != nil {
			c.heap.Remove(old.heapEntry)
			old.heapEntry = nil
		}
	}

	nextRun := time.Now().Add(interval)
	reg := &periodicRegistration{
		Owner:      owner,
		Name:       name,
		Interval:   interval,
		Options:    options,
		Task:       task,
		NextRun:    nextRun,
		Generation: generation,
	}
	timerEntry := &TimerEntry{
		Kind:       TimerScheduleOccurrence,
		Owner:      owner,
		ID:         key,
		Generation: generation,
		Deadline:   nextRun,
		Data:       reg,
	}
	reg.heapEntry = timerEntry
	c.heap.Push(timerEntry)
	c.entries[key] = reg
	c.mu.Unlock()

	if oldCancel != nil {
		oldCancel()
	}
	c.notify()
	return nil
}

func (c *periodicCoordinator) Unregister(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("task name cannot be empty")
	}
	c.mu.Lock()
	// Direct key match first (matches default runtime owner or exact key)
	entry, ok := c.entries[name]
	targetKey := name
	if !ok {
		// Search across registrations for entry.Name == name
		for k, e := range c.entries {
			if e.Name == name {
				entry = e
				targetKey = k
				ok = true
				break
			}
		}
	}

	var runCancel context.CancelFunc
	if ok {
		if entry.heapEntry != nil {
			c.heap.Remove(entry.heapEntry)
			entry.heapEntry = nil
		}
		runCancel = entry.RunCancel
		delete(c.entries, targetKey)
	}
	c.mu.Unlock()
	if !ok {
		return errors.New("task not found")
	}
	if runCancel != nil {
		runCancel()
	}
	c.notify()
	return nil
}

func (c *periodicCoordinator) UnregisterOwned(owner, name string) error {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	key := periodicKey(owner, name)
	c.mu.Lock()
	entry, ok := c.entries[key]
	var runCancel context.CancelFunc
	if ok {
		if entry.heapEntry != nil {
			c.heap.Remove(entry.heapEntry)
			entry.heapEntry = nil
		}
		runCancel = entry.RunCancel
		delete(c.entries, key)
	}
	c.mu.Unlock()
	if !ok {
		return errors.New("task not found")
	}
	if runCancel != nil {
		runCancel()
	}
	c.notify()
	return nil
}

func (c *periodicCoordinator) UnregisterByOwner(owner string) int {
	cleanOwner := strings.TrimSpace(owner)
	if cleanOwner == "" {
		return 0
	}
	c.mu.Lock()
	var cancels []context.CancelFunc
	count := 0
	for key, entry := range c.entries {
		if entry.Owner == cleanOwner || entry.Owner == "plugin:"+cleanOwner {
			if entry.heapEntry != nil {
				c.heap.Remove(entry.heapEntry)
				entry.heapEntry = nil
			}
			if entry.RunCancel != nil {
				cancels = append(cancels, entry.RunCancel)
			}
			delete(c.entries, key)
			count++
		}
	}
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	if count > 0 {
		c.notify()
	}
	return count
}

func (c *periodicCoordinator) Snapshots() []PeriodicTaskSnapshot {
	c.mu.Lock()
	snapshots := make([]PeriodicTaskSnapshot, 0, len(c.entries))
	for _, entry := range c.entries {
		snapshots = append(snapshots, PeriodicTaskSnapshot{
			Owner:     entry.Owner,
			Name:      entry.Name,
			Runs:      entry.Runs,
			Failures:  entry.Failures,
			LastRunAt: entry.LastRunAt,
			LastError: entry.LastError,
		})
	}
	c.mu.Unlock()
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Name < snapshots[j].Name })
	return snapshots
}

func (c *periodicCoordinator) syncHeapLocked() {
	for _, entry := range c.entries {
		if entry.Running {
			continue
		}
		if entry.heapEntry == nil {
			entry.heapEntry = &TimerEntry{
				Kind:       TimerScheduleOccurrence,
				Owner:      entry.Owner,
				ID:         periodicKey(entry.Owner, entry.Name),
				Generation: entry.Generation,
				Deadline:   entry.NextRun,
				Data:       entry,
			}
			c.heap.Push(entry.heapEntry)
		} else if !entry.heapEntry.Deadline.Equal(entry.NextRun) {
			c.heap.Remove(entry.heapEntry)
			entry.heapEntry.Deadline = entry.NextRun
			c.heap.Push(entry.heapEntry)
		}
	}
}

func (c *periodicCoordinator) loop() {
	defer c.wg.Done()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		c.mu.Lock()
		ctx := c.ctx
		if !c.running || ctx == nil {
			c.mu.Unlock()
			return
		}

		c.syncHeapLocked()
		now := time.Now()
		due := c.collectDueLocked(now)
		earliest, hasEarliest := c.heap.PeekEarliest()
		c.mu.Unlock()

		for _, run := range due {
			c.startExecution(run)
		}
		if len(due) > 0 {
			continue
		}

		if !hasEarliest {
			select {
			case <-ctx.Done():
				return
			case <-c.wake:
				continue
			}
		}

		delay := time.Until(earliest.Deadline)
		if delay < 0 {
			delay = 0
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		case <-timer.C:
		}
	}
}

type periodicDueRun struct {
	owner      string
	name       string
	generation uint64
	task       TaskFunc
	options    PeriodicTaskOptions
	ctx        context.Context
}

func (c *periodicCoordinator) collectDueLocked(now time.Time) []periodicDueRun {
	dueEntries := c.heap.PopDue(now, 100)
	if len(dueEntries) == 0 {
		return nil
	}
	var due []periodicDueRun
	for _, item := range dueEntries {
		entry, ok := item.Data.(*periodicRegistration)
		if !ok || entry == nil {
			continue
		}
		if entry.Generation != item.Generation || entry.Running {
			continue
		}
		runCtx, runCancel := context.WithCancel(c.ctx)
		entry.Running = true
		entry.RunCancel = runCancel
		entry.heapEntry = nil // removed from heap while active
		due = append(due, periodicDueRun{
			owner:      entry.Owner,
			name:       entry.Name,
			generation: entry.Generation,
			task:       entry.Task,
			options:    entry.Options,
			ctx:        runCtx,
		})
	}
	return due
}

func (c *periodicCoordinator) startExecution(run periodicDueRun) {
	c.mu.Lock()
	submitter := c.submitter
	c.mu.Unlock()
	if submitter == nil {
		err := errors.New("periodic task worker submitter is not configured")
		c.finishExecution(run, err, false)
		return
	}

	taskID := fmt.Sprintf("periodic:%s:%s:%d:%d", run.owner, run.name, run.generation, time.Now().UnixNano())
	_, err := submitter.Submit(run.ctx, tasks.WorkSpec{
		ID: tasks.TaskID(taskID), Scope: tasks.ScopeIdentity{Owner: run.owner, Generation: run.generation}, QuotaOwner: tasks.OwnerID(run.options.Owner), Pool: "general", Class: tasks.PriorityMaintenance,
		ExecutionTimeout: run.options.Timeout,
		Handler: func(ctx context.Context) error {
			return runPeriodicTask(ctx, run.task, run.options)
		},
		OnComplete: func(result tasks.TaskResult) {
			var completionErr error
			if !result.IsSuccess() {
				completionErr = errors.New(result.Failure.Message)
			}
			c.finishExecution(run, completionErr, true)
		},
	})
	if err != nil {
		c.finishExecution(run, err, false)
	}
}

func (c *periodicCoordinator) finishExecution(run periodicDueRun, err error, executed bool) {
	finishedAt := time.Now().UTC()
	c.mu.Lock()
	key := periodicKey(run.owner, run.name)
	entry := c.entries[key]
	if entry != nil && entry.Generation == run.generation {
		if entry.RunCancel != nil {
			entry.RunCancel()
			entry.RunCancel = nil
		}
		entry.Running = false
		entry.LastRunAt = finishedAt
		if err != nil {
			entry.Failures++
			entry.LastError = err.Error()
		} else {
			entry.LastError = ""
		}

		if executed {
			entry.Runs++
			entry.Attempt++
			// Retry timing belongs to the coordinator; each Task is one attempt.
			retry := err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && entry.Attempt < entry.Options.MaxAttempts
			if retry {
				entry.NextRun = finishedAt.Add(entry.Options.RetryDelay)
			} else {
				entry.Attempt = 0
				entry.NextRun = finishedAt.Add(entry.Interval)
			}
		} else {
			// Submitter rejected or saturated: do NOT burn the task retry budget.
			// Set next run to retry delay or short backoff to let workers drain.
			backoff := entry.Options.RetryDelay
			if backoff <= 0 {
				backoff = 200 * time.Millisecond
			}
			entry.NextRun = finishedAt.Add(backoff)
		}

		// Re-insert into min-heap with updated deadline
		if entry.heapEntry == nil {
			entry.heapEntry = &TimerEntry{
				Kind:       TimerScheduleOccurrence,
				Owner:      entry.Owner,
				ID:         key,
				Generation: entry.Generation,
				Deadline:   entry.NextRun,
				Data:       entry,
			}
			c.heap.Push(entry.heapEntry)
		} else {
			c.heap.Remove(entry.heapEntry)
			entry.heapEntry.Deadline = entry.NextRun
			c.heap.Push(entry.heapEntry)
		}
	}
	c.mu.Unlock()

	if err != nil && !errors.Is(err, context.Canceled) {
		c.logger.Warn("periodic task execution error", zap.String("task", run.name), zap.Error(err))
	}
	c.notify()
}
