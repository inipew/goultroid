package pmpermit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	pmpermitSvc "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/plugins/pmpermit"
	"go.uber.org/zap"
)

func TestA4PMPermitIncomingPrivateSenderMismatchFailClosed(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := pmpermitSvc.NewService(pmpermit.NewSQLiteRepository(db), &mockTelegram{}, 1, core.NewPermissions(1, nil), zap.NewNop())
	p := pmpermit.New(svc)
	cases := []struct {
		name string
		from tg.PeerClass
	}{
		{name: "mismatched user", from: &tg.PeerUser{UserID: 1}},
		{name: "anonymous channel", from: &tg.PeerChannel{ChannelID: 900}},
	}
	for i, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			msg := &tg.Message{ID: 401 + i, PeerID: &tg.PeerUser{UserID: 2002}, FromID: tt.from, Message: "unsafe private origin"}
			entities := tg.Entities{Users: map[int64]*tg.User{2002: {ID: 2002, AccessHash: 123}}}
			err := handleMessageEvent(p, context.Background(), entities, msg, false, "")
			if !errors.Is(err, core.ErrInterceptHandled) {
				t.Fatalf("non-matching private sender must be intercepted: %v", err)
			}
		})
	}
	if approved, err := svc.IsApproved(context.Background(), 2002); err != nil || approved {
		t.Fatalf("invalid sender changed PM approval: approved=%v err=%v", approved, err)
	}
}
