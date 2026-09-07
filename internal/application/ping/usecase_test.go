package ping_test

import (
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/application/ping"
)

func TestFormatResult(t *testing.T) {
	latency := 42 * time.Millisecond
	res := ping.FormatResult(latency)

	if !strings.Contains(res, "42 ms") {
		t.Errorf("expected '42 ms' in output, got %q", res)
	}
	if !strings.Contains(res, "Pong!") {
		t.Errorf("expected 'Pong!' in output, got %q", res)
	}
}

func TestUseCase_Execute(t *testing.T) {
	uc := ping.NewUseCase()
	res, err := uc.Execute(func() error {
		time.Sleep(10 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Latency < 10*time.Millisecond {
		t.Errorf("expected latency >= 10ms, got %v", res.Latency)
	}
}
