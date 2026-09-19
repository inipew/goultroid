package taskengine

import (
	"testing"
	"time"
)

func TestDefaultConfigBoundsTerminalRetentionByTime(t *testing.T) {
	cfg := NewDefaultConfig()
	if cfg.TerminalTTL != 5*time.Minute {
		t.Fatalf("default terminal TTL=%v, want 5m", cfg.TerminalTTL)
	}
}
