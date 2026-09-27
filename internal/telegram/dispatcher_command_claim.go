package telegram

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/idempotency"
	"go.uber.org/zap"
)

const (
	commandIdempotencyTTL       = 5 * time.Minute
	commandClaimMutationTimeout = 2 * time.Second
)

type dispatcherCommandClaim struct {
	claim *idempotency.ExecutionClaim
	key   string
}

func (d *Dispatcher) beginCommandClaim(ctx context.Context, key string) (dispatcherCommandClaim, bool, error) {
	claim, isNew, err := d.idempotencyMgr.Begin(ctx, key, commandIdempotencyTTL)
	if err != nil || !isNew {
		return dispatcherCommandClaim{}, isNew, err
	}
	return dispatcherCommandClaim{claim: claim, key: key}, true, nil
}

func (d *Dispatcher) acceptCommandClaim(ctx context.Context, command string, claim dispatcherCommandClaim) error {
	if claim.claim == nil {
		return nil
	}
	claimCtx, cancel := detachedCommandClaimContext(ctx)
	defer cancel()
	if err := claim.claim.Accept(claimCtx, commandIdempotencyTTL); err != nil {
		d.logger.Error("dispatcher: command idempotency accept failed after task admission",
			zap.String("key", claim.key),
			zap.String("command", command),
			zap.Error(err),
		)
		return err
	}
	return nil
}

func (d *Dispatcher) releaseCommandClaim(ctx context.Context, command string, claim dispatcherCommandClaim) {
	if claim.claim == nil {
		return
	}
	claimCtx, cancel := detachedCommandClaimContext(ctx)
	defer cancel()
	if err := claim.claim.Release(claimCtx); err != nil {
		d.logger.Error("dispatcher: command idempotency release failed before task admission",
			zap.String("key", claim.key),
			zap.String("command", command),
			zap.Error(err),
		)
	}
}

func detachedCommandClaimContext(parent context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if parent != nil {
		base = context.WithoutCancel(parent)
	}
	return context.WithTimeout(base, commandClaimMutationTimeout)
}
