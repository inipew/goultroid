package taskengine

import (
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestDefaultConfigFastIdleSettling(t *testing.T) {
	cfg := NewDefaultConfig()
	want := map[tasks.PoolID]time.Duration{
		"general":       10 * time.Second,
		"interactive":   10 * time.Second,
		"download":      15 * time.Second,
		"media-process": 20 * time.Second,
		"scheduler":     30 * time.Second,
	}
	for pool, idle := range want {
		got, ok := cfg.Pools[pool]
		if !ok {
			t.Fatalf("missing default pool %q", pool)
		}
		if got.IdleTimeout != idle {
			t.Fatalf("pool %s idle timeout = %s, want %s", pool, got.IdleTimeout, idle)
		}
		if !got.ZeroIdle {
			t.Fatalf("pool %s must remain zero-idle", pool)
		}
	}

	wantConcurrency := map[tasks.PoolID]int{
		"general":       8,
		"interactive":   32,
		"download":      3,
		"media-process": 2,
		"scheduler":     4,
	}
	for pool, concurrency := range wantConcurrency {
		if got := cfg.Pools[pool].Concurrency; got != concurrency {
			t.Fatalf("pool %s concurrency = %d, want %d", pool, got, concurrency)
		}
	}
}

func TestDefaultLazyLaneAndPoolFallbackIdleWindow(t *testing.T) {
	engine := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"custom": {Concurrency: 1, ZeroIdle: true, BacklogLimit: 1, PayloadBudget: 1 << 20},
		},
		ResultCapacity: 1,
	})
	if got := engine.poolIdleTimeouts["custom"]; got != 10*time.Second {
		t.Fatalf("fallback pool idle timeout = %s, want 10s", got)
	}
	if defaultLaneIdleTimeout != 10*time.Second {
		t.Fatalf("lazy lane idle timeout = %s, want 10s", defaultLaneIdleTimeout)
	}
}
