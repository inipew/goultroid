package client

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/core"
)

type groupQueryAPIStub struct {
	fullChatCalls    int
	fullChannelCalls int
	lastChatID       int64
	lastChannel      tg.InputChannelClass
	result           *tg.MessagesChatFull
	err              error
}

func (a *groupQueryAPIStub) MessagesGetFullChat(_ context.Context, chatID int64) (*tg.MessagesChatFull, error) {
	a.fullChatCalls++
	a.lastChatID = chatID
	return a.result, a.err
}

func (a *groupQueryAPIStub) ChannelsGetFullChannel(_ context.Context, channel tg.InputChannelClass) (*tg.MessagesChatFull, error) {
	a.fullChannelCalls++
	a.lastChannel = channel
	return a.result, a.err
}

type groupQueryResolverStub struct {
	refreshed tg.InputPeerClass
	err       error
	calls     int
}

func (r *groupQueryResolverStub) Resolve(context.Context, tg.PeerClass, int64, tg.Entities) (tg.InputPeerClass, error) {
	return nil, errors.New("unexpected Resolve call")
}

func (r *groupQueryResolverStub) ReResolve(_ context.Context, input tg.InputPeerClass) (tg.InputPeerClass, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	if r.refreshed != nil {
		return r.refreshed, nil
	}
	return input, nil
}

func (*groupQueryResolverStub) InvalidatePeer(tg.InputPeerClass) {}
func (*groupQueryResolverStub) Cache() peer.Cache                { return nil }

func TestManagedGroupQueryBasicGroupUsesManagedFullChat(t *testing.T) {
	want := &tg.MessagesChatFull{FullChat: &tg.ChatFull{ID: 55}}
	api := &groupQueryAPIStub{result: want}
	query := newManagedGroupQuery(api, &groupQueryResolverStub{})

	got, err := query.GetFullChat(context.Background(), &tg.InputPeerChat{ChatID: 55})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("result=%p want=%p", got, want)
	}
	if api.fullChatCalls != 1 || api.fullChannelCalls != 0 || api.lastChatID != 55 {
		t.Fatalf("unexpected RPC lane: chat=%d channel=%d chat_id=%d", api.fullChatCalls, api.fullChannelCalls, api.lastChatID)
	}
}

func TestManagedGroupQuerySupergroupUsesManagedFullChannel(t *testing.T) {
	want := &tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: 99}}
	api := &groupQueryAPIStub{result: want}
	resolver := &groupQueryResolverStub{}
	query := newManagedGroupQuery(api, resolver)

	got, err := query.GetFullChat(context.Background(), &tg.InputPeerChannel{ChannelID: 99, AccessHash: 1234})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("result=%p want=%p", got, want)
	}
	if api.fullChannelCalls != 1 || api.fullChatCalls != 0 {
		t.Fatalf("unexpected RPC lane: channel=%d chat=%d", api.fullChannelCalls, api.fullChatCalls)
	}
	channel, ok := api.lastChannel.(*tg.InputChannel)
	if !ok || channel.ChannelID != 99 || channel.AccessHash != 1234 {
		t.Fatalf("unexpected channel query: %#v", api.lastChannel)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver calls=%d, want 0 with valid access hash", resolver.calls)
	}
}

func TestManagedGroupQueryRecoversMissingSupergroupAccessHash(t *testing.T) {
	api := &groupQueryAPIStub{result: &tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: 99}}}
	resolver := &groupQueryResolverStub{
		refreshed: &tg.InputPeerChannel{ChannelID: 99, AccessHash: 777},
	}
	query := newManagedGroupQuery(api, resolver)

	if _, err := query.GetFullChat(context.Background(), &tg.InputPeerChannel{ChannelID: 99}); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls=%d, want 1", resolver.calls)
	}
	channel, ok := api.lastChannel.(*tg.InputChannel)
	if !ok || channel.AccessHash != 777 {
		t.Fatalf("recovered channel=%#v", api.lastChannel)
	}
}

func TestManagedGroupQueryFailsClosedForUnsupportedPeer(t *testing.T) {
	query := newManagedGroupQuery(&groupQueryAPIStub{}, &groupQueryResolverStub{})
	_, err := query.GetFullChat(context.Background(), &tg.InputPeerUser{UserID: 7, AccessHash: 8})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("error=%v, want ErrUnsupported", err)
	}
}
