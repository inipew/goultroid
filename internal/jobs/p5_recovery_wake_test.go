package jobs

import (
	"context"
	"testing"
)

func TestRegisterHandlerWakesRecovery(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.recoveryWake = make(chan struct{}, 1)
	if err := m.RegisterHandler("late-handler", func(context.Context, JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.recoveryWake:
	default:
		t.Fatal("registration did not wake recovery")
	}
}
