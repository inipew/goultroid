package rpc

import (
	"context"
	"time"
)

type Kind uint8

const (
	ReadOnly Kind = iota + 1
	IdempotentMutation
	NonIdempotentMutation
)

// Executor is the assistant-side port for the application Telegram executor.
type Executor interface {
	Do(ctx context.Context, method, family string, kind Kind, timeout time.Duration, operation func(context.Context) error) error
}

// DirectExecutor is a compatibility fallback for standalone assistant tests.
// Production wiring replaces it with the application RPCExecutor adapter.
type DirectExecutor struct{}

func (DirectExecutor) Do(ctx context.Context, _, _ string, _ Kind, timeout time.Duration, operation func(context.Context) error) error {
	if timeout <= 0 {
		return operation(ctx)
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return operation(opCtx)
}
