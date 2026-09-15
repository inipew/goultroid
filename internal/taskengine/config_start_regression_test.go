package taskengine

import (
	"context"
	"testing"
)

func TestEngineStartRejectsInvalidConfigBeforeLaunchingRuntime(t *testing.T) {
	e := NewEngine(Config{ResultCapacity: -1})
	if err := e.Start(context.Background()); err == nil {
		t.Fatal("expected invalid configuration to be rejected by Start")
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runStarted {
		t.Fatal("invalid engine marked itself started")
	}
	if e.rootCtx != nil || e.rootCancel != nil {
		t.Fatal("invalid engine created a runtime context")
	}
	if e.inbox != nil || e.controlInbox != nil {
		t.Fatal("invalid engine allocated runtime inboxes")
	}
	if got := e.runtimeRemaining.Load(); got != 0 {
		t.Fatalf("invalid engine launched runtime loops: remaining=%d", got)
	}
}

func TestValidateConfigRejectsNegativeTerminalRetention(t *testing.T) {
	if err := ValidateConfig(Config{MaxTerminalRetained: -1}); err == nil {
		t.Fatal("expected negative terminal retention to be rejected")
	}
}
