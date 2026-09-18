package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

type Resolver struct {
	api             *tg.Client
	peerManager     *peers.Manager
	storage         *PeerStorage
	cache           *PeerCache
	executor        *RPCExecutor
	logger          *zap.Logger
	group           singleflight.Group
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
}

var _ core.PeerResolver = (*Resolver)(nil)

// NewResolverWithConfig initializes a peer resolver with custom cache configurations.
func NewResolverWithConfig(api *tg.Client, peerManager *peers.Manager, cfg ResolverCacheConfig) *Resolver {
	ctx, cancel := context.WithCancel(context.Background())
	executor, _ := NewRPCExecutor(RPCExecutorConfig{DefaultPolicy: DefaultExecutorPolicy})
	return &Resolver{
		api:             api,
		peerManager:     peerManager,
		cache:           NewPeerCache(cfg),
		executor:        executor,
		lifecycleCtx:    ctx,
		lifecycleCancel: cancel,
	}
}

func NewResolver(api *tg.Client, peerManager *peers.Manager) *Resolver {
	return NewResolverWithConfig(api, peerManager, DefaultResolverCacheConfig)
}

// SetLogger sets the logger for recording resolver warnings and diagnostics.
func (r *Resolver) SetLogger(logger *zap.Logger) { r.logger = logger }

// Close cancels active underlying network resolutions.
func (r *Resolver) Close() error {
	if r != nil && r.lifecycleCancel != nil {
		r.lifecycleCancel()
	}
	return nil
}

func (r *Resolver) SetStorage(storage *PeerStorage) { r.storage = storage }

func (r *Resolver) SetCache(cache *PeerCache) { r.cache = cache }

func (r *Resolver) SetExecutor(exec *RPCExecutor) { r.executor = exec }

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
		strID := strconv.FormatInt(uid, 10)
		if r.cache != nil {
			if entry, hit := r.cache.Get("user", strID); hit {
				if entry.Negative {
					return nil, 0, fmt.Errorf("%w: user %d not found (cached)", core.ErrNotFound, uid)
				}
				if entry.AccessHash != 0 {
					return &tg.InputPeerUser{UserID: uid, AccessHash: entry.AccessHash}, uid, nil
				}
			}
		}
		if r.peerManager != nil {
			if user, err := r.peerManager.ResolveUserID(ctx, uid); err == nil {
				peer := user.InputPeer()
				if input, ok := peer.(*tg.InputPeerUser); ok && input.AccessHash != 0 {
					if r.cache != nil {
						r.cache.Set("user", strID, "user", uid, input.AccessHash)
					}
					return peer, user.ID(), nil
				}
			}
		}
		if r.storage != nil {
			if val, found, err := r.storage.Find(ctx, peers.Key{Prefix: "user", ID: uid}); err == nil && found && val.AccessHash != 0 {
				if r.cache != nil {
					r.cache.Set("user", strID, "user", uid, val.AccessHash)
				}
				return &tg.InputPeerUser{UserID: uid, AccessHash: val.AccessHash}, uid, nil
			}
			return nil, 0, fmt.Errorf("%w: user %d has no cached access hash", core.ErrAccessHashMissing, uid)
		}
		return nil, 0, fmt.Errorf("%w: user %d requires a usable access hash", core.ErrAccessHashMissing, uid)
	}

	cleaned := strings.ToLower(strings.TrimPrefix(ref, "@"))

	// 1. Memory Cache Lookup
	if r.cache != nil {
		if entry, hit := r.cache.Get("user", cleaned); hit {
			if entry.Negative {
				return nil, 0, fmt.Errorf("%w: user %q not found (cached)", core.ErrNotFound, ref)
			}
			if entry.AccessHash != 0 {
				return &tg.InputPeerUser{UserID: entry.ID, AccessHash: entry.AccessHash}, entry.ID, nil
			}
		}
	}

	// 2. Persistent Storage Lookup
	if r.storage != nil {
		if key, val, found, err := r.storage.FindByUsername(ctx, cleaned); err == nil && found && val.AccessHash != 0 && key.Prefix == "user" {
			if r.cache != nil {
				r.cache.Set("user", cleaned, "user", key.ID, val.AccessHash)
			}
			return &tg.InputPeerUser{UserID: key.ID, AccessHash: val.AccessHash}, key.ID, nil
		}
	}

	// 3. Singleflight Telegram Network Resolve
	if r.api != nil {
		type userResult struct {
			peer tg.InputPeerClass
			id   int64
		}

		resCh := r.group.DoChan("user:"+cleaned, func() (any, error) {
			underlyingCtx := r.lifecycleCtx
			if underlyingCtx == nil {
				underlyingCtx = context.Background()
			}
			callCtx, cancel := context.WithTimeout(underlyingCtx, 15*time.Second)
			defer cancel()
			p, id, err := r.resolveUsernameUser(callCtx, cleaned)
			if err != nil {
				return nil, err
			}
			return userResult{peer: p, id: id}, nil
		})

		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case res := <-resCh:
			if res.Err != nil {
				if errors.Is(res.Err, core.ErrNotFound) || ClassifyRPCError(res.Err) == RPCInvalidRequest {
					if r.cache != nil {
						r.cache.SetNegative("user", cleaned)
					}
				}
				return nil, 0, res.Err
			}
			ur := res.Val.(userResult)
			return ur.peer, ur.id, nil
		}
	}

	return nil, 0, fmt.Errorf("%w: user %q not found", core.ErrNotFound, ref)
}

func (r *Resolver) resolveUsernameUser(ctx context.Context, username string) (tg.InputPeerClass, int64, error) {
	meta := RPCMeta{
		Method: "contacts.resolveUsername",
		Family: "contacts",
		Kind:   RPCReadOnly,
	}

	resolved, err := ExecuteRPC(ctx, r.executor, meta, func(opCtx context.Context) (*tg.ContactsResolvedPeer, error) {
		return r.api.ContactsResolveUsername(opCtx, &tg.ContactsResolveUsernameRequest{Username: username})
	})

	if err != nil {
		return nil, 0, WrapRPCError("resolve username", err)
	}
	if r.peerManager != nil && resolved != nil {
		_ = r.peerManager.Apply(ctx, resolved.Users, resolved.Chats)
	}
	for _, entity := range resolved.Users {
		if user, ok := entity.(*tg.User); ok {
			if user.AccessHash == 0 {
				return nil, 0, fmt.Errorf("%w: user %d returned without access hash", core.ErrAccessHashMissing, user.ID)
			}
			if r.storage != nil {
				if err := r.storage.Save(ctx, peers.Key{Prefix: "user", ID: user.ID}, peers.Value{AccessHash: user.AccessHash}); err != nil && r.logger != nil {
					r.logger.Warn("failed to persist user peer to storage", zap.Int64("user_id", user.ID), zap.Error(err))
				}
				if err := r.storage.SaveEntity(ctx, "user", user.ID, user.Username, user.Phone, user.FirstName, user.LastName, ""); err != nil && r.logger != nil {
					r.logger.Warn("failed to persist user entity to storage", zap.Int64("user_id", user.ID), zap.Error(err))
				}
			}
			if r.cache != nil {
				r.cache.Set("user", username, "user", user.ID, user.AccessHash)
				r.cache.Set("user", strconv.FormatInt(user.ID, 10), "user", user.ID, user.AccessHash)
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
		strID := strconv.FormatInt(id, 10)
		if r.cache != nil {
			if entry, hit := r.cache.Get("chat", strID); hit && entry.Negative {
				return nil, fmt.Errorf("%w: chat %d not found (cached)", core.ErrNotFound, id)
			}
		}
		if id < 0 {
			str := strconv.FormatInt(id, 10)
			if strings.HasPrefix(str, "-100") {
				channelID := id
				if parsed, err := strconv.ParseInt(str[4:], 10, 64); err == nil {
					channelID = parsed
				}
				chanStr := strconv.FormatInt(channelID, 10)
				if r.cache != nil {
					if entry, hit := r.cache.Get("channel", chanStr); hit && entry.AccessHash != 0 {
						return &tg.InputPeerChannel{ChannelID: channelID, AccessHash: entry.AccessHash}, nil
					}
				}
				if r.peerManager != nil {
					if channel, err := r.peerManager.ResolveChannelID(ctx, channelID); err == nil {
						peer := channel.InputPeer()
						if input, ok := peer.(*tg.InputPeerChannel); ok && input.AccessHash != 0 {
							if r.cache != nil {
								r.cache.Set("channel", chanStr, "channel", channelID, input.AccessHash)
							}
							return peer, nil
						}
					}
				}
				if r.storage != nil {
					if val, found, err := r.storage.Find(ctx, peers.Key{Prefix: "channel", ID: channelID}); err == nil && found && val.AccessHash != 0 {
						if r.cache != nil {
							r.cache.Set("channel", chanStr, "channel", channelID, val.AccessHash)
						}
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

	// 1. Memory Cache Lookup
	if r.cache != nil {
		if entry, hit := r.cache.Get("chat", cleaned); hit {
			if entry.Negative {
				return nil, fmt.Errorf("%w: chat %q not found (cached)", core.ErrNotFound, ref)
			}
			if entry.Prefix == "channel" && entry.AccessHash != 0 {
				return &tg.InputPeerChannel{ChannelID: entry.ID, AccessHash: entry.AccessHash}, nil
			}
			if entry.Prefix == "chat" {
				return &tg.InputPeerChat{ChatID: entry.ID}, nil
			}
		}
	}

	// 2. Persistent Storage Lookup
	if r.storage != nil {
		if key, val, found, err := r.storage.FindByUsername(ctx, cleaned); err == nil && found {
			if key.Prefix == "channel" && val.AccessHash != 0 {
				if r.cache != nil {
					r.cache.Set("chat", cleaned, "channel", key.ID, val.AccessHash)
				}
				return &tg.InputPeerChannel{ChannelID: key.ID, AccessHash: val.AccessHash}, nil
			}
			if key.Prefix == "chat" {
				if r.cache != nil {
					r.cache.Set("chat", cleaned, "chat", key.ID, 0)
				}
				return &tg.InputPeerChat{ChatID: key.ID}, nil
			}
		}
	}

	// 3. Singleflight Telegram Network Resolve
	if r.api != nil {
		resCh := r.group.DoChan("chat:"+cleaned, func() (any, error) {
			underlyingCtx := r.lifecycleCtx
			if underlyingCtx == nil {
				underlyingCtx = context.Background()
			}
			callCtx, cancel := context.WithTimeout(underlyingCtx, 15*time.Second)
			defer cancel()
			return r.resolveUsernameChat(callCtx, cleaned)
		})

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case res := <-resCh:
			if res.Err != nil {
				if errors.Is(res.Err, core.ErrNotFound) || ClassifyRPCError(res.Err) == RPCInvalidRequest {
					if r.cache != nil {
						r.cache.SetNegative("chat", cleaned)
					}
				}
				return nil, res.Err
			}
			return res.Val.(tg.InputPeerClass), nil
		}
	}

	return nil, fmt.Errorf("%w: chat %q not found", core.ErrNotFound, ref)
}

func (r *Resolver) resolveUsernameChat(ctx context.Context, username string) (tg.InputPeerClass, error) {
	meta := RPCMeta{
		Method: "contacts.resolveUsername",
		Family: "contacts",
		Kind:   RPCReadOnly,
	}

	resolved, err := ExecuteRPC(ctx, r.executor, meta, func(opCtx context.Context) (*tg.ContactsResolvedPeer, error) {
		return r.api.ContactsResolveUsername(opCtx, &tg.ContactsResolveUsernameRequest{Username: username})
	})

	if err != nil {
		return nil, WrapRPCError("resolve chat username", err)
	}
	if r.peerManager != nil && resolved != nil {
		_ = r.peerManager.Apply(ctx, resolved.Users, resolved.Chats)
	}
	for _, entity := range resolved.Chats {
		switch chat := entity.(type) {
		case *tg.Channel:
			if chat.AccessHash == 0 {
				return nil, fmt.Errorf("%w: channel %d returned without access hash", core.ErrAccessHashMissing, chat.ID)
			}
			if r.storage != nil {
				if err := r.storage.Save(ctx, peers.Key{Prefix: "channel", ID: chat.ID}, peers.Value{AccessHash: chat.AccessHash}); err != nil && r.logger != nil {
					r.logger.Warn("failed to persist channel peer to storage", zap.Int64("channel_id", chat.ID), zap.Error(err))
				}
				if err := r.storage.SaveEntity(ctx, "channel", chat.ID, chat.Username, "", "", "", chat.Title); err != nil && r.logger != nil {
					r.logger.Warn("failed to persist channel entity to storage", zap.Int64("channel_id", chat.ID), zap.Error(err))
				}
			}
			if r.cache != nil {
				r.cache.Set("chat", username, "channel", chat.ID, chat.AccessHash)
				r.cache.Set("channel", strconv.FormatInt(chat.ID, 10), "channel", chat.ID, chat.AccessHash)
			}
			return &tg.InputPeerChannel{ChannelID: chat.ID, AccessHash: chat.AccessHash}, nil
		case *tg.Chat:
			if r.storage != nil {
				if err := r.storage.Save(ctx, peers.Key{Prefix: "chat", ID: chat.ID}, peers.Value{AccessHash: 0}); err != nil && r.logger != nil {
					r.logger.Warn("failed to persist chat peer to storage", zap.Int64("chat_id", chat.ID), zap.Error(err))
				}
				if err := r.storage.SaveEntity(ctx, "chat", chat.ID, "", "", "", "", chat.Title); err != nil && r.logger != nil {
					r.logger.Warn("failed to persist chat entity to storage", zap.Int64("chat_id", chat.ID), zap.Error(err))
				}
			}
			if r.cache != nil {
				r.cache.Set("chat", username, "chat", chat.ID, 0)
				r.cache.Set("chat", strconv.FormatInt(chat.ID, 10), "chat", chat.ID, 0)
			}
			return &tg.InputPeerChat{ChatID: chat.ID}, nil
		}
	}
	return nil, fmt.Errorf("%w: username %q returned no chat", core.ErrNotFound, username)
}

// Invalidate removes cached peer entries from memory and persistent storage.
func (r *Resolver) Invalidate(ctx context.Context, peer tg.InputPeerClass) error {
	if peer == nil {
		return nil
	}
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		if r.cache != nil {
			r.cache.InvalidateID(p.UserID)
		}
		if r.storage != nil {
			_ = r.storage.Invalidate(peers.Key{Prefix: "user", ID: p.UserID})
		}
	case *tg.InputPeerChannel:
		if r.cache != nil {
			r.cache.InvalidateID(p.ChannelID)
		}
		if r.storage != nil {
			_ = r.storage.Invalidate(peers.Key{Prefix: "channel", ID: p.ChannelID})
		}
	case *tg.InputPeerChat:
		if r.cache != nil {
			r.cache.InvalidateID(p.ChatID)
		}
		if r.storage != nil {
			_ = r.storage.Invalidate(peers.Key{Prefix: "chat", ID: p.ChatID})
		}
	}
	return nil
}

// InvalidateRef removes cached peer entries by reference (username or string ID).
func (r *Resolver) InvalidateRef(ref string) {
	cleaned := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(ref, "@")))
	if cleaned == "" {
		return
	}
	if r.cache != nil {
		r.cache.Invalidate("user", cleaned)
		r.cache.Invalidate("chat", cleaned)
		r.cache.Invalidate("channel", cleaned)
	}
	if id, err := strconv.ParseInt(strings.TrimPrefix(cleaned, "-100"), 10, 64); err == nil {
		if r.cache != nil {
			r.cache.InvalidateID(id)
		}
		if r.storage != nil {
			_ = r.storage.Invalidate(peers.Key{Prefix: "user", ID: id})
			_ = r.storage.Invalidate(peers.Key{Prefix: "channel", ID: id})
			_ = r.storage.Invalidate(peers.Key{Prefix: "chat", ID: id})
		}
	}
}

// Len returns the number of active entries in the resolver memory cache.
func (r *Resolver) Len() int {
	if r != nil && r.cache != nil {
		return r.cache.Len()
	}
	return 0
}
