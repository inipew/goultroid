package userlog_test

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	userlogSvc "github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/plugins/userlog"
	"go.uber.org/zap"
)

func TestA4UserlogIgnoresBroadcastAndAnonymousSender(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	svc := userlogSvc.NewService(userlogSvc.NewSQLiteRepository(db), mockTG, zap.NewNop())
	if err := svc.SetLogChat(context.Background(), 777); err != nil {
		t.Fatal(err)
	}
	p := userlog.New(svc, 12345)
	initializePlugin(t, p)

	cases := []struct {
		name     string
		entities tg.Entities
		from     tg.PeerClass
	}{
		{name: "broadcast mention", entities: tg.Entities{Channels: map[int64]*tg.Channel{500: {ID: 500, AccessHash: 333}}}, from: &tg.PeerUser{UserID: 2002}},
		{name: "missing channel kind", from: &tg.PeerUser{UserID: 2002}},
		{name: "anonymous channel sender", entities: tg.Entities{Channels: map[int64]*tg.Channel{500: {ID: 500, AccessHash: 333, Megagroup: true}}}, from: &tg.PeerChannel{ChannelID: 600}},
	}
	for i, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			msg := &tg.Message{ID: 310 + i, PeerID: &tg.PeerChannel{ChannelID: 500}, FromID: tt.from, Message: "@owner", Mentioned: true}
			if err := handleMessageEvent(p, context.Background(), tt.entities, msg, false, ""); err != nil {
				t.Fatal(err)
			}
		})
	}
	if got := mockTG.getSent(); got != "" {
		t.Fatalf("broadcast/anonymous sender was logged as user: %q", got)
	}
}
