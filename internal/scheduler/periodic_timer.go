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
//
// Collapse mapping:
//   - one registration = one JobDefinition ("periodic:owner:name") whose
//     RetryPolicy derives from PeriodicTaskOptions (MaxAttempts,
//     RetryDelay -> InitialDelay). There is exactly one retry engine.
//   - each due tick submits one occurrence; single-flight per registration.
//   - Runs/Failures/LastRunAt/LastError are reconciled from the durable
//     occurrence by the timing loop, never by ad-hoc completion callbacks.
//   - terminal occurrences are pruned (1h window): periodic ticks must not
//     grow durable storage without bound.
type periodicCoordinator struct {
	mu      sync.Mutex
	entries map[string]*periodicRegistration
	jobDefs map[string]periodicIdentity
	heap    *IndexedHeap
	wake    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
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
	Owner      string
	Name       string
	Interval   time.Duration
	Options    PeriodicTaskOptions
	Task       TaskFunc
	NextRun    time.Time
	Generation uint64
	// Running reserves the registration from due collection while an
	// occurrence is submitted or in flight. OccurrenceID follows the
	// durable run; empty while idle.
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

// periodicHandlerType is the single JobManager handler for all periodic
// ticks; it dispatches to the currently registered TaskFunc.
const periodicHandlerType = "periodic.task"

// pruneWindow bounds durable growth of periodic occurrences.
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

// SetJobsManager wires the declarative owner of periodic execution.
func (c *periodicCoordinator) SetJobsManager(jobsMgr *jobs.Manager) {
	c.mu.Lock()
	c.jobsMgr = jobsMgr
	c.mu.Unlock()
	c.notify()
}

// SetClock injects the timing source (defaults to wall clock). Timer sleeps
// still use the system clock; the injected source drives due computation.
func (c *periodicCoordinator) SetClock(nowFn func() time.Time) {
	c.mu.Lock()
	c.nowFn = nowFn
	c.mu.Unlock()
	c.notify()
}

func (c *periodicCoordinator) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nowFn != nil {
		return c.nowFn()
	}
	return time.Now()
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
	type inflight struct {
		occurrenceID string
	}
	var pending []inflight
	for _, entry := range c.entries {
		if entry.Running && entry.OccurrenceID != "" {
			pending = append(pending, inflight{occurrenceID: entry.OccurrenceID})
		}
	}
	jobsMgr := c.jobsMgr
	c.entries = make(map[string]*periodicRegistration)
	c.heap = NewIndexedHeap()
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// In-flight occurrences are cancelled durably so the retry driver cannot
	// resurrect them after shutdown. Best effort: the engine may be gone.
	for _, in := range pending {
		if jobsMgr != nil {
			cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = jobsMgr.CancelOccurrence(cctx, in.occurrenceID, "periodic coordinator stopping")
			ccancel()
		}
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
		// Re-registration reuses the definition: refresh policy/timeout
		// through the revision CAS instead of leaking definitions.
		if uerr := jobsMgr.UpdateDefinition(context.Background(), def); uerr != nil {
			return fmt.Errorf("register periodic job: %w", uerr)
		}
	}

	c.mu.Lock()
	c.seq++
	generation := c.seq
	key := periodicKey(owner, name)

	if old := c.entries[key]; old != nil {
		if old.heapEntry != nil {
			c.heap.Remove(old.heapEntry)
			old.heapEntry = nil
		}
		// An in-flight occurrence keeps running under the previous
		// generation; cancel it so retries cannot resurrect stale work.
		if old.Running && old.OccurrenceID != "" {
			if jobsMgr != nil {
				go func(occID string) {
					cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer ccancel()
					_ = jobsMgr.CancelOccurrence(cctx, occID, "periodic task re-registered")
				}(old.OccurrenceID)
			}
		}
	}

	now := time.Now()
	if c.nowFn != nil {
		now = c.nowFn()
	}
	nextRun := now.Add(interval)
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
	c.jobDefs[defID] = periodicIdentity{owner: owner, name: name}
	c.mu.Unlock()

	c.notify()
	return nil
}

// runTaskFunc executes the currently registered function for one job attempt.
// The attempt's timeout is enforced by TaskEngine (definition Timeout); panics
// are isolated there and surface as failed attempts under the job retry policy.
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

func (c *periodicCoordinator) forgetDef(defID string) {
	c.mu.Lock()
	delete(c.jobDefs, defID)
	c.mu.Unlock()
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
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
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
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	type inflight struct {
		occurrenceID string
	}
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
			cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = jobsMgr.CancelOccurrence(cctx, in.occurrenceID, "periodic tasks unregistered by owner")
			ccancel()
		}
	}
	// Owner-wide cancellation also fences the scope so late retries cannot
	// resurrect the owner's work.
	if jobsMgr != nil && count > 0 {
		jobsMgr.CancelByOwner(cleanOwner)
	}
	if count > 0 {
		c.notify()
	}
	return count
}

// inflightCount reports registrations with a submitted occurrence whose
// settlement the timing loop must observe promptly.
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
		// While occurrences are in flight, their settlement must be observed
		// promptly: cap the sleep so reconciliation cannot starve behind a
		// distant heap deadline.
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
	owner      string
	name       string
	generation uint64
	interval   time.Duration
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
		entry.Running = true
		entry.heapEntry = nil // removed from heap while in flight
		due = append(due, periodicDueRun{
			owner:      entry.Owner,
			name:       entry.Name,
			generation: entry.Generation,
			interval:   entry.Interval,
			ctx:        c.ctx,
		})
	}
	return due
}

// submitExecution hands one due tick to JobManager as a single occurrence.
// Admission rejection is timing backpressure, not an attempt: the tick is
// re-armed with a short backoff without burning retry budget.
func (c *periodicCoordinator) submitExecution(jobsMgr *jobs.Manager, run periodicDueRun) {
	if jobsMgr == nil {
		c.finishSubmission(run)
		return
	}
	defID := periodicDefinitionID(run.owner, run.name)
	slot := time.Now().UTC().UnixNano()
	occurrenceKey := fmt.Sprintf("periodic:%s:%d", defID, slot)
	_, occurrenceID, err := jobsMgr.SubmitOccurrence(run.ctx, defID, occurrenceKey)
	if err != nil {
		c.logger.Warn("periodic occurrence admission rejected", zap.String("task", run.name), zap.Error(err))
		c.finishSubmission(run)
		return
	}
	c.mu.Lock()
	key := periodicKey(run.owner, run.name)
	if entry := c.entries[key]; entry != nil && entry.Generation == run.generation && entry.Running {
		entry.OccurrenceID = occurrenceID
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	// Registration changed under the submission: cancel the stray occurrence.
	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	_ = jobsMgr.CancelOccurrence(cctx, occurrenceID, "periodic registration changed during submit")
}

// finishSubmission re-arms a tick whose occurrence was never admitted.
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
	}
	c.mu.Unlock()
	c.notify()
}

// reconcileInflight settles registrations whose occurrences reached a durable
// terminal state. Retries between attempts are owned by JobManager; the next
// tick is armed only when the occurrence is final.
func (c *periodicCoordinator) reconcileInflight(jobsMgr *jobs.Manager) {
	if jobsMgr == nil {
		return
	}
	c.mu.Lock()
	var inflight []pendingReconcile
	for key, entry := range c.entries {
		if entry.Running && entry.OccurrenceID != "" {
			inflight = append(inflight, pendingReconcile{key: key, occurrenceID: entry.OccurrenceID, defID: periodicDefinitionID(entry.Owner, entry.Name), interval: entry.Interval})
		}
	}
	c.mu.Unlock()
	if len(inflight) == 0 {
		return
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
		return // attempts still running or retrying under JobManager.
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
		entry.NextRun = finished.Add(in.interval)
	}
	c.mu.Unlock()
	// Bound durable growth: periodic ticks settle constantly; keep an hour.
	pctx, pcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pcancel()
	_, _ = jobsMgr.PruneOccurrences(pctx, in.defID, finished.Add(-periodicPruneWindow), 500)
	c.notify()
}
