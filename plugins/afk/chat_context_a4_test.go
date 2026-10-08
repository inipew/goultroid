package afk

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

func TestA4AFKDoesNotReplyToBroadcastOrAnonymousSenders(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := &mockService{}
	const ownerID int64 = 1001
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	if err := p.Init(); err != nil {
		t.Fatal(err)
	}
	if err := p.enableAFK(context.Background(), "away"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		entities tg.Entities
		from     tg.PeerClass
		wantSend bool
	}{
		{name: "broadcast channel", entities: tg.Entities{Channels: map[int64]*tg.Channel{500: {ID: 500, AccessHash: 123}}}, from: &tg.PeerUser{UserID: 2002}},
		{name: "unknown channel metadata", from: &tg.PeerUser{UserID: 2002}},
		{name: "anonymous channel-backed sender", entities: tg.Entities{Channels: map[int64]*tg.Channel{500: {ID: 500, AccessHash: 123, Megagroup: true}}}, from: &tg.PeerChannel{ChannelID: 999}},
		{name: "real megagroup member", entities: tg.Entities{Channels: map[int64]*tg.Channel{500: {ID: 500, AccessHash: 123, Megagroup: true}}}, from: &tg.PeerUser{UserID: 2002}, wantSend: true},
	}
	for i, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			svc.sent = ""
			msg := &tg.Message{ID: 110 + i, PeerID: &tg.PeerChannel{ChannelID: 500}, FromID: tt.from, Mentioned: true, Message: "@owner"}
			if err := handleMessageEvent(p, context.Background(), tt.entities, msg, false, ""); err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(svc.sent, "currently AFK")
			if got != tt.wantSend {
				t.Fatalf("AFK replied=%v want=%v text=%q", got, tt.wantSend, svc.sent)
			}
		})
	}
}

func TestA4AFKForeignPeerReplyDoesNotLookupCurrentTopic(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := &mockService{messages: map[int]*tg.Message{
		100: {ID: 100, Out: true, Message: "owner wrote this"},
	}}
	const ownerID int64 = 1001
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	if err := p.Init(); err != nil {
		t.Fatal(err)
	}
	if err := p.enableAFK(context.Background(), "away"); err != nil {
		t.Fatal(err)
	}
	entities := tg.Entities{Channels: map[int64]*tg.Channel{500: {ID: 500, AccessHash: 123, Megagroup: true}}}
	cases := []struct {
		name     string
		replyTo  tg.PeerClass
		wantSend bool
	}{
		{name: "foreign channel reply", replyTo: &tg.PeerChannel{ChannelID: 999}},
		{name: "same channel reply", replyTo: &tg.PeerChannel{ChannelID: 500}, wantSend: true},
	}
	for i, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			svc.sent = ""
			msg := &tg.Message{
				ID: 202 + i, PeerID: &tg.PeerChannel{ChannelID: 500}, FromID: &tg.PeerUser{UserID: int64(3000 + i)}, Message: "reply",
				ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 100, ReplyToPeerID: tt.replyTo, ForumTopic: true, ReplyToTopID: 55},
			}
			if err := handleMessageEvent(p, context.Background(), entities, msg, false, ""); err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(svc.sent, "currently AFK")
			if got != tt.wantSend {
				t.Fatalf("AFK replied=%v want=%v text=%q", got, tt.wantSend, svc.sent)
			}
		})
	}
}

func TestA4AFKManualBroadcastDeactivatesWithoutWelcome(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := &mockService{}
	const ownerID int64 = 1001
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return svc })
	client := &afkEffectTestClient{inline: true}
	p.SetTaskClient(client)
	if err := p.enableAFK(context.Background(), "away"); err != nil {
		t.Fatal(err)
	}
	msg := &tg.Message{ID: 400, PeerID: &tg.PeerChannel{ChannelID: 500}, Out: true, Message: "manual post"}
	entities := tg.Entities{Channels: map[int64]*tg.Channel{500: {ID: 500, AccessHash: 111}}}
	if err := handleMessageEvent(p, context.Background(), entities, msg, false, ""); err != nil {
		t.Fatal(err)
	}
	if st := p.state.Load(); st == nil || st.isAFK {
		t.Fatalf("manual owner post must deactivate AFK: %+v", st)
	}
	if n := len(client.snapshot()); n != 0 || svc.sent != "" {
		t.Fatalf("broadcast channel must not receive welcome: effects=%d sent=%q", n, svc.sent)
	}
}
