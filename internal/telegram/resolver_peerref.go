package telegram

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/telegram/peers"
	"github.com/inipew/goultroid/internal/core"
)

func (r *Resolver) ResolveRef(ctx context.Context, ref string) (core.PeerRef, error) {
	p, err := r.Resolve(ctx, ref)
	if err != nil {
		return core.PeerRef{}, err
	}
	return core.PeerRefFromInputPeer(p)
}

func (r *Resolver) InvalidatePeer(ref core.PeerRef) error {
	if r.storage == nil {
		return nil
	}
	prefix := map[core.PeerKind]string{core.PeerKindUser: "user", core.PeerKindChat: "chat", core.PeerKindChannel: "channel"}[ref.Kind]
	if prefix == "" {
		return fmt.Errorf("invalid peer kind %d", ref.Kind)
	}
	return r.storage.Invalidate(peers.Key{Prefix: prefix, ID: ref.ID})
}

func (r *Resolver) ResolveWithRecovery(ctx context.Context, ref core.PeerRef, fn func(context.Context, core.PeerRef) error) error {
	if !ref.Valid() {
		return fmt.Errorf("invalid peer reference: kind=%s id=%d", ref.Kind, ref.ID)
	}
	if err := fn(ctx, ref); err == nil {
		return nil
	} else if !IsStalePeerError(err) {
		return err
	}
	if err := r.InvalidatePeer(ref); err != nil {
		return fmt.Errorf("invalidate stale peer: %w", err)
	}
	lookup := fmt.Sprintf("%d", ref.ID)
	if ref.Kind == core.PeerKindChannel {
		lookup = fmt.Sprintf("-100%d", ref.ID)
	}
	fresh, err := r.ResolveRef(ctx, lookup)
	if err != nil {
		return fmt.Errorf("re-resolve stale peer: %w", err)
	}
	return fn(ctx, fresh)
}

func IsStalePeerError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToUpper(err.Error())
	for _, code := range []string{"PEER_ID_INVALID", "USER_ID_INVALID", "CHANNEL_INVALID", "INPUT_CONSTRUCTOR_INVALID", "ACCESS_HASH_INVALID"} {
		if strings.Contains(s, code) {
			return true
		}
	}
	return false
}
