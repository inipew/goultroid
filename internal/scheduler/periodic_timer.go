package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

// periodicCoordinator multiplexes all runtime periodic tasks onto an indexed
// min-heap timer. It decides WHEN work is due; execution, retries, and
// durability belong to JobManager/TaskEngine (Phase E collapse).
type periodicCoordinator struct {
	mu      sync.Mutex
	entries map[string]*periodicRegistration
	jobDefs map[string]periodicIdentity
	heap    *IndexedHeap
	wake    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	done    chan struct{}
	running bool
	seq     uint64
	logger  *zap.Logger
	jobsMgr *jobs.Manager
	nowFn   func() time.Time
}

type periodicIdentity struct {
	owner string
	name  string
}

type periodicRegistration struct {
	Owner    string
	Name     string
	Interval time.Duration
	Options  PeriodicTaskOptions
	Task     TaskFunc
	// ScheduledFor is the immutable logical identity of the current tick. It
	// remains unchanged while admission is retried/backed off. NextRun is only
	// the next timing-loop wake deadline and may move temporarily on backpressure.
	ScheduledFor time.Time
	NextRun      time.Time
	Generation   uint64

	Running      bool
	OccurrenceID string
	Runs         int64
	Failures     int64
	LastRunAt    time.Time
	LastError    string
	heapEntry    *TimerEntry
}

func periodicKey(owner, name string) string {
	if owner == "" || owner == "runtime" {
		return name
	}
	return owner + "/" + name
}

func periodicDefinitionID(owner, name string) string {
	return fmt.Sprintf("periodic:%s:%s", owner, name)
}

const periodicHandlerType = "periodic.task"
const periodicPruneWindow = time.Hour

func newPeriodicCoordinator(logger *zap.Logger) *periodicCoordinator {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &periodicCoordinator{
		entries: make(map[string]*periodicRegistration),
		jobDefs: make(map[string]periodicIdentity),
		heap:    NewIndexedHeap(),
		wake:    make(chan struct{}, 1),
		logger:  logger,
	}
}

func (c *periodicCoordinator) SetJobsManager(jobsMgr *jobs.Manager) {
	c.mu.Lock()
	c.jobsMgr = jobsMgr
	c.mu.Unlock()
	c.notify()
}

func (c *periodicCoordinator) SetClock(nowFn func() time.Time) {
	c.mu.Lock()
	c.nowFn = nowFn
	c.mu.Unlock()
	c.notify()
}

func (c *periodicCoordinator) notify() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *periodicCoordinator) operationContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	c.mu.Lock()
	base := c.ctx
	c.mu.Unlock()
	if base == nil {
		base = context.Background()
	}
	return context.WithTimeout(base, timeout)
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
	c.done = make(chan struct{})
	c.wg.Add(1)
	done := c.done
	c.mu.Unlock()
	go func() {
		c.wg.Wait()
		close(done)
	}()
	go c.loop()
	return nil
}

func (c *periodicCoordinator) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if !c.running {
		done := c.done
		c.mu.Unlock()
		if done == nil {
			return nil
		}
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.running = false
	cancel := c.cancel
	type inflight struct{ occurrenceID string }
	var pending []inflight
	for _, entry := range c.entries {
		if entry.Running && entry.OccurrenceID != "" {
			pending = append(pending, inflight{occurrenceID: entry.OccurrenceID})
		}
	}
	jobsMgr := c.jobsMgr
	done := c.done
	c.entries = make(map[string]*periodicRegistration)
	c.heap = NewIndexedHeap()
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, in := range pending {
		if jobsMgr == nil || ctx.Err() != nil {
			break
		}
		_ = jobsMgr.CancelOccurrence(ctx, in.occurrenceID, "periodic coordinator stopping")
	}
	c.notify()

	if done == nil {
		return nil
	}
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
	jobsMgr := c.jobsMgr
	c.mu.Unlock()
	if jobsMgr == nil {
		return errors.New("periodic execution owner is not configured")
	}

	defID := periodicDefinitionID(owner, name)
	def := jobs.JobDefinition{
		ID:          defID,
		ScopeOwner:  owner,
		QuotaOwner:  owner,
		HandlerType: periodicHandlerType,
		Pool:        "general",
		Class:       string(tasks.PriorityMaintenance),
		Timeout:     options.Timeout,
		RetryPolicy: jobs.JobRetryPolicy{
			MaxAttempts:       options.MaxAttempts,
			InitialDelay:      options.RetryDelay,
			MaxDelay:          interval,
			BackoffMultiplier: 2,
		},
		Enabled: true,
	}
	if err := jobsMgr.Register(def); err != nil {
		uctx, ucancel := c.operationContext(10 * time.Second)
		uerr := jobsMgr.UpdateDefinition(uctx, def)
		ucancel()
		if uerr != nil {
			return fmt.Errorf("register periodic job: %w", uerr)
		}
	}

	c.mu.Lock()
	c.seq++
	generation := c.seq
	key := periodicKey(owner, name)
	var oldOccurrenceID string
	if old := c.entries[key]; old != nil {
		if old.heapEntry != nil {
			c.heap.Remove(old.heapEntry)
			old.heapEntry = nil
		}
		if old.Running && old.OccurrenceID != "" {
			oldOccurrenceID = old.OccurrenceID
		}
	}

	now := time.Now()
	if c.nowFn != nil {
		now = c.nowFn()
	}
	nextRun := now.Add(interval)
	reg := &periodicRegistration{
		Owner: owner, Name: name, Interval: interval, Options: options, Task: task,
		ScheduledFor: nextRun, NextRun: nextRun, Generation: generation,
	}
	timerEntry := &TimerEntry{
		Kind: TimerScheduleOccurrence, Owner: owner, ID: key,
		Generation: generation, Deadline: nextRun, Data: reg,
	}
	reg.heapEntry = timerEntry
	c.heap.Push(timerEntry)
	c.entries[key] = reg
	c.jobDefs[defID] = periodicIdentity{owner: owner, name: name}
	c.mu.Unlock()

	// Re-registration cancellation is bounded and synchronous. Repeated
	// registrations cannot accumulate detached cancellation goroutines.
	if oldOccurrenceID != "" {
		cctx, ccancel := c.operationContext(5 * time.Second)
		_ = jobsMgr.CancelOccurrence(cctx, oldOccurrenceID, "periodic task re-registered")
		ccancel()
	}
	c.notify()
	return nil
}

func (c *periodicCoordinator) runTaskFunc(ctx context.Context, defID string) error {
	c.mu.Lock()
	id, ok := c.jobDefs[defID]
	if !ok {
		c.mu.Unlock()
		return errors.New("periodic job definition is not registered")
	}
	reg := c.entries[periodicKey(id.owner, id.name)]
	var fn TaskFunc
	if reg != nil {
		fn = reg.Task
	}
	c.mu.Unlock()
	if fn == nil {
		return errors.New("periodic task is not registered")
	}
	return fn(ctx)
}

func (c *periodicCoordinator) Unregister(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("task name cannot be empty")
	}
	c.mu.Lock()
	entry, ok := c.entries[name]
	targetKey := name
	if !ok {
		for k, e := range c.entries {
			if e.Name == name {
				entry = e
				targetKey = k
				ok = true
				break
			}
		}
	}
	var occurrenceID, defID string
	if ok {
		if entry.heapEntry != nil {
			c.heap.Remove(entry.heapEntry)
			entry.heapEntry = nil
		}
		occurrenceID = entry.OccurrenceID
		defID = periodicDefinitionID(entry.Owner, entry.Name)
		delete(c.entries, targetKey)
		delete(c.jobDefs, defID)
	}
	jobsMgr := c.jobsMgr
	c.mu.Unlock()
	if !ok {
		return errors.New("task not found")
	}
	if occurrenceID != "" && jobsMgr != nil {
		cctx, ccancel := c.operationContext(5 * time.Second)
		defer ccancel()
		_ = jobsMgr.CancelOccurrence(cctx, occurrenceID, "periodic task unregistered")
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
	var occurrenceID string
	if ok {
		if entry.heapEntry != nil {
			c.heap.Remove(entry.heapEntry)
			entry.heapEntry = nil
		}
		occurrenceID = entry.OccurrenceID
		delete(c.entries, key)
		delete(c.jobDefs, periodicDefinitionID(entry.Owner, entry.Name))
	}
	jobsMgr := c.jobsMgr
	c.mu.Unlock()
	if !ok {
		return errors.New("task not found")
	}
	if occurrenceID != "" && jobsMgr != nil {
		cctx, ccancel := c.operationContext(5 * time.Second)
		defer ccancel()
		_ = jobsMgr.CancelOccurrence(cctx, occurrenceID, "periodic task unregistered")
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
	type inflight struct{ occurrenceID string }
	var pending []inflight
	count := 0
	for key, entry := range c.entries {
		if entry.Owner == cleanOwner || entry.Owner == "plugin:"+cleanOwner {
			if entry.heapEntry != nil {
				c.heap.Remove(entry.heapEntry)
				entry.heapEntry = nil
			}
			if entry.Running && entry.OccurrenceID != "" {
				pending = append(pending, inflight{occurrenceID: entry.OccurrenceID})
			}
			delete(c.jobDefs, periodicDefinitionID(entry.Owner, entry.Name))
			delete(c.entries, key)
			count++
		}
	}
	jobsMgr := c.jobsMgr
	c.mu.Unlock()
	for _, in := range pending {
		if jobsMgr != nil {
			cctx, ccancel := c.operationContext(5 * time.Second)
			_ = jobsMgr.CancelOccurrence(cctx, in.occurrenceID, "periodic tasks unregistered by owner")
			ccancel()
		}
	}
	if jobsMgr != nil && count > 0 {
		jobsMgr.CancelByOwner(cleanOwner)
	}
	if count > 0 {
		c.notify()
	}
	return count
}

func (c *periodicCoordinator) inflightCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, entry := range c.entries {
		if entry.Running && entry.OccurrenceID != "" {
			n++
		}
	}
	return n
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

func (c *periodicCoordinator) syncHeapLocked() {
	for _, entry := range c.entries {
		if entry.Running {
			continue
		}
		if entry.heapEntry == nil {
			entry.heapEntry = &TimerEntry{
				Kind: TimerScheduleOccurrence, Owner: entry.Owner,
				ID: periodicKey(entry.Owner, entry.Name), Generation: entry.Generation,
				Deadline: entry.NextRun, Data: entry,
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
		jobsMgr := c.jobsMgr
		c.syncHeapLocked()
		var now time.Time
		if c.nowFn != nil {
			now = c.nowFn()
		} else {
			now = time.Now()
		}
		due := c.collectDueLocked(now)
		earliest, hasEarliest := c.heap.PeekEarliest()
		c.mu.Unlock()

		for _, run := range due {
			c.submitExecution(jobsMgr, run)
		}
		c.reconcileInflight(jobsMgr)
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
		if delay > time.Second && c.inflightCount() > 0 {
			delay = time.Second
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
	owner        string
	name         string
	generation   uint64
	interval     time.Duration
	scheduledFor time.Time
	ctx          context.Context
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
		entry.Running = true
		entry.heapEntry = nil
		scheduledFor := entry.ScheduledFor
		if scheduledFor.IsZero() {
			scheduledFor = entry.NextRun
			entry.ScheduledFor = scheduledFor
		}
		due = append(due, periodicDueRun{
			owner: entry.Owner, name: entry.Name, generation: entry.Generation,
			interval: entry.Interval, scheduledFor: scheduledFor, ctx: c.ctx,
		})
	}
	return due
}

// submitExecution hands one logical due tick to JobManager. occurrenceKey is
// derived from definition + registration generation + immutable scheduled slot,
// never from the wall clock at submit time.
func (c *periodicCoordinator) submitExecution(jobsMgr *jobs.Manager, run periodicDueRun) {
	if jobsMgr == nil {
		c.finishSubmission(run)
		return
	}
	defID := periodicDefinitionID(run.owner, run.name)
	occurrenceKey := fmt.Sprintf("periodic:%s:%d:%d", defID, run.generation, run.scheduledFor.UTC().UnixNano())
	_, occurrenceID, err := jobsMgr.SubmitOccurrence(run.ctx, defID, occurrenceKey)
	if err == nil {
		c.bindOccurrence(jobsMgr, run, occurrenceID)
		return
	}

	// Materialization happens before TaskEngine admission. An admission error may
	// therefore already have a canonical occurrence/attempt that recovery owns.
	// Resolve it by the same stable key instead of minting a second logical tick.
	stateCtx, cancel := c.operationContext(10 * time.Second)
	occ, lookupErr := jobsMgr.OccurrenceByKey(stateCtx, occurrenceKey)
	cancel()
	if lookupErr == nil && occ != nil {
		c.logger.Warn("periodic admission rejected after materialization; tracking canonical occurrence",
			zap.String("task", run.name), zap.String("occurrence_id", occ.ID), zap.Error(err))
		c.bindOccurrence(jobsMgr, run, occ.ID)
		return
	}

	c.logger.Warn("periodic occurrence admission rejected", zap.String("task", run.name), zap.Error(err))
	c.finishSubmission(run)
}

// bindOccurrence attaches a canonical durable occurrence to the current
// registration. If the registration changed during submission, close the stray
// occurrence synchronously under a bounded context.
func (c *periodicCoordinator) bindOccurrence(jobsMgr *jobs.Manager, run periodicDueRun, occurrenceID string) {
	c.mu.Lock()
	key := periodicKey(run.owner, run.name)
	if entry := c.entries[key]; entry != nil && entry.Generation == run.generation && entry.Running {
		entry.OccurrenceID = occurrenceID
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	if occurrenceID == "" || jobsMgr == nil {
		return
	}
	cctx, ccancel := c.operationContext(5 * time.Second)
	_ = jobsMgr.CancelOccurrence(cctx, occurrenceID, "periodic registration changed during submit")
	ccancel()
}

// finishSubmission backs off a tick whose occurrence was not materialized. It
// changes only the wake deadline; ScheduledFor stays fixed so the next attempt
// uses exactly the same idempotency key.
func (c *periodicCoordinator) finishSubmission(run periodicDueRun) {
	c.mu.Lock()
	key := periodicKey(run.owner, run.name)
	if entry := c.entries[key]; entry != nil && entry.Generation == run.generation && entry.Running && entry.OccurrenceID == "" {
		entry.Running = false
		var now time.Time
		if c.nowFn != nil {
			now = c.nowFn()
		} else {
			now = time.Now().UTC()
		}
		entry.NextRun = now.Add(200 * time.Millisecond)
		if entry.ScheduledFor.IsZero() {
			entry.ScheduledFor = run.scheduledFor
		}
	}
	c.mu.Unlock()
	c.notify()
}

func (c *periodicCoordinator) reconcileInflight(jobsMgr *jobs.Manager) {
	if jobsMgr == nil {
		return
	}
	c.mu.Lock()
	var inflight []pendingReconcile
	for key, entry := range c.entries {
		if entry.Running && entry.OccurrenceID != "" {
			inflight = append(inflight, pendingReconcile{
				key: key, occurrenceID: entry.OccurrenceID,
				defID: periodicDefinitionID(entry.Owner, entry.Name), interval: entry.Interval,
			})
		}
	}
	c.mu.Unlock()
	for _, in := range inflight {
		c.reconcileOne(jobsMgr, in)
	}
}

type pendingReconcile struct {
	key          string
	occurrenceID string
	defID        string
	interval     time.Duration
}

func (c *periodicCoordinator) reconcileOne(jobsMgr *jobs.Manager, in pendingReconcile) {
	ctx, cancel := c.operationContext(10 * time.Second)
	defer cancel()
	occ, err := jobsMgr.GetOccurrence(ctx, in.occurrenceID)
	if err != nil || occ == nil {
		return
	}
	var finished time.Time
	var failed bool
	var errText string
	switch occ.State {
	case jobs.OccurrenceCompleted:
	case jobs.OccurrenceFailed, jobs.OccurrenceCancelled:
		failed = true
		if latest, lerr := jobsMgr.LatestAttempt(ctx, in.occurrenceID); lerr == nil && latest != nil {
			errText = latest.Error
			if !latest.FinishedAt.IsZero() {
				finished = latest.FinishedAt
			}
		}
	default:
		return
	}
	if finished.IsZero() {
		if c.nowFn != nil {
			finished = c.nowFn()
		} else {
			finished = time.Now().UTC()
		}
	}
	c.mu.Lock()
	if entry := c.entries[in.key]; entry != nil && entry.Running && entry.OccurrenceID == in.occurrenceID {
		entry.Running = false
		entry.OccurrenceID = ""
		entry.LastRunAt = finished
		if failed {
			entry.Failures++
			entry.LastError = errText
		} else {
			entry.LastError = ""
		}
		entry.Runs++
		nextSlot := finished.Add(in.interval)
		entry.ScheduledFor = nextSlot
		entry.NextRun = nextSlot
	}
	c.mu.Unlock()

	pctx, pcancel := c.operationContext(10 * time.Second)
	_, _ = jobsMgr.PruneOccurrences(pctx, in.defID, finished.Add(-periodicPruneWindow), 500)
	pcancel()
	c.notify()
}
