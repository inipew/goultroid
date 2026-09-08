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

type Resolver struct {
	api         *tg.Client
	peerManager *peers.Manager
	storage     *PeerStorage
}

var _ core.PeerResolver = (*Resolver)(nil)

func NewResolver(api *tg.Client, peerManager *peers.Manager) *Resolver {
	return &Resolver{api: api, peerManager: peerManager}
}

func (r *Resolver) SetStorage(storage *PeerStorage) { r.storage = storage }

func (r *Resolver) Resolve(ctx context.Context, ref string) (tg.InputPeerClass, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, core.ErrInvalidArgs
	}
	lower := strings.ToLower(ref)
	if lower == "self" || lower == "me" {
		return &tg.InputPeerSelf{}, nil
	}
	if peer, _, err := r.ResolveUser(ctx, ref); err == nil && peer != nil {
		return peer, nil
	}
	if peer, err := r.ResolveChat(ctx, ref); err == nil && peer != nil {
		return peer, nil
	}
	return nil, fmt.Errorf("%w: entity %q could not be resolved", core.ErrNotFound, ref)
}

func (r *Resolver) ResolveUser(ctx context.Context, ref string) (tg.InputPeerClass, int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, 0, core.ErrInvalidArgs
	}
	if uid, err := strconv.ParseInt(ref, 10, 64); err == nil && uid > 0 {
		if r.peerManager != nil {
			if user, err := r.peerManager.ResolveUserID(ctx, uid); err == nil {
				peer := user.InputPeer()
				if input, ok := peer.(*tg.InputPeerUser); ok && input.AccessHash != 0 {
					return peer, user.ID(), nil
				}
			}
		}
		if r.storage != nil {
			if val, found, err := r.storage.Find(ctx, peers.Key{Prefix: "user", ID: uid}); err == nil && found && val.AccessHash != 0 {
				return &tg.InputPeerUser{UserID: uid, AccessHash: val.AccessHash}, uid, nil
			}
			return nil, 0, fmt.Errorf("%w: user %d has no cached access hash", core.ErrAccessHashMissing, uid)
		}
		return nil, 0, fmt.Errorf("%w: user %d requires a usable access hash", core.ErrAccessHashMissing, uid)
	}

	cleaned := strings.ToLower(strings.TrimPrefix(ref, "@"))
	// Username resolution is the recovery path for stale access hashes. When an
	// API is available, resolve against Telegram first and refresh local caches.
	if r.api != nil {
		if peer, id, err := r.resolveUsernameUser(ctx, cleaned); err == nil {
			return peer, id, nil
		}
	}
	if r.storage != nil {
		if key, val, found, err := r.storage.FindByUsername(ctx, cleaned); err == nil && found && val.AccessHash != 0 && key.Prefix == "user" {
			return &tg.InputPeerUser{UserID: key.ID, AccessHash: val.AccessHash}, key.ID, nil
		}
	}
	if r.peerManager != nil {
		if peer, err := r.peerManager.Resolve(ctx, cleaned); err == nil && peer != nil {
			input := peer.InputPeer()
			if user, ok := input.(*tg.InputPeerUser); ok && user.AccessHash != 0 {
				return input, peer.ID(), nil
			}
		}
	}
	return nil, 0, fmt.Errorf("%w: user %q not found", core.ErrNotFound, ref)
}

func (r *Resolver) resolveUsernameUser(ctx context.Context, username string) (tg.InputPeerClass, int64, error) {
	var resolved *tg.ContactsResolvedPeer
	err := RetryRPC(ctx, DefaultRPCPolicy, func(ctx context.Context) error {
		var err error
		resolved, err = r.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
		return err
	})
	if err != nil {
		return nil, 0, WrapRPCError("resolve username", err)
	}
	for _, entity := range resolved.Users {
		if user, ok := entity.(*tg.User); ok {
			if user.AccessHash == 0 {
				return nil, 0, fmt.Errorf("%w: user %d returned without access hash", core.ErrAccessHashMissing, user.ID)
			}
			if r.storage != nil {
				_ = r.storage.Save(ctx, peers.Key{Prefix: "user", ID: user.ID}, peers.Value{AccessHash: user.AccessHash})
				_ = r.storage.SaveEntity(ctx, "user", user.ID, user.Username, user.Phone, user.FirstName, user.LastName, "")
			}
			return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, user.ID, nil
		}
	}
	return nil, 0, fmt.Errorf("%w: username %q returned no user", core.ErrNotFound, username)
}

func (r *Resolver) ResolveChat(ctx context.Context, ref string) (tg.InputPeerClass, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, core.ErrInvalidArgs
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		if id < 0 {
			str := strconv.FormatInt(id, 10)
			if strings.HasPrefix(str, "-100") {
				channelID := id
				if parsed, err := strconv.ParseInt(str[4:], 10, 64); err == nil {
					channelID = parsed
				}
				if r.peerManager != nil {
					if channel, err := r.peerManager.ResolveChannelID(ctx, channelID); err == nil {
						peer := channel.InputPeer()
						if input, ok := peer.(*tg.InputPeerChannel); ok && input.AccessHash != 0 {
							return peer, nil
						}
					}
				}
				if r.storage != nil {
					if val, found, err := r.storage.Find(ctx, peers.Key{Prefix: "channel", ID: channelID}); err == nil && found && val.AccessHash != 0 {
						return &tg.InputPeerChannel{ChannelID: channelID, AccessHash: val.AccessHash}, nil
					}
					return nil, fmt.Errorf("%w: channel %d has no cached access hash", core.ErrAccessHashMissing, channelID)
				}
				return nil, fmt.Errorf("%w: channel %d requires a usable access hash", core.ErrAccessHashMissing, channelID)
			}
			chatID := -id
			if r.peerManager != nil {
				if chat, err := r.peerManager.ResolveChatID(ctx, chatID); err == nil {
					return chat.InputPeer(), nil
				}
			}
			return &tg.InputPeerChat{ChatID: chatID}, nil
		}
		if r.peerManager != nil {
			if chat, err := r.peerManager.ResolveChatID(ctx, id); err == nil {
				return chat.InputPeer(), nil
			}
		}
		return &tg.InputPeerChat{ChatID: id}, nil
	}

	cleaned := strings.ToLower(strings.TrimPrefix(ref, "@"))
	if r.api != nil {
		if peer, err := r.resolveUsernameChat(ctx, cleaned); err == nil {
			return peer, nil
		}
	}
	if r.storage != nil {
		if key, val, found, err := r.storage.FindByUsername(ctx, cleaned); err == nil && found {
			if key.Prefix == "channel" && val.AccessHash != 0 {
				return &tg.InputPeerChannel{ChannelID: key.ID, AccessHash: val.AccessHash}, nil
			}
			if key.Prefix == "chat" {
				return &tg.InputPeerChat{ChatID: key.ID}, nil
			}
		}
	}
	if r.peerManager != nil {
		if peer, err := r.peerManager.Resolve(ctx, cleaned); err == nil && peer != nil {
			return peer.InputPeer(), nil
		}
	}
	return nil, fmt.Errorf("%w: chat %q not found", core.ErrNotFound, ref)
}

func (r *Resolver) resolveUsernameChat(ctx context.Context, username string) (tg.InputPeerClass, error) {
	var resolved *tg.ContactsResolvedPeer
	err := RetryRPC(ctx, DefaultRPCPolicy, func(ctx context.Context) error {
		var err error
		resolved, err = r.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
		return err
	})
	if err != nil {
		return nil, WrapRPCError("resolve chat username", err)
	}
	for _, entity := range resolved.Chats {
		switch chat := entity.(type) {
		case *tg.Channel:
			if chat.AccessHash == 0 {
				return nil, fmt.Errorf("%w: channel %d returned without access hash", core.ErrAccessHashMissing, chat.ID)
			}
			if r.storage != nil {
				_ = r.storage.Save(ctx, peers.Key{Prefix: "channel", ID: chat.ID}, peers.Value{AccessHash: chat.AccessHash})
				_ = r.storage.SaveEntity(ctx, "channel", chat.ID, chat.Username, "", "", "", chat.Title)
			}
			return &tg.InputPeerChannel{ChannelID: chat.ID, AccessHash: chat.AccessHash}, nil
		case *tg.Chat:
			if r.storage != nil {
				_ = r.storage.Save(ctx, peers.Key{Prefix: "chat", ID: chat.ID}, peers.Value{AccessHash: 0})
				_ = r.storage.SaveEntity(ctx, "chat", chat.ID, "", "", "", "", chat.Title)
			}
			return &tg.InputPeerChat{ChatID: chat.ID}, nil
		}
	}
	return nil, fmt.Errorf("%w: username %q returned no chat", core.ErrNotFound, username)
}
