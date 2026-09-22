package client

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type p7iChatAPIStub struct {
	calls  int
	result tg.MessagesChatsClass
	err    error
}

func (s *p7iChatAPIStub) ChannelsGetChannels(
	context.Context,
	[]tg.InputChannelClass,
) (tg.MessagesChatsClass, error) {
	s.calls++
	return s.result, s.err
}

func TestP7IManagedChatClassifierUsesEntityWithoutRPC(t *testing.T) {
	api := &p7iChatAPIStub{}
	classifier := newManagedGroupRuleChatClassifier(api)
	chat, err := classifier.Classify(
		context.Background(),
		&tg.Message{PeerID: &tg.PeerChannel{ChannelID: 88}},
		tg.Entities{Channels: map[int64]*tg.Channel{
			88: {ID: 88, AccessHash: 188, Megagroup: true, Title: "Super"},
		}},
		&tg.InputPeerChannel{ChannelID: 88, AccessHash: 188},
	)
	if err != nil {
		t.Fatal(err)
	}
	if chat.Kind() != core.ChatKindSupergroup || chat.ID != 88 || api.calls != 0 {
		t.Fatalf("chat=%+v api.calls=%d", chat, api.calls)
	}
}

func TestP7IManagedChatClassifierFetchesUnknownSupergroupAfterAdmission(t *testing.T) {
	api := &p7iChatAPIStub{result: &tg.MessagesChats{Chats: []tg.ChatClass{
		&tg.Channel{ID: 88, AccessHash: 188, Megagroup: true, Title: "Recovered"},
	}}}
	classifier := newManagedGroupRuleChatClassifier(api)
	chat, err := classifier.Classify(
		context.Background(),
		&tg.Message{PeerID: &tg.PeerChannel{ChannelID: 88}},
		tg.Entities{},
		&tg.InputPeerChannel{ChannelID: 88, AccessHash: 188},
	)
	if err != nil {
		t.Fatal(err)
	}
	if api.calls != 1 || chat.Kind() != core.ChatKindSupergroup ||
		chat.ID != 88 || chat.Title != "Recovered" {
		t.Fatalf("api.calls=%d chat=%+v", api.calls, chat)
	}
}

func TestP7IManagedChatClassifierRejectsUnknownBroadcastAfterFetch(t *testing.T) {
	api := &p7iChatAPIStub{result: &tg.MessagesChats{Chats: []tg.ChatClass{
		&tg.Channel{ID: 88, AccessHash: 188, Megagroup: false, Title: "Broadcast"},
	}}}
	classifier := newManagedGroupRuleChatClassifier(api)
	_, err := classifier.Classify(
		context.Background(),
		&tg.Message{PeerID: &tg.PeerChannel{ChannelID: 88}},
		tg.Entities{},
		&tg.InputPeerChannel{ChannelID: 88, AccessHash: 188},
	)
	if !errors.Is(err, core.ErrGroupOnly) {
		t.Fatalf("broadcast classify error=%v, want ErrGroupOnly", err)
	}
	if api.calls != 1 {
		t.Fatalf("api.calls=%d, want 1", api.calls)
	}
}

func TestP7IManagedChatClassifierRejectsKnownBroadcastWithoutRPC(t *testing.T) {
	api := &p7iChatAPIStub{}
	classifier := newManagedGroupRuleChatClassifier(api)
	_, err := classifier.Classify(
		context.Background(),
		&tg.Message{PeerID: &tg.PeerChannel{ChannelID: 88}},
		tg.Entities{Channels: map[int64]*tg.Channel{
			88: {ID: 88, AccessHash: 188, Megagroup: false},
		}},
		&tg.InputPeerChannel{ChannelID: 88, AccessHash: 188},
	)
	if !errors.Is(err, core.ErrGroupOnly) {
		t.Fatalf("broadcast classify error=%v, want ErrGroupOnly", err)
	}
	if api.calls != 0 {
		t.Fatalf("known broadcast used RPC %d times", api.calls)
	}
}
