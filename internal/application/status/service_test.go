package status_test

import (
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/application/status"
)

func TestCollectSnapshot(t *testing.T) {
	startTime := time.Now().Add(-5 * time.Minute)
	ownerID := int64(998877)

	snap := status.CollectSnapshot(startTime, ownerID)

	if snap.Uptime < 4*time.Minute {
		t.Errorf("expected uptime >= 4m, got %v", snap.Uptime)
	}
	if snap.OwnerID != ownerID {
		t.Errorf("expected ownerID %d, got %d", ownerID, snap.OwnerID)
	}
	if snap.GoVersion == "" {
		t.Error("expected non-empty GoVersion")
	}
	if snap.Goroutines <= 0 {
		t.Errorf("expected goroutines > 0, got %d", snap.Goroutines)
	}
	if snap.ProgressBar == "" {
		t.Error("expected non-empty ProgressBar")
	}
}

func TestRenderAliveCard(t *testing.T) {
	snap := status.Snapshot{
		Uptime:      10 * time.Minute,
		AllocMB:     12.5,
		SysMB:       35.0,
		Goroutines:  15,
		GoVersion:   "go1.22.0",
		OwnerID:     123456,
		ProgressBar: "[████░░░░]",
	}

	card := status.RenderAliveCard(snap, "MyAssistantBot")

	if !strings.Contains(card, "@MyAssistantBot") {
		t.Errorf("expected bot username in card, got %q", card)
	}
	if !strings.Contains(card, "123456") {
		t.Errorf("expected owner ID in card, got %q", card)
	}
	if !strings.Contains(card, "go1.22.0") {
		t.Errorf("expected go version in card, got %q", card)
	}
	if !strings.Contains(card, "[████░░░░]") {
		t.Errorf("expected progress bar in card, got %q", card)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d        time.Duration
		expected string
	}{
		{45 * time.Second, "45s"},
		{5*time.Minute + 30*time.Second, "5m 30s"},
		{2*time.Hour + 15*time.Minute, "2h 15m 0s"},
		{25*time.Hour + 10*time.Minute, "1d 1h 10m 0s"},
	}

	for _, tt := range tests {
		got := status.FormatDuration(tt.d)
		if got != tt.expected {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.d, got, tt.expected)
		}
	}
}
