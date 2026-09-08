package core

import (
	"context"

	"github.com/gotd/td/tg"
)

func (k PeerKind) String() string {
	switch k {
	case PeerKindUser:
		return "user"
	case PeerKindChat:
		return "chat"
	case PeerKindChannel:
		return "channel"
	default:
		return "unknown"
	}
}

// PeerResolver abstracts the resolution of user and chat references
// (numeric ID, @username, phone number, etc.) into valid MTProto InputPeerClass instances
// with guaranteed access hashes.
type PeerResolver interface {
	Resolve(ctx context.Context, ref string) (tg.InputPeerClass, error)
	ResolveUser(ctx context.Context, ref string) (tg.InputPeerClass, int64, error)
	ResolveChat(ctx context.Context, ref string) (tg.InputPeerClass, error)
}

// MockPeerResolver provides a stub implementation for unit tests.
type MockPeerResolver struct {
	UserPeer tg.InputPeerClass
	UserID   int64
	UserErr  error
	ChatPeer tg.InputPeerClass
	ChatErr  error
}

// Resolve implements PeerResolver for unit tests.
func (m *MockPeerResolver) Resolve(ctx context.Context, ref string) (tg.InputPeerClass, error) {
	if m.UserErr != nil {
		return nil, m.UserErr
	}
	if m.ChatErr != nil {
		return nil, m.ChatErr
	}
	if m.UserPeer != nil {
		return m.UserPeer, nil
	}
	if m.ChatPeer != nil {
		return m.ChatPeer, nil
	}
	return &tg.InputPeerUser{UserID: m.UserID, AccessHash: 12345}, nil
}

// ResolveUser implements PeerResolver for unit tests.
func (m *MockPeerResolver) ResolveUser(ctx context.Context, ref string) (tg.InputPeerClass, int64, error) {
	if m.UserErr != nil {
		return nil, 0, m.UserErr
	}
	if m.UserPeer != nil {
		return m.UserPeer, m.UserID, nil
	}
	return &tg.InputPeerUser{UserID: m.UserID, AccessHash: 12345}, m.UserID, nil
}

// ResolveChat implements PeerResolver for unit tests.
func (m *MockPeerResolver) ResolveChat(ctx context.Context, ref string) (tg.InputPeerClass, error) {
	if m.ChatErr != nil {
		return nil, m.ChatErr
	}
	if m.ChatPeer != nil {
		return m.ChatPeer, nil
	}
	return &tg.InputPeerChat{ChatID: 100}, nil
}
