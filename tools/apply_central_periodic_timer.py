from pathlib import Path
import re

engine_path = Path('internal/scheduler/engine.go')
s = engine_path.read_text()

s = s.replace('''\ttasks    map[string]context.CancelFunc\n\ttaskMeta map[string]*periodicTaskMeta\n\ttasksMu  sync.RWMutex\n''', '''\tperiodic *periodicCoordinator\n''')

s = re.sub(r'''\ntype periodicTaskMeta struct \{.*?\n\}\n''', '\n', s, count=1, flags=re.S)

s = s.replace('''\t\ttasks:          make(map[string]context.CancelFunc),\n\t\ttaskMeta:       make(map[string]*periodicTaskMeta),\n\t\tmaxConcurrency: defaultConcurrency,\n''', '''\t\tmaxConcurrency: defaultConcurrency,\n''')

old_return = '''\treturn &Engine{\n\t\tdb:             db,\n\t\tsvcFunc:        svcFunc,\n\t\trouter:         router,\n\t\tperms:          perms,\n\t\texecutor:       core.NewCommandExecutor(logger, nil, 30*time.Second),\n\t\tlogger:         logger,\n\t\tmaxConcurrency: defaultConcurrency,\n\t\tmisfirePolicy:  MisfireRunOnce,\n\t\twakeChan:       make(chan struct{}, 1),\n\t}\n'''
new_return = '''\tengine := &Engine{\n\t\tdb:             db,\n\t\tsvcFunc:        svcFunc,\n\t\trouter:         router,\n\t\tperms:          perms,\n\t\texecutor:       core.NewCommandExecutor(logger, nil, 30*time.Second),\n\t\tlogger:         logger,\n\t\tmaxConcurrency: defaultConcurrency,\n\t\tmisfirePolicy:  MisfireRunOnce,\n\t\twakeChan:       make(chan struct{}, 1),\n\t}\n\tengine.periodic = newPeriodicCoordinator(logger)\n\treturn engine\n'''
if old_return not in s:
    raise SystemExit('NewEngine anchor not found')
s = s.replace(old_return, new_return, 1)

start_anchor = '''\te.ctx, e.cancel = context.WithCancel(parentCtx)\n\te.claimMu.Lock()\n'''
start_replace = '''\te.ctx, e.cancel = context.WithCancel(parentCtx)\n\tif e.periodic != nil {\n\t\tif err := e.periodic.Start(e.ctx); err != nil {\n\t\t\te.cancel()\n\t\t\treturn fmt.Errorf("start periodic coordinator: %w", err)\n\t\t}\n\t}\n\te.claimMu.Lock()\n'''
if start_anchor not in s:
    raise SystemExit('Start anchor not found')
s = s.replace(start_anchor, start_replace, 1)

stop_block = '''\n\te.tasksMu.Lock()\n\tfor _, cancelTask := range e.tasks {\n\t\tcancelTask()\n\t}\n\te.tasks = make(map[string]context.CancelFunc)\n\te.taskMeta = make(map[string]*periodicTaskMeta)\n\te.tasksMu.Unlock()\n\n'''
if stop_block not in s:
    raise SystemExit('Stop periodic block anchor not found')
s = s.replace(stop_block, '''\n\tif e.periodic != nil {\n\t\tif err := e.periodic.Stop(ctx); err != nil {\n\t\t\treturn fmt.Errorf("stop periodic coordinator: %w", err)\n\t\t}\n\t}\n\n''', 1)

pattern = re.compile(r'''func \(e \*Engine\) RegisterPeriodicTaskWithOptions\(name string, interval time\.Duration, options PeriodicTaskOptions, task TaskFunc\) error \{.*?\n\}\n\nfunc runPeriodicTask''', re.S)
replacement = '''func (e *Engine) RegisterPeriodicTaskWithOptions(name string, interval time.Duration, options PeriodicTaskOptions, task TaskFunc) error {\n\tif e.periodic == nil {\n\t\treturn errors.New("periodic coordinator is not configured")\n\t}\n\treturn e.periodic.Register(name, interval, options, task)\n}\n\nfunc runPeriodicTask'''
s, n = pattern.subn(replacement, s, count=1)
if n != 1:
    raise SystemExit(f'RegisterPeriodicTaskWithOptions replacement count={n}')

pattern = re.compile(r'''func \(e \*Engine\) UnregisterPeriodicTask\(name string\) error \{.*?\n\}\n\n// UnregisterPeriodicTasksByOwner''', re.S)
replacement = '''func (e *Engine) UnregisterPeriodicTask(name string) error {\n\tif e.periodic == nil {\n\t\treturn errors.New("periodic coordinator is not configured")\n\t}\n\treturn e.periodic.Unregister(name)\n}\n\n// UnregisterPeriodicTasksByOwner'''
s, n = pattern.subn(replacement, s, count=1)
if n != 1:
    raise SystemExit(f'UnregisterPeriodicTask replacement count={n}')

pattern = re.compile(r'''func \(e \*Engine\) UnregisterPeriodicTasksByOwner\(owner string\) int \{.*?\n\}\n\n// PeriodicTaskSnapshots''', re.S)
replacement = '''func (e *Engine) UnregisterPeriodicTasksByOwner(owner string) int {\n\tif e.periodic == nil {\n\t\treturn 0\n\t}\n\treturn e.periodic.UnregisterByOwner(owner)\n}\n\n// PeriodicTaskSnapshots'''
s, n = pattern.subn(replacement, s, count=1)
if n != 1:
    raise SystemExit(f'UnregisterPeriodicTasksByOwner replacement count={n}')

pattern = re.compile(r'''func \(e \*Engine\) PeriodicTaskSnapshots\(\) \[\]PeriodicTaskSnapshot \{.*?\n\}\n\nfunc \(e \*Engine\) ScheduleOnce''', re.S)
replacement = '''func (e *Engine) PeriodicTaskSnapshots() []PeriodicTaskSnapshot {\n\tif e.periodic == nil {\n\t\treturn nil\n\t}\n\treturn e.periodic.Snapshots()\n}\n\nfunc (e *Engine) ScheduleOnce'''
s, n = pattern.subn(replacement, s, count=1)
if n != 1:
    raise SystemExit(f'PeriodicTaskSnapshots replacement count={n}')

engine_path.write_text(s)

periodic = r'''package scheduler

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
    Owner       string
    Name        string
    Interval    time.Duration
    Options     PeriodicTaskOptions
    Task        TaskFunc
    NextRun     time.Time
    Generation  uint64
    Running     bool
    RunCancel   context.CancelFunc
    Runs        int64
    Failures    int64
    LastRunAt   time.Time
    LastError   string
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
'''
Path('internal/scheduler/periodic_timer.go').write_text(periodic)

periodic_test = r'''package scheduler

import (
    "context"
    "sync/atomic"
    "testing"
    "time"

    "go.uber.org/zap"
)

func TestPeriodicCoordinatorRunsMultipleTasksFromSharedLoop(t *testing.T) {
    c := newPeriodicCoordinator(zap.NewNop())
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    if err := c.Start(ctx); err != nil {
        t.Fatal(err)
    }
    defer func() {
        stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
        defer stopCancel()
        if err := c.Stop(stopCtx); err != nil {
            t.Fatal(err)
        }
    }()

    var a, b atomic.Int32
    if err := c.Register("a", 10*time.Millisecond, PeriodicTaskOptions{Owner: "x"}, func(context.Context) error {
        a.Add(1)
        return nil
    }); err != nil {
        t.Fatal(err)
    }
    if err := c.Register("b", 15*time.Millisecond, PeriodicTaskOptions{Owner: "x"}, func(context.Context) error {
        b.Add(1)
        return nil
    }); err != nil {
        t.Fatal(err)
    }

    deadline := time.After(time.Second)
    for a.Load() < 2 || b.Load() < 2 {
        select {
        case <-deadline:
            t.Fatalf("periodic executions did not advance: a=%d b=%d", a.Load(), b.Load())
        default:
            time.Sleep(5 * time.Millisecond)
        }
    }
}

func TestPeriodicCoordinatorPreventsSameTaskOverlap(t *testing.T) {
    c := newPeriodicCoordinator(zap.NewNop())
    if err := c.Start(context.Background()); err != nil {
        t.Fatal(err)
    }
    defer func() {
        stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
        defer cancel()
        if err := c.Stop(stopCtx); err != nil {
            t.Fatal(err)
        }
    }()

    var running, maxRunning atomic.Int32
    started := make(chan struct{}, 1)
    release := make(chan struct{})
    if err := c.Register("slow", 5*time.Millisecond, PeriodicTaskOptions{}, func(context.Context) error {
        n := running.Add(1)
        for {
            old := maxRunning.Load()
            if n <= old || maxRunning.CompareAndSwap(old, n) {
                break
            }
        }
        select { case started <- struct{}{}: default: }
        <-release
        running.Add(-1)
        return nil
    }); err != nil {
        t.Fatal(err)
    }

    select {
    case <-started:
    case <-time.After(time.Second):
        t.Fatal("task did not start")
    }
    time.Sleep(30 * time.Millisecond)
    if got := maxRunning.Load(); got != 1 {
        t.Fatalf("same task overlapped: max running=%d", got)
    }
    close(release)
}

func TestPeriodicCoordinatorUnregisterCancelsActiveRun(t *testing.T) {
    c := newPeriodicCoordinator(zap.NewNop())
    if err := c.Start(context.Background()); err != nil {
        t.Fatal(err)
    }
    defer func() { _ = c.Stop(context.Background()) }()

    started := make(chan struct{})
    stopped := make(chan struct{})
    if err := c.Register("cancel", time.Millisecond, PeriodicTaskOptions{}, func(ctx context.Context) error {
        close(started)
        <-ctx.Done()
        close(stopped)
        return ctx.Err()
    }); err != nil {
        t.Fatal(err)
    }
    select {
    case <-started:
    case <-time.After(time.Second):
        t.Fatal("task did not start")
    }
    if err := c.Unregister("cancel"); err != nil {
        t.Fatal(err)
    }
    select {
    case <-stopped:
    case <-time.After(time.Second):
        t.Fatal("active run was not cancelled")
    }
}
'''
Path('internal/scheduler/periodic_timer_test.go').write_text(periodic_test)
