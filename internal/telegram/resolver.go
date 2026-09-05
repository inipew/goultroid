package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// Resolver implements core.PeerResolver using gotd peers.Manager and raw MTProto API.
type Resolver struct {
	api         *tg.Client
	peerManager *peers.Manager
}

// Ensure Resolver implements core.PeerResolver.
var _ core.PeerResolver = (*Resolver)(nil)

// NewResolver creates a new Resolver instance.
func NewResolver(api *tg.Client, peerManager *peers.Manager) *Resolver {
	return &Resolver{
		api:         api,
		peerManager: peerManager,
	}
}

// ResolveUser resolves a user reference (numeric ID, @username, or phone) into an InputPeer and User ID.
func (r *Resolver) ResolveUser(ctx context.Context, ref string) (tg.InputPeerClass, int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, 0, core.ErrInvalidArgs
	}

	// 1. Numeric User ID
	if uid, err := strconv.ParseInt(ref, 10, 64); err == nil && uid > 0 {
		if r.peerManager != nil {
			if u, err := r.peerManager.ResolveUserID(ctx, uid); err == nil {
				return u.InputPeer(), u.ID(), nil
			}
		}
		// Fallback without cached access hash
		return &tg.InputPeerUser{UserID: uid}, uid, nil
	}

	cleaned := strings.TrimPrefix(ref, "@")

	// 2. Try peers.Manager resolution (handles usernames, domains, phone numbers)
	if r.peerManager != nil {
		if p, err := r.peerManager.Resolve(ctx, cleaned); err == nil && p != nil {
			return p.InputPeer(), p.ID(), nil
		}
	}

	// 3. Fallback to raw ContactsResolveUsername
	if r.api != nil {
		resolved, err := r.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
			Username: cleaned,
		})
		if err == nil && resolved != nil {
			for _, u := range resolved.Users {
				if user, ok := u.(*tg.User); ok {
					return &tg.InputPeerUser{
						UserID:     user.ID,
						AccessHash: user.AccessHash,
					}, user.ID, nil
				}
			}
		}
	}

	return nil, 0, fmt.Errorf("%w: user %q not found", core.ErrNotFound, ref)
}

// ResolveChat resolves a chat or channel reference (ID or @username) into an InputPeer.
func (r *Resolver) ResolveChat(ctx context.Context, ref string) (tg.InputPeerClass, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, core.ErrInvalidArgs
	}

	// 1. Numeric Chat/Channel ID
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		// Telegram channel / supergroup notation often starts with -100
		if id < 0 {
			channelID := id
			str := strconv.FormatInt(id, 10)
			if strings.HasPrefix(str, "-100") {
				if parsed, err := strconv.ParseInt(str[4:], 10, 64); err == nil {
					channelID = parsed
				}
			} else {
				channelID = -id
			}

			if r.peerManager != nil {
				if ch, err := r.peerManager.ResolveChannelID(ctx, channelID); err == nil {
					return ch.InputPeer(), nil
				}
			}
			return &tg.InputPeerChannel{ChannelID: channelID}, nil
		}

		// Basic Chat
		if r.peerManager != nil {
			if c, err := r.peerManager.ResolveChatID(ctx, id); err == nil {
				return c.InputPeer(), nil
			}
		}
		return &tg.InputPeerChat{ChatID: id}, nil
	}

	cleaned := strings.TrimPrefix(ref, "@")

	// 2. peers.Manager resolution
	if r.peerManager != nil {
		if p, err := r.peerManager.Resolve(ctx, cleaned); err == nil && p != nil {
			return p.InputPeer(), nil
		}
	}

	// 3. Fallback to raw ContactsResolveUsername
	if r.api != nil {
		resolved, err := r.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
			Username: cleaned,
		})
		if err == nil && resolved != nil {
			for _, c := range resolved.Chats {
				switch ch := c.(type) {
				case *tg.Channel:
					return &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}, nil
				case *tg.Chat:
					return &tg.InputPeerChat{ChatID: ch.ID}, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("%w: chat %q not found", core.ErrNotFound, ref)
}
