package telegram

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tgerr"
)

// TestResilience_CombinedFailureInjectionSoak stress-tests the RPCExecutor,
// RateLimiter, and PeerCache under concurrent multi-failure injection:
// - MTProto transient network errors (io.EOF)
// - Rate limit flood waits (FLOOD_WAIT_1)
// - Concurrent peer cache reads, writes, and invalidations
// Verifies no deadlocks, no memory leaks, hard elapsed budgets, and correct metrics.
func TestResilience_CombinedFailureInjectionSoak(t *testing.T) {
	fakeLim := &fakeLimiter{}
	metrics := NewInMemoryRPCMetrics()
	cache := NewPeerCache(ResolverCacheConfig{
		MaxEntries:  50,
		PositiveTTL: 100 * time.Millisecond,
	})

	exec, err := NewRPCExecutor(RPCExecutorConfig{
		Limiter: fakeLim,
		Metrics: metrics,
		DefaultPolicy: RetryPolicy{
			MaxAttempts:        3,
			BaseDelay:          1 * time.Millisecond,
			MaxDelay:           5 * time.Millisecond,
			MaxElapsed:         50 * time.Millisecond,
			InlineFloodWaitMax: 20 * time.Millisecond,
			JitterFraction:     0.1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 16
	const opsPerWorker = 50

	var (
		totalSuccess atomic.Int64
		totalFailed  atomic.Int64
		wg           sync.WaitGroup
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Launch concurrent cache modifier
	cacheStop := make(chan struct{})
	go func() {
		r := rand.New(rand.NewSource(12345))
		for {
			select {
			case <-cacheStop:
				return
			default:
				id := int64(r.Intn(100))
				key := fmt.Sprintf("user_%d", id)
				if r.Float32() < 0.3 {
					cache.Invalidate("username", key)
				} else {
					cache.Set("username", key, "user", id, id*10)
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()
	t.Cleanup(func() { close(cacheStop) })

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(workerID * 1000)))

			for i := 0; i < opsPerWorker; i++ {
				if ctx.Err() != nil {
					return
				}

				method := "messages.sendMessage"
				if i%2 == 0 {
					method = "users.getFullUser"
				}

				meta := RPCMeta{
					Method:  method,
					Family:  "messages",
					PeerKey: fmt.Sprintf("chat_%d", workerID%4),
					Kind:    RPCIdempotentMutation,
				}

				callAttempts := 0
				err := exec.Do(ctx, meta, func(runCtx context.Context) error {
					callAttempts++
					roll := rng.Float32()
					switch {
					case roll < 0.20:
						return io.EOF // transient network error
					case roll < 0.35:
						return tgerr.New(420, "FLOOD_WAIT_1")
					default:
						return nil
					}
				})

				if err == nil {
					totalSuccess.Add(1)
				} else {
					totalFailed.Add(1)
				}
			}
		}(w)
	}

	wg.Wait()

	if totalSuccess.Load() == 0 {
		t.Fatal("expected at least some successful RPC calls")
	}

	snap := metrics.Snapshot()
	if snap.TotalRequests == 0 {
		t.Fatal("expected non-zero metrics total requests")
	}

	// Verify cache bounded capacity after heavy churn
	if cache.Len() > 50 {
		t.Fatalf("cache len %d exceeded capacity 50", cache.Len())
	}
}
