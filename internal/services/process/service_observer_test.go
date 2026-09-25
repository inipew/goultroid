package process

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOSRunnerStreamsObservedOutput(t *testing.T) {
	runner := NewOSRunner(1, time.Second, 1024)

	var (
		mu       sync.Mutex
		observed strings.Builder
	)
	res, err := runner.Run(context.Background(), Request{
		Command: "printf",
		Args:    []string{"%s", "progress-stream"},
		StdoutObserver: func(chunk []byte) {
			mu.Lock()
			defer mu.Unlock()
			observed.Write(chunk)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Stdout != "progress-stream" {
		t.Fatalf("stdout = %q, want progress-stream", res.Stdout)
	}

	mu.Lock()
	got := observed.String()
	mu.Unlock()
	if got != "progress-stream" {
		t.Fatalf("observed stdout = %q, want progress-stream", got)
	}
}
