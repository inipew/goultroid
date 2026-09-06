package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

func TestEngine_BoundedConcurrency(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	router := core.NewRouter(".")
	perms := core.NewPermissions(1001, nil)
	engine := NewEngine(db, func() core.TelegramServicer { return svc }, router, perms, zap.NewNop())

	// Set concurrency to 2 workers
	engine.SetMaxConcurrency(2)

	var currentConcurrent int32
	var maxObservedConcurrent int32

	_ = router.Register(core.Command{
		Name: "slowcmd",
		Handler: func(ctx *core.Context) error {
			curr := atomic.AddInt32(&currentConcurrent, 1)
			for {
				oldMax := atomic.LoadInt32(&maxObservedConcurrent)
				if curr <= oldMax || atomic.CompareAndSwapInt32(&maxObservedConcurrent, oldMax, curr) {
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
			atomic.AddInt32(&currentConcurrent, -1)
			return nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Seed 6 jobs due immediately
	for i := 0; i < 6; i++ {
		_, err := engine.ScheduleOnce(ctx, 12345, "chat", 0, time.Now().Add(-time.Minute), ActionCommand, ".slowcmd", 1001)
		if err != nil {
			t.Fatalf("failed to schedule job %d: %v", i, err)
		}
	}

	_ = engine.Start(ctx)
	defer engine.Stop()

	// Wait for jobs to process across multiple ticks
	time.Sleep(800 * time.Millisecond)

	maxSeen := atomic.LoadInt32(&maxObservedConcurrent)
	if maxSeen > 2 {
		t.Errorf("bounded concurrency exceeded: max observed was %d, expected <= 2", maxSeen)
	}
	if maxSeen == 0 {
		t.Errorf("expected jobs to run, max observed was 0")
	}
}

func TestEngine_MisfirePolicy_Skip(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	var sentCount int32
	mockSvc := &mockService{
		MockTelegramServicer: core.MockTelegramServicer{},
	}
	svcFn := func() core.TelegramServicer {
		return &customMockSender{
			mockService: mockSvc,
			onSend: func(text string) {
				atomic.AddInt32(&sentCount, 1)
			},
		}
	}

	router := core.NewRouter(".")
	perms := core.NewPermissions(1001, nil)
	engine := NewEngine(db, svcFn, router, perms, zap.NewNop())
	engine.SetMisfirePolicy(MisfireSkip)

	if engine.MisfirePolicy() != MisfireSkip {
		t.Errorf("expected MisfireSkip policy, got %v", engine.MisfirePolicy())
	}

	ctx := context.Background()

	// Create an overdue recurring job (> 1 minute overdue)
	job, err := engine.ScheduleRecurring(ctx, 999, "chat", 0, 10*time.Second, ActionMessage, "ping", 1001)
	if err != nil {
		t.Fatalf("failed to schedule recurring: %v", err)
	}

	// Artificially push NextRunAt back by 5 minutes to simulate bot downtime
	_, err = db.ExecContext(ctx, "UPDATE scheduled_jobs SET next_run_at = ? WHERE id = ?", time.Now().Add(-5*time.Minute), job.ID)
	if err != nil {
		t.Fatalf("failed to update next_run_at: %v", err)
	}

	claimed, err := db.ClaimDueScheduledJobs(ctx, time.Now(), 1, 30*time.Second)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("expected to claim overdue job")
	}

	// Execute job with MisfireSkip policy
	_, cancel2 := context.WithCancel(ctx)
	engine.executeJob(ctx, claimed[0], cancel2)

	// Per MisfireSkip, message should NOT have been sent
	if atomic.LoadInt32(&sentCount) != 0 {
		t.Errorf("expected MisfireSkip to skip execution, sent count: %d", atomic.LoadInt32(&sentCount))
	}
}

type customMockSender struct {
	*mockService
	onSend func(text string)
}

func (c *customMockSender) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if c.onSend != nil {
		c.onSend(text)
	}
	return c.mockService.SendMessage(ctx, peer, text)
}
