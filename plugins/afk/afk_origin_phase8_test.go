package afk

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

func TestPhase8AFKManualCrossPeerMessageIDCollision(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := &botSentMockService{
		botSentPeerID: 2002,
		botSentIDs:    map[int]bool{999: true},
	}
	p := New(NewSQLiteRepository(db), 1001, func() core.TelegramServicer { return svc })
	if err := p.Init(); err != nil {
		t.Fatal(err)
	}
	if err := p.enableAFK(context.Background(), "away"); err != nil {
		t.Fatal(err)
	}
	entities := tg.Entities{Users: map[int64]*tg.User{3003: {ID: 3003, AccessHash: 888}}}
	msg := &tg.Message{ID: 999, Out: true, PeerID: &tg.PeerUser{UserID: 3003}, Message: "human message"}
	if err := handleMessageEvent(p, context.Background(), entities, msg, false, ""); err != nil {
		t.Fatal(err)
	}
	if st := p.state.Load(); st == nil || st.isAFK {
		t.Fatalf("manual message in another peer did not deactivate AFK: %+v", st)
	}
}
