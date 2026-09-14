package scheduler

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

type periodicRegistration struct {
	Owner      string
	Name       string
	Interval   time.Duration
	Options    PeriodicTaskOptions
	Task       TaskFunc
	NextRun    time.Time
	Generation uint64
	Running    bool
	RunCancel  context.CancelFunc
	Runs       int64
	Failures   int64
	LastRunAt  time.Time
	LastError  string
}

// periodicCoordinator multiplexes all runtime periodic tasks onto a single
// deadline timer. Registered tasks do not own permanent goroutines or tickers;
// goroutines exist only while concrete executions are running.
type periodicCoordinator struct {
	mu      sync.Mutex
	entries map[string]*periodicRegistration
	wake    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running bool
	seq     uint64
	logger  *zap.Logger
}

func newPeriodicCoordinator(logger *zap.Logger) *periodicCoordinator {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &periodicCoordinator{
		entries: make(map[string]*periodicRegistration),
		wake:    make(chan struct{}, 1),
		logger:  logger,
	}
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
	var oldCancel context.CancelFunc
	if old := c.entries[name]; old != nil {
		oldCancel = old.RunCancel
	}
	c.entries[name] = &periodicRegistration{
		Owner: owner, Name: name, Interval: interval, Options: options, Task: task,
		NextRun: time.Now().Add(interval), Generation: generation,
	}
	c.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}
	c.notify()
	return nil
}

func (c *periodicCoordinator) Unregister(name string) error {
	c.mu.Lock()
	entry, ok := c.entries[name]
	if ok {
		delete(c.entries, name)
	}
	c.mu.Unlock()
	if !ok {
		return errors.New("task not found")
	}
	if entry.RunCancel != nil {
		entry.RunCancel()
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
	for name, entry := range c.entries {
		if entry.Owner == cleanOwner || entry.Owner == "plugin:"+cleanOwner {
			if entry.RunCancel != nil {
				cancels = append(cancels, entry.RunCancel)
			}
			delete(c.entries, name)
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
			Owner: entry.Owner, Name: entry.Name, Runs: entry.Runs,
			Failures: entry.Failures, LastRunAt: entry.LastRunAt, LastError: entry.LastError,
		})
	}
	c.mu.Unlock()
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Name < snapshots[j].Name })
	return snapshots
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
		now := time.Now()
		next, found := c.nextDeadlineLocked()
		due := c.collectDueLocked(now)
		if len(due) > 0 {
			next, found = c.nextDeadlineLocked()
		}
		c.mu.Unlock()

		for _, run := range due {
			c.startExecution(run)
		}
		if len(due) > 0 {
			continue
		}

		if !found {
			select {
			case <-ctx.Done():
				return
			case <-c.wake:
				continue
			}
		}

		delay := time.Until(next)
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
	name       string
	generation uint64
	task       TaskFunc
	options    PeriodicTaskOptions
	ctx        context.Context
}

func (c *periodicCoordinator) nextDeadlineLocked() (time.Time, bool) {
	var next time.Time
	found := false
	for _, entry := range c.entries {
		if entry.Running {
			continue
		}
		if !found || entry.NextRun.Before(next) {
			next = entry.NextRun
			found = true
		}
	}
	return next, found
}

func (c *periodicCoordinator) collectDueLocked(now time.Time) []periodicDueRun {
	var due []periodicDueRun
	for _, entry := range c.entries {
		if entry.Running || entry.NextRun.After(now) {
			continue
		}
		runCtx, runCancel := context.WithCancel(c.ctx)
		entry.Running = true
		entry.RunCancel = runCancel
		entry.NextRun = now.Add(entry.Interval)
		due = append(due, periodicDueRun{
			name: entry.Name, generation: entry.Generation, task: entry.Task,
			options: entry.Options, ctx: runCtx,
		})
	}
	return due
}

func (c *periodicCoordinator) startExecution(run periodicDueRun) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		err := runPeriodicTask(run.ctx, run.task, run.options)
		finishedAt := time.Now().UTC()

		c.mu.Lock()
		entry := c.entries[run.name]
		if entry != nil && entry.Generation == run.generation {
			if entry.RunCancel != nil {
				entry.RunCancel()
				entry.RunCancel = nil
			}
			entry.Running = false
			entry.Runs++
			entry.LastRunAt = finishedAt
			if err != nil {
				entry.Failures++
				entry.LastError = err.Error()
			} else {
				entry.LastError = ""
			}
			// Long-running executions do not catch up missed ticks. This keeps
			// one task from monopolizing the coordinator after a stall.
			if !entry.NextRun.After(finishedAt) {
				entry.NextRun = finishedAt.Add(entry.Interval)
			}
		}
		c.mu.Unlock()

		if err != nil && !errors.Is(err, context.Canceled) {
			c.logger.Warn("periodic task execution error", zap.String("task", run.name), zap.Error(err))
		}
		c.notify()
	}()
}
