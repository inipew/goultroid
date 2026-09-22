package client

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/core"
)

type groupQueryAPI interface {
	MessagesGetFullChat(context.Context, int64) (*tg.MessagesChatFull, error)
	ChannelsGetFullChannel(context.Context, tg.InputChannelClass) (*tg.MessagesChatFull, error)
}

// managedGroupQuery is the P7-D read-only manager query transport. It contains
// no mutation methods and all Telegram RPC calls go through managedAPI.
type managedGroupQuery struct {
	api      groupQueryAPI
	resolver peer.Resolver
}

func newManagedGroupQuery(api groupQueryAPI, resolver peer.Resolver) *managedGroupQuery {
	if api == nil || resolver == nil {
		return nil
	}
	return &managedGroupQuery{api: api, resolver: resolver}
}

func (q *managedGroupQuery) GetFullChat(ctx context.Context, input tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	if q == nil || q.api == nil || q.resolver == nil {
		return nil, fmt.Errorf("%w: manager query transport unavailable", core.ErrUnavailable)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	switch peerInput := input.(type) {
	case *tg.InputPeerChat:
		if peerInput == nil || peerInput.ChatID <= 0 {
			return nil, fmt.Errorf("%w: invalid basic-group peer", core.ErrPeerInvalid)
		}
		return q.api.MessagesGetFullChat(ctx, peerInput.ChatID)

	case *tg.InputPeerChannel:
		if peerInput == nil || peerInput.ChannelID <= 0 {
			return nil, fmt.Errorf("%w: invalid supergroup peer", core.ErrPeerInvalid)
		}
		resolved := peerInput
		if resolved.AccessHash == 0 {
			refreshed, err := q.resolver.ReResolve(ctx, peerInput)
			if err != nil {
				return nil, fmt.Errorf("%w: refresh supergroup %d: %v", core.ErrPeerUnresolved, peerInput.ChannelID, err)
			}
			var ok bool
			resolved, ok = refreshed.(*tg.InputPeerChannel)
			if !ok || resolved == nil || resolved.ChannelID != peerInput.ChannelID || resolved.AccessHash == 0 {
				return nil, fmt.Errorf("%w: invalid refreshed supergroup peer", core.ErrPeerUnresolved)
			}
		}
		return q.api.ChannelsGetFullChannel(ctx, &tg.InputChannel{
			ChannelID:  resolved.ChannelID,
			AccessHash: resolved.AccessHash,
		})

	default:
		return nil, fmt.Errorf("%w: manager query peer %T", core.ErrUnsupported, input)
	}
}
