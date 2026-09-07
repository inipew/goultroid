package peer

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
)

// Resolver resolves Telegram generic Peer objects into MTProto InputPeerClass instances.
type Resolver interface {
	Resolve(ctx context.Context, peer tg.PeerClass, senderID int64, entities tg.Entities) (tg.InputPeerClass, error)
	ReResolve(ctx context.Context, inputPeer tg.InputPeerClass) (tg.InputPeerClass, error)
	InvalidatePeer(inputPeer tg.InputPeerClass)
	Cache() Cache
}

// EntityFetcher retrieves entity details to recover stale access hashes on cache miss.
type EntityFetcher interface {
	FetchUser(ctx context.Context, id int64) (*tg.User, error)
	FetchChannel(ctx context.Context, id int64) (*tg.Channel, error)
}

// DefaultResolver resolves peers using an in-memory cache and entity lookup.
type DefaultResolver struct {
	cache   Cache
	fetcher EntityFetcher
}

var _ Resolver = (*DefaultResolver)(nil)

// NewResolver creates a new peer resolver backed by the provided cache.
func NewResolver(cache Cache) *DefaultResolver {
	if cache == nil {
		cache = NewMemoryCache()
	}
	return &DefaultResolver{cache: cache}
}

// SetEntityFetcher attaches an entity fetcher for network-level access hash recovery.
func (r *DefaultResolver) SetEntityFetcher(fetcher EntityFetcher) {
	r.fetcher = fetcher
}

// Cache returns the underlying peer cache.
func (r *DefaultResolver) Cache() Cache {
	return r.cache
}

// InvalidatePeer removes the cached record corresponding to an InputPeer.
func (r *DefaultResolver) InvalidatePeer(inputPeer tg.InputPeerClass) {
	if r.cache == nil || inputPeer == nil {
		return
	}
	switch p := inputPeer.(type) {
	case *tg.InputPeerUser:
		r.cache.Invalidate(PeerKindUser, p.UserID)
	case *tg.InputPeerChannel:
		r.cache.Invalidate(PeerKindChannel, p.ChannelID)
	}
}

// ReResolve attempts to refresh or re-fetch cached authorization for a previously resolved peer.
func (r *DefaultResolver) ReResolve(ctx context.Context, inputPeer tg.InputPeerClass) (tg.InputPeerClass, error) {
	if inputPeer == nil {
		return nil, ErrPeerResolution
	}
	switch p := inputPeer.(type) {
	case *tg.InputPeerUser:
		if r.cache != nil {
			if rec, ok := r.cache.Get(PeerKindUser, p.UserID); ok && rec.AccessHash != 0 {
				return &tg.InputPeerUser{UserID: p.UserID, AccessHash: rec.AccessHash}, nil
			}
		}
		if r.fetcher != nil {
			if u, err := r.fetcher.FetchUser(ctx, p.UserID); err == nil && u != nil && u.AccessHash != 0 {
				if r.cache != nil {
					r.cache.Put(PeerRecord{ID: p.UserID, Kind: PeerKindUser, AccessHash: u.AccessHash})
				}
				return &tg.InputPeerUser{UserID: p.UserID, AccessHash: u.AccessHash}, nil
			}
		}
		return nil, fmt.Errorf("%w: user %d", ErrAccessHashMissing, p.UserID)
	case *tg.InputPeerChannel:
		if r.cache != nil {
			if rec, ok := r.cache.Get(PeerKindChannel, p.ChannelID); ok && rec.AccessHash != 0 {
				return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: rec.AccessHash}, nil
			}
		}
		if r.fetcher != nil {
			if ch, err := r.fetcher.FetchChannel(ctx, p.ChannelID); err == nil && ch != nil && ch.AccessHash != 0 {
				if r.cache != nil {
					r.cache.Put(PeerRecord{ID: p.ChannelID, Kind: PeerKindChannel, AccessHash: ch.AccessHash})
				}
				return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: ch.AccessHash}, nil
			}
		}
		return nil, fmt.Errorf("%w: channel %d", ErrAccessHashMissing, p.ChannelID)
	case *tg.InputPeerChat:
		return p, nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnsupportedPeer, inputPeer)
	}
}

// Resolve deterministically resolves a peer into an InputPeer.
func (r *DefaultResolver) Resolve(ctx context.Context, peer tg.PeerClass, senderID int64, entities tg.Entities) (tg.InputPeerClass, error) {
	// 1. Populate cache from provided entities
	if r.cache != nil {
		r.cache.CacheEntities(entities)
	}

	// 2. If peer is nil, fallback to senderID if present
	if peer == nil {
		if senderID != 0 {
			if rec, ok := r.cache.Get(PeerKindUser, senderID); ok && rec.AccessHash != 0 {
				return &tg.InputPeerUser{UserID: senderID, AccessHash: rec.AccessHash}, nil
			}
		}
		return nil, fmt.Errorf("%w: nil peer and missing sender coordinates", ErrPeerResolution)
	}

	// 3. Resolve according to explicit peer variant
	switch p := peer.(type) {
	case *tg.PeerUser:
		var accessHash int64
		if u, ok := entities.Users[p.UserID]; ok && u != nil && u.AccessHash != 0 {
			accessHash = u.AccessHash
			r.cache.Put(PeerRecord{ID: p.UserID, Kind: PeerKindUser, AccessHash: accessHash})
		} else if rec, ok := r.cache.Get(PeerKindUser, p.UserID); ok {
			accessHash = rec.AccessHash
		}

		if accessHash == 0 && senderID != 0 && senderID == p.UserID {
			if rec, ok := r.cache.Get(PeerKindUser, senderID); ok {
				accessHash = rec.AccessHash
			}
		}

		if accessHash == 0 {
			return nil, fmt.Errorf("%w: missing access hash for user %d", ErrAccessHashMissing, p.UserID)
		}
		return &tg.InputPeerUser{UserID: p.UserID, AccessHash: accessHash}, nil

	case *tg.PeerChat:
		// Basic groups in MTProto only require ChatID
		return &tg.InputPeerChat{ChatID: p.ChatID}, nil

	case *tg.PeerChannel:
		var accessHash int64
		if ch, ok := entities.Channels[p.ChannelID]; ok && ch != nil && ch.AccessHash != 0 {
			accessHash = ch.AccessHash
			r.cache.Put(PeerRecord{ID: p.ChannelID, Kind: PeerKindChannel, AccessHash: accessHash})
		} else if rec, ok := r.cache.Get(PeerKindChannel, p.ChannelID); ok {
			accessHash = rec.AccessHash
		}

		if accessHash == 0 {
			return nil, fmt.Errorf("%w: missing access hash for channel %d", ErrAccessHashMissing, p.ChannelID)
		}
		return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: accessHash}, nil

	default:
		return nil, fmt.Errorf("%w: %T", ErrUnsupportedPeer, peer)
	}
}
