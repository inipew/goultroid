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
	networkSlots    chan struct{}
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
}

var _ core.PeerResolver = (*Resolver)(nil)

// NewResolverWithContext initializes a peer resolver whose underlying shared
// network work is owned by parent. Callers waiting on the same singleflight may
// cancel independently, while parent cancellation terminates the shared RPC.
func NewResolverWithContext(parent context.Context, api *tg.Client, peerManager *peers.Manager, cfg ResolverCacheConfig) *Resolver {
	if parent == nil {
		parent = context.Background()
	}
	if cfg.MaxConcurrentNetwork <= 0 {
		cfg.MaxConcurrentNetwork = defaultResolverCacheConfig().MaxConcurrentNetwork
	}
	ctx, cancel := context.WithCancel(parent)
	executor, _ := NewRPCExecutor(RPCExecutorConfig{DefaultPolicy: defaultExecutorPolicy()})
	return &Resolver{
		api:             api,
		peerManager:     peerManager,
		cache:           NewPeerCache(cfg),
		executor:        executor,
		networkSlots:    make(chan struct{}, cfg.MaxConcurrentNetwork),
		lifecycleCtx:    ctx,
		lifecycleCancel: cancel,
	}
}

// NewResolverWithConfig initializes a standalone peer resolver with custom
// cache configuration. Production wiring should prefer NewResolverWithContext.
func NewResolverWithConfig(api *tg.Client, peerManager *peers.Manager, cfg ResolverCacheConfig) *Resolver {
	return NewResolverWithContext(context.Background(), api, peerManager, cfg)
}

func NewResolver(api *tg.Client, peerManager *peers.Manager) *Resolver {
	return NewResolverWithConfig(api, peerManager, defaultResolverCacheConfig())
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

func (r *Resolver) acquireNetwork(ctx context.Context) (func(), error) {
	if r == nil {
		return nil, errors.New("resolver is nil")
	}
	if ctx == nil {
		ctx = r.lifecycleCtx
	}
	if ctx == nil {
		return nil, errors.New("resolver lifecycle context is unavailable")
	}
	if r.networkSlots == nil {
		return func() {}, nil
	}
	select {
	case r.networkSlots <- struct{}{}:
		return func() { <-r.networkSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
		if r.storage != nil {
			val, found, findErr := r.storage.Find(ctx, peers.Key{Prefix: "user", ID: uid})
			if findErr != nil {
				return nil, 0, fmt.Errorf("resolve numeric user from storage: %w", findErr)
			}
			if found && val.AccessHash != 0 {
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
		key, val, found, findErr := r.storage.FindByUsername(ctx, cleaned)
		if findErr != nil {
			return nil, 0, fmt.Errorf("resolve user %q from storage: %w", cleaned, findErr)
		}
		if found && val.AccessHash != 0 && key.Prefix == "user" {
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
				return nil, errors.New("resolver lifecycle context is unavailable")
			}
			release, err := r.acquireNetwork(underlyingCtx)
			if err != nil {
				return nil, err
			}
			defer release()
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
				if parsed, parseErr := strconv.ParseInt(str[4:], 10, 64); parseErr == nil {
					channelID = parsed
				}
				chanStr := strconv.FormatInt(channelID, 10)
				if r.cache != nil {
					if entry, hit := r.cache.Get("channel", chanStr); hit && entry.AccessHash != 0 {
						return &tg.InputPeerChannel{ChannelID: channelID, AccessHash: entry.AccessHash}, nil
					}
				}
				if r.storage != nil {
					val, found, findErr := r.storage.Find(ctx, peers.Key{Prefix: "channel", ID: channelID})
					if findErr != nil {
						return nil, fmt.Errorf("resolve numeric channel from storage: %w", findErr)
					}
					if found && val.AccessHash != 0 {
						if r.cache != nil {
							r.cache.Set("channel", chanStr, "channel", channelID, val.AccessHash)
						}
						return &tg.InputPeerChannel{ChannelID: channelID, AccessHash: val.AccessHash}, nil
					}
					return nil, fmt.Errorf("%w: channel %d has no cached access hash", core.ErrAccessHashMissing, channelID)
				}
				return nil, fmt.Errorf("%w: channel %d requires a usable access hash", core.ErrAccessHashMissing, channelID)
			}
			return &tg.InputPeerChat{ChatID: -id}, nil
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
		key, val, found, findErr := r.storage.FindByUsername(ctx, cleaned)
		if findErr != nil {
			return nil, fmt.Errorf("resolve chat %q from storage: %w", cleaned, findErr)
		}
		if found {
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
				return nil, errors.New("resolver lifecycle context is unavailable")
			}
			release, err := r.acquireNetwork(underlyingCtx)
			if err != nil {
				return nil, err
			}
			defer release()
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

// RefreshPeer invalidates a stale user/channel access hash and forces exactly one
// fresh username resolve through the resolver's shared RPCExecutor. Basic chats
// do not carry access hashes and therefore need no refresh.
func (r *Resolver) RefreshPeer(ctx context.Context, peer tg.InputPeerClass) error {
	if peer == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("peer refresh context is nil")
	}
	var prefix string
	var id int64
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		prefix, id = "user", p.UserID
	case *tg.InputPeerChannel:
		prefix, id = "channel", p.ChannelID
	case *tg.InputPeerChat, *tg.InputPeerSelf:
		return nil
	default:
		return fmt.Errorf("%w: unsupported peer refresh type %T", core.ErrUnsupported, peer)
	}
	if id == 0 || r.storage == nil {
		return fmt.Errorf("%w: %s %d has no persistent metadata", core.ErrAccessHashMissing, prefix, id)
	}

	username, found, err := r.storage.FindUsernameByID(ctx, prefix, id)
	if err != nil {
		return fmt.Errorf("find refresh username for %s %d: %w", prefix, id, err)
	}
	if !found || username == "" {
		return fmt.Errorf("%w: %s %d has no persisted username for refresh", core.ErrAccessHashMissing, prefix, id)
	}

	if r.api == nil {
		return fmt.Errorf("%w: telegram api unavailable for peer refresh", core.ErrUnavailable)
	}
	if r.cache != nil {
		r.cache.InvalidateID(id)
		r.cache.Invalidate("user", username)
		r.cache.Invalidate("chat", username)
		r.cache.Invalidate("channel", username)
	}
	if err := r.storage.InvalidateContext(ctx, peers.Key{Prefix: prefix, ID: id}); err != nil {
		return err
	}

	key := "refresh:" + prefix + ":" + username
	resCh := r.group.DoChan(key, func() (any, error) {
		underlyingCtx := r.lifecycleCtx
		if underlyingCtx == nil {
			return nil, errors.New("resolver lifecycle context is unavailable")
		}
		release, acquireErr := r.acquireNetwork(underlyingCtx)
		if acquireErr != nil {
			return nil, acquireErr
		}
		defer release()
		callCtx, cancel := context.WithTimeout(underlyingCtx, 15*time.Second)
		defer cancel()
		if prefix == "user" {
			_, _, refreshErr := r.resolveUsernameUser(callCtx, username)
			return nil, refreshErr
		}
		_, refreshErr := r.resolveUsernameChat(callCtx, username)
		return nil, refreshErr
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-resCh:
		return res.Err
	}
}

// Invalidate removes cached peer entries from memory and persistent storage.
func (r *Resolver) Invalidate(ctx context.Context, peer tg.InputPeerClass) error {
	if peer == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("peer invalidation context is nil")
	}
	var key peers.Key
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		key = peers.Key{Prefix: "user", ID: p.UserID}
	case *tg.InputPeerChannel:
		key = peers.Key{Prefix: "channel", ID: p.ChannelID}
	case *tg.InputPeerChat:
		key = peers.Key{Prefix: "chat", ID: p.ChatID}
	default:
		return nil
	}
	if r.cache != nil {
		r.cache.InvalidateID(key.ID)
	}
	if r.storage != nil {
		return r.storage.InvalidateContext(ctx, key)
	}
	return nil
}

// InvalidateRefContext removes memory entries and the exact persistent access
// hash associated with a username or numeric reference while retaining entity
// metadata needed for a forced network refresh.
func (r *Resolver) InvalidateRefContext(ctx context.Context, ref string) error {
	if ctx == nil {
		return errors.New("peer invalidation context is nil")
	}
	cleaned := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(ref, "@")))
	if cleaned == "" {
		return nil
	}
	if r.cache != nil {
		r.cache.Invalidate("user", cleaned)
		r.cache.Invalidate("chat", cleaned)
		r.cache.Invalidate("channel", cleaned)
	}

	if id, parseErr := strconv.ParseInt(strings.TrimPrefix(cleaned, "-100"), 10, 64); parseErr == nil {
		if r.cache != nil {
			r.cache.InvalidateID(id)
		}
		if r.storage != nil {
			for _, prefix := range []string{"user", "channel", "chat"} {
				if err := r.storage.InvalidateContext(ctx, peers.Key{Prefix: prefix, ID: id}); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if r.storage == nil {
		return nil
	}
	key, _, found, err := r.storage.FindByUsername(ctx, cleaned)
	if err != nil {
		return fmt.Errorf("find peer %q for invalidation: %w", cleaned, err)
	}
	if !found {
		return nil
	}
	if r.cache != nil {
		r.cache.InvalidateID(key.ID)
	}
	return r.storage.InvalidateContext(ctx, key)
}

// InvalidateRef is retained for compatibility with older callers. Runtime hot
// paths should use InvalidateRefContext so cancellation and shutdown propagate.
func (r *Resolver) InvalidateRef(ref string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.InvalidateRefContext(ctx, ref)
}

// Len returns the number of active entries in the resolver memory cache.
func (r *Resolver) Len() int {
	if r != nil && r.cache != nil {
		return r.cache.Len()
	}
	return 0
}
