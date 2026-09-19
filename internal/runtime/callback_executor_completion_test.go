package runtime

import (
	"context"
	"errors"
	"testing"
)

func TestCallbackExecutorCompletionReceivesNormalizedPanic(t *testing.T) {
	e := NewCallbackExecutor(1)
	done := make(chan error, 1)
	result, err := e.StartWithCompletion(context.Background(), func() error {
		panic("boom")
	}, func(err error) {
		done <- err
	})
	if err != nil {
		t.Fatal(err)
	}

	completionErr := <-done
	var panicErr *CallbackPanicError
	if !errors.As(completionErr, &panicErr) {
		t.Fatalf("completion error=%T %v, want CallbackPanicError", completionErr, completionErr)
	}
	resultErr := <-result
	if !errors.As(resultErr, &panicErr) {
		t.Fatalf("result error=%T %v, want CallbackPanicError", resultErr, resultErr)
	}
}

func TestCallbackExecutorCompletionRunsBeforeCapacityRelease(t *testing.T) {
	e := NewCallbackExecutor(1)
	done := make(chan CallbackExecutorStats, 1)
	result, err := e.StartWithCompletion(context.Background(), func() error { return nil }, func(error) {
		done <- e.Stats()
	})
	if err != nil {
		t.Fatal(err)
	}
	stats := <-done
	if stats.Active != 1 {
		t.Fatalf("completion observed active=%d, want 1 before release", stats.Active)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
