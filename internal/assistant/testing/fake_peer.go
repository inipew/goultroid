package testing

import (
	"context"
	"sync"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/peer"
)

// FakePeerResolver provides a mock for resolving and refreshing peers.
type FakePeerResolver struct {
	mu             sync.RWMutex
	cache          peer.Cache
	ResolveCalls   int
	ReResolveCalls int
	Invalidated    []tg.InputPeerClass

	ResolvedPeer   tg.InputPeerClass
	ReResolvedPeer tg.InputPeerClass
	ResolveErr     error
	ReResolveErr   error
}

var _ peer.Resolver = (*FakePeerResolver)(nil)
var _ interaction.PeerReResolver = (*FakePeerResolver)(nil)

// NewFakePeerResolver creates an initialized FakePeerResolver.
func NewFakePeerResolver() *FakePeerResolver {
	return &FakePeerResolver{
		cache:       peer.NewMemoryCache(),
		Invalidated: make([]tg.InputPeerClass, 0),
	}
}

func (f *FakePeerResolver) Cache() peer.Cache {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.cache
}

func (f *FakePeerResolver) Resolve(ctx context.Context, p tg.PeerClass, fallbackUserID int64, e tg.Entities) (tg.InputPeerClass, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ResolveCalls++
	if f.ResolveErr != nil {
		return nil, f.ResolveErr
	}
	if f.ResolvedPeer != nil {
		return f.ResolvedPeer, nil
	}
	return &tg.InputPeerUser{UserID: fallbackUserID, AccessHash: 12345}, nil
}

func (f *FakePeerResolver) InvalidatePeer(p tg.InputPeerClass) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Invalidated = append(f.Invalidated, p)
}

func (f *FakePeerResolver) ReResolve(ctx context.Context, inputPeer tg.InputPeerClass) (tg.InputPeerClass, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ReResolveCalls++
	if f.ReResolveErr != nil {
		return nil, f.ReResolveErr
	}
	if f.ReResolvedPeer != nil {
		return f.ReResolvedPeer, nil
	}
	return inputPeer, nil
}
