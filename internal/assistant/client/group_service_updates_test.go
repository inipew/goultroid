package client

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type groupServiceIngressStub struct {
	interested       bool
	interestedCalls  int
	published        []*core.GroupServiceEvent
	lastInterestedID int64
	lastKind         core.GroupServiceKind
}

func (s *groupServiceIngressStub) Interested(chatID int64, kind core.GroupServiceKind) bool {
	s.interestedCalls++
	s.lastInterestedID = chatID
	s.lastKind = kind
	return s.interested
}

func (s *groupServiceIngressStub) Publish(event *core.GroupServiceEvent) {
	s.published = append(s.published, event)
}

func TestP7HInactiveChatStopsBeforePublish(t *testing.T) {
	ingress := &groupServiceIngressStub{}
	message := &tg.MessageService{
		ID:     10,
		PeerID: &tg.PeerChat{ChatID: 77},
		FromID: &tg.PeerUser{UserID: 7},
		Action: &tg.MessageActionChatAddUser{Users: []int64{42}},
	}
	entities := tg.Entities{
		Chats: map[int64]*tg.Chat{77: {ID: 77, Title: "Group"}},
		Users: map[int64]*tg.User{42: {ID: 42, FirstName: "Alice"}},
	}

	handleAssistantGroupService(message, entities, UpdateHandlerDeps{
		GroupEvents: ingress,
		SelfID:      func() int64 { return 999 },
	})

	if ingress.interestedCalls != 1 || ingress.lastInterestedID != 77 ||
		ingress.lastKind != core.GroupServiceMemberJoined {
		t.Fatalf("interest calls=%d chat=%d kind=%s",
			ingress.interestedCalls, ingress.lastInterestedID, ingress.lastKind)
	}
	if len(ingress.published) != 0 {
		t.Fatalf("inactive chat published %d events", len(ingress.published))
	}
}

func TestP7HActiveJoinDeduplicatesUsersAndExcludesAssistant(t *testing.T) {
	ingress := &groupServiceIngressStub{interested: true}
	message := &tg.MessageService{
		ID:     11,
		Date:   1234,
		PeerID: &tg.PeerChat{ChatID: 77},
		FromID: &tg.PeerUser{UserID: 7},
		Action: &tg.MessageActionChatAddUser{Users: []int64{42, 42, 999, 43}},
	}
	entities := tg.Entities{
		Chats: map[int64]*tg.Chat{77: {ID: 77, Title: "Group"}},
		Users: map[int64]*tg.User{
			42: {ID: 42, FirstName: "Alice"},
			43: {ID: 43, FirstName: "Bob", Username: "bob"},
		},
	}

	handleAssistantGroupService(message, entities, UpdateHandlerDeps{
		GroupEvents: ingress,
		SelfID:      func() int64 { return 999 },
	})

	if len(ingress.published) != 1 {
		t.Fatalf("published events=%d, want 1", len(ingress.published))
	}
	event := ingress.published[0]
	if event.ChatID != 77 || event.Kind != core.GroupServiceMemberJoined ||
		event.MessageID != 11 || event.ActorID != 7 || event.ChatTitle != "Group" {
		t.Fatalf("event=%+v", event)
	}
	if len(event.Users) != 2 || event.Users[0].ID != 42 || event.Users[1].ID != 43 {
		t.Fatalf("users=%+v, want [42 43]", event.Users)
	}
	if _, ok := event.Peer.(*tg.InputPeerChat); !ok {
		t.Fatalf("peer=%T, want InputPeerChat", event.Peer)
	}
}

func TestP7HSupergroupServiceMessageUsesChannelIngressShape(t *testing.T) {
	ingress := &groupServiceIngressStub{interested: true}
	message := &tg.MessageService{
		ID:     12,
		PeerID: &tg.PeerChannel{ChannelID: 88},
		FromID: &tg.PeerUser{UserID: 42},
		Action: &tg.MessageActionChatJoinedByRequest{},
	}
	entities := tg.Entities{
		Channels: map[int64]*tg.Channel{
			88: {ID: 88, AccessHash: 188, Megagroup: true, Title: "Super"},
		},
		Users: map[int64]*tg.User{
			42: {ID: 42, FirstName: "Alice"},
		},
	}

	handleAssistantGroupService(message, entities, UpdateHandlerDeps{
		GroupEvents: ingress,
		SelfID:      func() int64 { return 999 },
	})

	if len(ingress.published) != 1 {
		t.Fatalf("published events=%d, want 1", len(ingress.published))
	}
	event := ingress.published[0]
	peer, ok := event.Peer.(*tg.InputPeerChannel)
	if !ok || peer.ChannelID != 88 || peer.AccessHash != 188 {
		t.Fatalf("peer=%#v, want resolved supergroup peer", event.Peer)
	}
	if event.Kind != core.GroupServiceMemberJoined || len(event.Users) != 1 ||
		event.Users[0].ID != 42 {
		t.Fatalf("event=%+v", event)
	}
}

func TestP7HBroadcastServiceMessageFailsClosedBeforeInterest(t *testing.T) {
	ingress := &groupServiceIngressStub{interested: true}
	message := &tg.MessageService{
		ID:     13,
		PeerID: &tg.PeerChannel{ChannelID: 88},
		FromID: &tg.PeerUser{UserID: 42},
		Action: &tg.MessageActionChatDeleteUser{UserID: 42},
	}
	entities := tg.Entities{
		Channels: map[int64]*tg.Channel{
			88: {ID: 88, AccessHash: 188, Megagroup: false, Title: "Broadcast"},
		},
	}

	handleAssistantGroupService(message, entities, UpdateHandlerDeps{
		GroupEvents: ingress,
	})

	if ingress.interestedCalls != 0 || len(ingress.published) != 0 {
		t.Fatalf("broadcast reached group event interest/publish: interested=%d published=%d",
			ingress.interestedCalls, len(ingress.published))
	}
}

func TestP7HGoodbyeMapsDeleteUser(t *testing.T) {
	ingress := &groupServiceIngressStub{interested: true}
	message := &tg.MessageService{
		ID:     14,
		PeerID: &tg.PeerChat{ChatID: 77},
		FromID: &tg.PeerUser{UserID: 7},
		Action: &tg.MessageActionChatDeleteUser{UserID: 42},
	}
	handleAssistantGroupService(message, tg.Entities{
		Chats: map[int64]*tg.Chat{77: {ID: 77}},
		Users: map[int64]*tg.User{42: {ID: 42, FirstName: "Alice"}},
	}, UpdateHandlerDeps{GroupEvents: ingress})

	if len(ingress.published) != 1 ||
		ingress.published[0].Kind != core.GroupServiceMemberLeft ||
		len(ingress.published[0].Users) != 1 ||
		ingress.published[0].Users[0].ID != 42 {
		t.Fatalf("goodbye event=%+v", ingress.published)
	}
}
