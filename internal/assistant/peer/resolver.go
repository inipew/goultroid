package peer

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
)

// Resolver resolves Telegram generic Peer objects into MTProto InputPeerClass instances.
type Resolver interface {
	Resolve(ctx context.Context, peer tg.PeerClass, senderID int64, entities tg.Entities) (tg.InputPeerClass, error)
	InvalidatePeer(inputPeer tg.InputPeerClass)
	Cache() Cache
}

// DefaultResolver resolves peers using an in-memory cache and entity lookup.
type DefaultResolver struct {
	cache Cache
}

var _ Resolver = (*DefaultResolver)(nil)

// NewResolver creates a new peer resolver backed by the provided cache.
func NewResolver(cache Cache) *DefaultResolver {
	if cache == nil {
		cache = NewMemoryCache()
	}
	return &DefaultResolver{cache: cache}
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
		return nil, fmt.Errorf("%w: nil peer and missing sender coordinates", interaction.ErrPeerResolution)
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
			return nil, fmt.Errorf("%w: missing access hash for user %d", interaction.ErrPeerResolution, p.UserID)
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
			return nil, fmt.Errorf("%w: missing access hash for channel %d", interaction.ErrPeerResolution, p.ChannelID)
		}
		return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: accessHash}, nil

	default:
		return nil, fmt.Errorf("%w: unsupported peer type %T", interaction.ErrPeerResolution, peer)
	}
}
