package jobs

import (
	"context"
	"testing"
	"time"
)

func TestDefaultLazyWorkerIdleWindows(t *testing.T) {
	pump := NewPersistencePump(2, 4)
	if pump.idleTimeout != 10*time.Second {
		t.Fatalf("persistence idle timeout = %s, want 10s", pump.idleTimeout)
	}
	if retryIdleTimeout != 10*time.Second {
		t.Fatalf("retry idle timeout = %s, want 10s", retryIdleTimeout)
	}

	pump.idleTimeout = 0
	if err := pump.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pump.idleTimeout != 10*time.Second {
		t.Fatalf("persistence fallback idle timeout = %s, want 10s", pump.idleTimeout)
	}
	if err := pump.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
