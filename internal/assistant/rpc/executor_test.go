package rpc

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDirectExecutor_PreservesShorterParentDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := (DirectExecutor{}).Do(parent, "users.getUsers", "users", ReadOnly, time.Second, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Do() error = %v, want deadline exceeded", err)
	}
}

func TestDirectExecutor_UsesOperationTimeout(t *testing.T) {
	started := time.Now()
	err := (DirectExecutor{}).Do(context.Background(), "upload.getFile", "upload", ReadOnly, 15*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Do() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("operation timeout was not bounded: %v", elapsed)
	}
}
