package client

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/core"
)

type corePeerResolverAPI interface {
	ContactsResolveUsername(context.Context, *tg.ContactsResolveUsernameRequest) (*tg.ContactsResolvedPeer, error)
}

type assistantCorePeerResolver struct {
	api   corePeerResolverAPI
	peers peer.Resolver
}

var _ core.PeerResolver = (*assistantCorePeerResolver)(nil)

func newAssistantCorePeerResolver(api corePeerResolverAPI, peers peer.Resolver) *assistantCorePeerResolver {
	if api == nil || peers == nil {
		return nil
	}
	return &assistantCorePeerResolver{api: api, peers: peers}
}

func (r *assistantCorePeerResolver) cacheResolved(result *tg.ContactsResolvedPeer) {
	if r == nil || r.peers == nil || result == nil {
		return
	}
	entities := tg.Entities{
		Users:    make(map[int64]*tg.User),
		Chats:    make(map[int64]*tg.Chat),
		Channels: make(map[int64]*tg.Channel),
	}
	for _, item := range result.Users {
		if user, ok := item.(*tg.User); ok && user != nil {
			entities.Users[user.ID] = user
		}
	}
	for _, item := range result.Chats {
		switch chat := item.(type) {
		case *tg.Chat:
			if chat != nil {
				entities.Chats[chat.ID] = chat
			}
		case *tg.Channel:
			if chat != nil {
				entities.Channels[chat.ID] = chat
			}
		}
	}
	r.peers.Cache().CacheEntities(entities)
}

func (r *assistantCorePeerResolver) resolveUsername(ctx context.Context, ref string) (*tg.ContactsResolvedPeer, error) {
	username := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(ref, "@")))
	if username == "" {
		return nil, core.ErrInvalidArgs
	}
	result, err := r.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
	if err != nil {
		return nil, fmt.Errorf("%w: resolve username %q: %v", core.ErrUnavailable, username, err)
	}
	if result == nil {
		return nil, fmt.Errorf("%w: username %q returned no result", core.ErrNotFound, username)
	}
	r.cacheResolved(result)
	return result, nil
}

func (r *assistantCorePeerResolver) ResolveUser(ctx context.Context, ref string) (tg.InputPeerClass, int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, 0, core.ErrInvalidArgs
	}
	if userID, err := strconv.ParseInt(ref, 10, 64); err == nil {
		if userID <= 0 {
			return nil, 0, core.ErrInvalidArgs
		}
		resolved, resolveErr := r.peers.ReResolve(ctx, &tg.InputPeerUser{UserID: userID})
		if resolveErr != nil {
			return nil, 0, fmt.Errorf("%w: resolve user %d: %v", core.ErrPeerUnresolved, userID, resolveErr)
		}
		user, ok := resolved.(*tg.InputPeerUser)
		if !ok || user.AccessHash == 0 {
			return nil, 0, fmt.Errorf("%w: user %d", core.ErrAccessHashMissing, userID)
		}
		return user, userID, nil
	}

	result, err := r.resolveUsername(ctx, ref)
	if err != nil {
		return nil, 0, err
	}
	resolvedPeer, ok := result.Peer.(*tg.PeerUser)
	if !ok || resolvedPeer.UserID <= 0 {
		return nil, 0, fmt.Errorf("%w: username %q is not a user", core.ErrNotFound, ref)
	}
	for _, item := range result.Users {
		user, ok := item.(*tg.User)
		if !ok || user == nil || user.ID != resolvedPeer.UserID {
			continue
		}
		if user.AccessHash == 0 {
			return nil, 0, fmt.Errorf("%w: user %d", core.ErrAccessHashMissing, user.ID)
		}
		return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, user.ID, nil
	}
	return nil, 0, fmt.Errorf("%w: username %q returned no matching user", core.ErrNotFound, ref)
}

func (r *assistantCorePeerResolver) ResolveChat(ctx context.Context, ref string) (tg.InputPeerClass, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, core.ErrInvalidArgs
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		if id == 0 {
			return nil, core.ErrInvalidArgs
		}
		if id < 0 {
			text := strconv.FormatInt(id, 10)
			if strings.HasPrefix(text, "-100") {
				channelID, parseErr := strconv.ParseInt(text[4:], 10, 64)
				if parseErr != nil || channelID <= 0 {
					return nil, core.ErrInvalidArgs
				}
				resolved, resolveErr := r.peers.ReResolve(ctx, &tg.InputPeerChannel{ChannelID: channelID})
				if resolveErr != nil {
					return nil, fmt.Errorf("%w: resolve channel %d: %v", core.ErrPeerUnresolved, channelID, resolveErr)
				}
				return resolved, nil
			}
			return &tg.InputPeerChat{ChatID: -id}, nil
		}
		return &tg.InputPeerChat{ChatID: id}, nil
	}

	result, err := r.resolveUsername(ctx, ref)
	if err != nil {
		return nil, err
	}
	switch resolvedPeer := result.Peer.(type) {
	case *tg.PeerChannel:
		for _, item := range result.Chats {
			channel, ok := item.(*tg.Channel)
			if !ok || channel == nil || channel.ID != resolvedPeer.ChannelID {
				continue
			}
			if channel.AccessHash == 0 {
				return nil, fmt.Errorf("%w: channel %d", core.ErrAccessHashMissing, channel.ID)
			}
			return &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}, nil
		}
	case *tg.PeerChat:
		for _, item := range result.Chats {
			chat, ok := item.(*tg.Chat)
			if ok && chat != nil && chat.ID == resolvedPeer.ChatID {
				return &tg.InputPeerChat{ChatID: chat.ID}, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: username %q returned no matching chat", core.ErrNotFound, ref)
}

func (r *assistantCorePeerResolver) Resolve(ctx context.Context, ref string) (tg.InputPeerClass, error) {
	if user, _, err := r.ResolveUser(ctx, ref); err == nil {
		return user, nil
	}
	return r.ResolveChat(ctx, ref)
}
