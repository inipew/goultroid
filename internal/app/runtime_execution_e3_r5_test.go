package app

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/scheduler"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

type runtimeExecutionR5MessageService struct{}

func (*runtimeExecutionR5MessageService) SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error) {
	return nil, nil
}
func (*runtimeExecutionR5MessageService) SendMessageWithMarkup(context.Context, tg.InputPeerClass, string, tg.ReplyMarkupClass) (*tg.Message, error) {
	return nil, nil
}
func (*runtimeExecutionR5MessageService) EditMessage(context.Context, tg.InputPeerClass, int, string) error {
	return nil
}
func (*runtimeExecutionR5MessageService) EditMessageMarkup(context.Context, tg.InputPeerClass, int, string, tg.ReplyMarkupClass) error {
	return nil
}
func (*runtimeExecutionR5MessageService) EditMessageMarkupOnly(context.Context, tg.InputPeerClass, int, tg.ReplyMarkupClass) error {
	return nil
}
func (*runtimeExecutionR5MessageService) DeleteMessage(context.Context, tg.InputPeerClass, []int) error {
	return nil
}
func (*runtimeExecutionR5MessageService) React(context.Context, tg.InputPeerClass, int, string) error {
	return nil
}
func (*runtimeExecutionR5MessageService) GetMessage(context.Context, tg.InputPeerClass, int) (*tg.Message, error) {
	return nil, nil
}
func (*runtimeExecutionR5MessageService) PinMessage(context.Context, tg.InputPeerClass, int, bool) error {
	return nil
}
func (*runtimeExecutionR5MessageService) UnpinMessage(context.Context, tg.InputPeerClass, int) error {
	return nil
}
func (*runtimeExecutionR5MessageService) ForwardMessages(context.Context, tg.InputPeerClass, tg.InputPeerClass, []int) error {
	return nil
}
func (*runtimeExecutionR5MessageService) PurgeMessages(context.Context, tg.InputPeerClass, int, int, int) (int, error) {
	return 0, nil
}

type runtimeExecutionR5Metrics struct {
	schedulerRunning int
	childWaiting     int
	queueDelays      []time.Duration
	attemptLatencies []time.Duration
}

type runtimeExecutionR5MeasuringClient struct {
	inner tasks.Client

	mu          sync.Mutex
	queueDelays []time.Duration
}

func (c *runtimeExecutionR5MeasuringClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	submittedAt := time.Now()
	ticket, err := c.inner.Submit(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &runtimeExecutionR5MeasuringTicket{
		Ticket:      ticket,
		submittedAt: submittedAt,
		record:      c.recordQueueDelay,
	}, nil
}

func (c *runtimeExecutionR5MeasuringClient) Cancel(id tasks.TaskID, reason tasks.Cause) (tasks.CancelReceipt, error) {
	return c.inner.Cancel(id, reason)
}

func (c *runtimeExecutionR5MeasuringClient) CancelScope(scope tasks.ScopeIdentity, reason tasks.Cause) int {
	return c.inner.CancelScope(scope, reason)
}

func (c *runtimeExecutionR5MeasuringClient) Snapshot(id tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return c.inner.Snapshot(id)
}

func (c *runtimeExecutionR5MeasuringClient) recordQueueDelay(delay time.Duration) {
	if delay < 0 {
		return
	}
	c.mu.Lock()
	c.queueDelays = append(c.queueDelays, delay)
	c.mu.Unlock()
}

func (c *runtimeExecutionR5MeasuringClient) queueDelaySnapshot() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.queueDelays...)
}

type runtimeExecutionR5MeasuringTicket struct {
	tasks.Ticket

	submittedAt time.Time
	record      func(time.Duration)
	once        sync.Once
}

func (t *runtimeExecutionR5MeasuringTicket) Wait(ctx context.Context) (tasks.TaskResult, error) {
	result, err := t.Ticket.Wait(ctx)
	if !result.StartedAt.IsZero() {
		t.once.Do(func() {
			t.record(result.StartedAt.Sub(t.submittedAt))
		})
	}
	return result, err
}

func runtimeExecutionR5Percentile(samples []time.Duration, percentile float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(float64(len(ordered)-1) * percentile)
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index]
}

func measureRuntimeExecutionR5ResourceContention(ctx context.Context, wrapperCount int) (runtimeExecutionR5Metrics, error) {
	if wrapperCount <= 0 {
		return runtimeExecutionR5Metrics{}, fmt.Errorf("wrapper count must be positive")
	}
	cfg := taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"scheduler": {Concurrency: wrapperCount, BacklogLimit: wrapperCount * 2, PayloadBudget: 1 << 20},
			"download":  {Concurrency: 1, BacklogLimit: wrapperCount * 2, PayloadBudget: 1 << 20},
		},
		ResultCapacity: wrapperCount*3 + 8,
		ResourceCapacities: map[string]int64{
			"download": 1,
		},
	}
	engine := taskengine.NewEngine(cfg)
	if err := engine.Start(ctx); err != nil {
		return runtimeExecutionR5Metrics{}, fmt.Errorf("start task engine: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = engine.Stop(stopCtx)
	}()

	blockerStarted := make(chan struct{})
	blockerRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseBlocker := func() { releaseOnce.Do(func() { close(blockerRelease) }) }
	defer releaseBlocker()
	blockerTicket, err := engine.Submit(ctx, tasks.WorkSpec{
		ID:         "r5-download-blocker",
		QuotaOwner: "r5:blocker",
		Pool:       "download",
		Class:      tasks.PriorityMaintenance,
		Handler: func(runCtx context.Context) error {
			close(blockerStarted)
			select {
			case <-blockerRelease:
				return nil
			case <-runCtx.Done():
				return runCtx.Err()
			}
		},
	})
	if err != nil {
		return runtimeExecutionR5Metrics{}, fmt.Errorf("submit blocker: %w", err)
	}
	select {
	case <-blockerStarted:
	case <-ctx.Done():
		return runtimeExecutionR5Metrics{}, ctx.Err()
	}

	router := core.NewRouter(".")
	command := core.Command{
		Name: "r5",
		Resources: []tasks.ResourceRequirement{
			{Name: "download", Amount: 1},
		},
		Handler: func(*core.Context) error { return nil },
	}
	if err := router.Register(command); err != nil {
		return runtimeExecutionR5Metrics{}, fmt.Errorf("register R5 command: %w", err)
	}
	messageService := &runtimeExecutionR5MessageService{}
	measuringClient := &runtimeExecutionR5MeasuringClient{inner: engine}
	handler := scheduledActionHandler{
		service: func() core.TelegramCapabilities {
			return core.TelegramCapabilities{Messages: messageService}
		},
		router:   router,
		executor: core.NewCommandExecutor(nil, nil, 5*time.Second),
		tasks:    measuringClient,
	}

	wrapperSubmitted := make([]time.Time, wrapperCount)
	wrapperTickets := make([]tasks.Ticket, 0, wrapperCount)
	for i := 0; i < wrapperCount; i++ {
		index := i
		wrapperSubmitted[index] = time.Now()
		ticket, submitErr := engine.Submit(ctx, tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("r5-wrapper-%d", index)),
			QuotaOwner: "r5:scheduler",
			Pool:       "scheduler",
			Class:      tasks.PriorityMaintenance,
			Handler: func(runCtx context.Context) error {
				return handler.executeCommand(runCtx, scheduler.ScheduledJob{
					ID:         int64(index + 1),
					ChatID:     1,
					PeerType:   "chat",
					ActionType: scheduler.ActionCommand,
					Payload:    ".r5",
				})
			},
		})
		if submitErr != nil {
			return runtimeExecutionR5Metrics{}, fmt.Errorf("submit wrapper %d: %w", index, submitErr)
		}
		wrapperTickets = append(wrapperTickets, ticket)
	}

	metrics := runtimeExecutionR5Metrics{}
	contentionDeadline := time.Now().Add(time.Second)
	for time.Now().Before(contentionDeadline) {
		stats, statsErr := engine.Stats(ctx)
		if statsErr != nil {
			return runtimeExecutionR5Metrics{}, fmt.Errorf("task engine stats: %w", statsErr)
		}
		schedulerStats := stats.Pools["scheduler"]
		downloadStats := stats.Pools["download"]
		if schedulerStats.Running > metrics.schedulerRunning {
			metrics.schedulerRunning = schedulerStats.Running
		}
		if downloadStats.Waiting > metrics.childWaiting {
			metrics.childWaiting = downloadStats.Waiting
		}
		if schedulerStats.Running == wrapperCount && downloadStats.Waiting >= wrapperCount {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if metrics.schedulerRunning != wrapperCount || metrics.childWaiting < wrapperCount {
		return runtimeExecutionR5Metrics{}, fmt.Errorf(
			"contention state not reached: scheduler_running=%d/%d child_waiting=%d/%d",
			metrics.schedulerRunning, wrapperCount, metrics.childWaiting, wrapperCount,
		)
	}

	releaseBlocker()
	if _, err := blockerTicket.Wait(ctx); err != nil {
		return runtimeExecutionR5Metrics{}, fmt.Errorf("wait blocker: %w", err)
	}

	for i, ticket := range wrapperTickets {
		result, waitErr := ticket.Wait(ctx)
		if waitErr != nil {
			return runtimeExecutionR5Metrics{}, fmt.Errorf("wait wrapper %d: %w", i, waitErr)
		}
		if result.FinishedAt.IsZero() {
			return runtimeExecutionR5Metrics{}, fmt.Errorf("wrapper %d finished without terminal timestamp", i)
		}
		metrics.attemptLatencies = append(metrics.attemptLatencies, result.FinishedAt.Sub(wrapperSubmitted[i]))
	}
	metrics.queueDelays = measuringClient.queueDelaySnapshot()
	if len(metrics.queueDelays) != wrapperCount {
		return runtimeExecutionR5Metrics{}, fmt.Errorf("recorded child queue delays = %d, want %d", len(metrics.queueDelays), wrapperCount)
	}
	return metrics, nil
}

func TestRuntimeExecutionE3_ResourceScheduledCommandOccupiesSchedulerPoolWhileChildQueued(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	metrics, err := measureRuntimeExecutionR5ResourceContention(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.schedulerRunning != 4 {
		t.Fatalf("scheduler running = %d, want 4 resource-bearing wrappers", metrics.schedulerRunning)
	}
	if metrics.childWaiting < 4 {
		t.Fatalf("download waiting = %d, want at least 4 child jobs", metrics.childWaiting)
	}
	t.Logf(
		"R5 resource contention: scheduler_running=%d child_waiting=%d child_queue_p50=%s child_queue_p95=%s attempt_p95=%s",
		metrics.schedulerRunning,
		metrics.childWaiting,
		runtimeExecutionR5Percentile(metrics.queueDelays, 0.50),
		runtimeExecutionR5Percentile(metrics.queueDelays, 0.95),
		runtimeExecutionR5Percentile(metrics.attemptLatencies, 0.95),
	)
}

func BenchmarkRuntimeExecutionE3_ResourceScheduledCommandContention(b *testing.B) {
	var schedulerRunning float64
	var childWaiting float64
	var queueP50 time.Duration
	var queueP95 time.Duration
	var attemptP95 time.Duration
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		metrics, err := measureRuntimeExecutionR5ResourceContention(ctx, 4)
		cancel()
		if err != nil {
			b.Fatal(err)
		}
		schedulerRunning += float64(metrics.schedulerRunning)
		childWaiting += float64(metrics.childWaiting)
		queueP50 += runtimeExecutionR5Percentile(metrics.queueDelays, 0.50)
		queueP95 += runtimeExecutionR5Percentile(metrics.queueDelays, 0.95)
		attemptP95 += runtimeExecutionR5Percentile(metrics.attemptLatencies, 0.95)
	}
	b.ReportMetric(schedulerRunning/float64(b.N), "scheduler_running/op")
	b.ReportMetric(childWaiting/float64(b.N), "child_waiting/op")
	b.ReportMetric(float64(queueP50.Nanoseconds())/float64(b.N), "child_queue_p50_ns/op")
	b.ReportMetric(float64(queueP95.Nanoseconds())/float64(b.N), "child_queue_p95_ns/op")
	b.ReportMetric(float64(attemptP95.Nanoseconds())/float64(b.N), "attempt_p95_ns/op")
}

func TestRuntimeExecutionE3_ResourceFreeScheduledCommandStaysInWrapper(t *testing.T) {
	cfg := taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"scheduler": {Concurrency: 1, BacklogLimit: 4, PayloadBudget: 1 << 20},
		},
		ResultCapacity: 8,
	}
	engine := taskengine.NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = engine.Stop(stopCtx)
	}()

	commandStarted := make(chan struct{})
	commandRelease := make(chan struct{})
	router := core.NewRouter(".")
	if err := router.Register(core.Command{
		Name: "r5direct",
		Handler: func(commandCtx *core.Context) error {
			close(commandStarted)
			select {
			case <-commandRelease:
				return nil
			case <-commandCtx.Ctx.Done():
				return commandCtx.Ctx.Err()
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	messageService := &runtimeExecutionR5MessageService{}
	handler := scheduledActionHandler{
		service: func() core.TelegramCapabilities {
			return core.TelegramCapabilities{Messages: messageService}
		},
		router:   router,
		executor: core.NewCommandExecutor(nil, nil, 5*time.Second),
		tasks:    engine,
	}
	ticket, err := engine.Submit(context.Background(), tasks.WorkSpec{
		ID:         "r5-resource-free-wrapper",
		QuotaOwner: "r5:scheduler",
		Pool:       "scheduler",
		Class:      tasks.PriorityMaintenance,
		Handler: func(runCtx context.Context) error {
			return handler.executeCommand(runCtx, scheduler.ScheduledJob{
				ID:         1,
				ChatID:     1,
				PeerType:   "chat",
				ActionType: scheduler.ActionCommand,
				Payload:    ".r5direct",
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-commandStarted:
	case <-time.After(time.Second):
		t.Fatal("resource-free command did not start in scheduler wrapper")
	}

	stats, err := engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Pools["scheduler"].Running; got != 1 {
		t.Fatalf("scheduler running = %d, want 1 wrapper", got)
	}
	if got := stats.ActiveTasks; got != 1 {
		t.Fatalf("active tasks = %d, want wrapper only with no child TaskEngine job", got)
	}

	close(commandRelease)
	if result, err := ticket.Wait(context.Background()); err != nil || !result.IsSuccess() {
		t.Fatalf("resource-free wrapper result=%+v err=%v", result, err)
	}
}
