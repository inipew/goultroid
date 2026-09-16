package sysinfo

import (
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/taskengine"
)

func TestFormatPoolRuntimeStats(t *testing.T) {
	stats := taskengine.PoolRuntimeStats{
		Workers:      8,
		MinWorkers:   2,
		MaxWorkers:   16,
		Idle:         5,
		IdleWorkers:  5,
		Running:      2,
		Dispatching:  1,
		Waiting:      4,
		WaitingBytes: 2048,
	}

	formatted := formatPoolRuntimeStats("interactive", stats)
	expectedTokens := []string{
		"• <b>interactive</b>:",
		"running 2",
		"dispatching 1",
		"idle 5",
		"waiting 4 / 2.0 KB",
		"workers 8 (2-16)",
	}

	for _, token := range expectedTokens {
		if !strings.Contains(formatted, token) {
			t.Errorf("expected formatted stats to contain %q, got %q", token, formatted)
		}
	}
}
