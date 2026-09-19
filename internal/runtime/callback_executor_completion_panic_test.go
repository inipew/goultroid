package runtime

import (
	"context"
	"errors"
	"testing"
)

func TestCallbackExecutorCompletionPanicIsContained(t *testing.T) {
	e := NewCallbackExecutor(1)
	result, err := e.StartWithCompletion(context.Background(), func() error { return nil }, func(error) {
		panic("completion boom")
	})
	if err != nil {
		t.Fatal(err)
	}
	resultErr := <-result
	var panicErr *CallbackPanicError
	if !errors.As(resultErr, &panicErr) {
		t.Fatalf("result error=%T %v, want contained CallbackPanicError", resultErr, resultErr)
	}
	if got := e.Stats().Active; got != 0 {
		t.Fatalf("executor active=%d after completion panic, want 0", got)
	}
}
