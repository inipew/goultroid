package client

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/peer"
)

type corePeerResolverAPIStub struct {
	result *tg.ContactsResolvedPeer
	err    error
	calls  int
}

func (a *corePeerResolverAPIStub) ContactsResolveUsername(
	context.Context,
	*tg.ContactsResolveUsernameRequest,
) (*tg.ContactsResolvedPeer, error) {
	a.calls++
	return a.result, a.err
}

func TestP7GCorePeerResolverNumericUserUsesExistingAssistantCache(t *testing.T) {
	cache := peer.NewMemoryCache()
	cache.Put(peer.PeerRecord{
		ID:         42,
		Kind:       peer.PeerKindUser,
		AccessHash: 4242,
	})
	api := &corePeerResolverAPIStub{}
	resolver := newAssistantCorePeerResolver(api, peer.NewResolver(cache))

	got, id, err := resolver.ResolveUser(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	user, ok := got.(*tg.InputPeerUser)
	if !ok || user.UserID != 42 || user.AccessHash != 4242 || id != 42 {
		t.Fatalf("resolved numeric user=%#v id=%d", got, id)
	}
	if api.calls != 0 {
		t.Fatalf("numeric cache hit issued %d username RPCs", api.calls)
	}
}

func TestP7GCorePeerResolverUsernameUsesExactResolvedPeer(t *testing.T) {
	api := &corePeerResolverAPIStub{
		result: &tg.ContactsResolvedPeer{
			Peer: &tg.PeerUser{UserID: 42},
			Users: []tg.UserClass{
				&tg.User{ID: 99, AccessHash: 9900, Username: "other"},
				&tg.User{ID: 42, AccessHash: 4200, Username: "target"},
			},
		},
	}
	cache := peer.NewMemoryCache()
	resolver := newAssistantCorePeerResolver(api, peer.NewResolver(cache))

	got, id, err := resolver.ResolveUser(context.Background(), "@target")
	if err != nil {
		t.Fatal(err)
	}
	user, ok := got.(*tg.InputPeerUser)
	if !ok || user.UserID != 42 || user.AccessHash != 4200 || id != 42 {
		t.Fatalf("resolved username user=%#v id=%d", got, id)
	}
	if api.calls != 1 {
		t.Fatalf("username resolve RPCs=%d, want 1", api.calls)
	}
	if record, ok := cache.Get(peer.PeerKindUser, 42); !ok || record.AccessHash != 4200 {
		t.Fatalf("resolved username was not fed into shared peer cache: %+v ok=%v", record, ok)
	}
}

func TestP7GCorePeerResolverUsernameChatUsesExactResolvedPeer(t *testing.T) {
	api := &corePeerResolverAPIStub{
		result: &tg.ContactsResolvedPeer{
			Peer: &tg.PeerChannel{ChannelID: 77},
			Chats: []tg.ChatClass{
				&tg.Channel{ID: 88, AccessHash: 8800, Username: "other"},
				&tg.Channel{ID: 77, AccessHash: 7700, Username: "targetgroup"},
			},
		},
	}
	resolver := newAssistantCorePeerResolver(api, peer.NewResolver(peer.NewMemoryCache()))

	got, err := resolver.ResolveChat(context.Background(), "@targetgroup")
	if err != nil {
		t.Fatal(err)
	}
	channel, ok := got.(*tg.InputPeerChannel)
	if !ok || channel.ChannelID != 77 || channel.AccessHash != 7700 {
		t.Fatalf("resolved username chat=%#v", got)
	}
}
