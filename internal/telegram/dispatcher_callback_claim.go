package telegram

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/idempotency"
	"go.uber.org/zap"
)

const (
	callbackIdempotencyTTL       = 5 * time.Minute
	callbackClaimMutationTimeout = 2 * time.Second
)

type dispatcherCallbackClaim struct {
	claim *idempotency.ExecutionClaim
	key   string
}

func (d *Dispatcher) beginCallbackClaim(ctx context.Context, key string) (dispatcherCallbackClaim, bool, error) {
	claim, isNew, err := d.idempotencyMgr.Begin(ctx, key, callbackIdempotencyTTL)
	if err != nil || !isNew {
		return dispatcherCallbackClaim{}, isNew, err
	}
	return dispatcherCallbackClaim{claim: claim, key: key}, true, nil
}

func (d *Dispatcher) reserveCallbackClaim(ctx context.Context, claim dispatcherCallbackClaim) error {
	if claim.claim == nil {
		return nil
	}
	claimCtx, cancel := detachedCallbackClaimContext(ctx)
	defer cancel()
	return claim.claim.Reserve(claimCtx, callbackIdempotencyTTL)
}

func (d *Dispatcher) releaseCallbackClaim(ctx context.Context, claim dispatcherCallbackClaim) {
	if claim.claim == nil {
		return
	}
	claimCtx, cancel := detachedCallbackClaimContext(ctx)
	defer cancel()
	if err := claim.claim.Release(claimCtx); err != nil {
		d.logger.Error("dispatcher: callback idempotency release failed before task admission",
			zap.String("key", claim.key),
			zap.Error(err),
		)
	}
}

func detachedCallbackClaimContext(parent context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if parent != nil {
		base = context.WithoutCancel(parent)
	}
	return context.WithTimeout(base, callbackClaimMutationTimeout)
}
