package app

import (
	"context"
	"time"

	assistentrpc "github.com/inipew/goultroid/internal/assistant/rpc"
	"github.com/inipew/goultroid/internal/telegram"
)

type assistantRPCExecutor struct {
	executor *telegram.RPCExecutor
}

func (a assistantRPCExecutor) Do(ctx context.Context, method, family string, kind assistentrpc.Kind, timeout time.Duration, operation func(context.Context) error) error {
	telegramKind := telegram.RPCReadOnly
	switch kind {
	case assistentrpc.IdempotentMutation:
		telegramKind = telegram.RPCIdempotentMutation
	case assistentrpc.NonIdempotentMutation:
		telegramKind = telegram.RPCNonIdempotentMutation
	}
	meta := telegram.RPCMeta{Method: method, Family: family, Kind: telegramKind, Timeout: timeout}
	if telegramKind == telegram.RPCNonIdempotentMutation {
		meta.RetryPolicy = telegram.RetryPolicy{MaxAttempts: 1, InlineFloodWaitMax: 5 * time.Second}
	}
	return a.executor.Do(ctx, meta, operation)
}
